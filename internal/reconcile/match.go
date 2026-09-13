package reconcile

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"hol-up-prototype/internal/realtime"
	"hol-up-prototype/internal/schedule"
)

type identity struct {
	known                  bool
	origin                 time.Duration
	route, direction, path string
}

// Only the documented suffix has semantics. Historical and current prefixes
// remain opaque. Integer 600ms units preserve hundredths of a minute exactly.
var identifier = regexp.MustCompile(`(?:^|_)(-?[0-9]{6,8})_([A-Za-z0-9]+)\.\.([NSEW])([A-Za-z0-9]*)$`)
var startTime = regexp.MustCompile(`^[0-9]{2,}:[0-5][0-9]:[0-5][0-9]$`)

func parseID(id string) identity {
	parts := identifier.FindStringSubmatch(id)
	if parts == nil {
		return identity{}
	}
	units, err := strconv.ParseInt(parts[1], 10, 32)
	if err != nil {
		return identity{}
	}
	return identity{true, time.Duration(units) * 600 * time.Millisecond, parts[2], parts[3], parts[4]}
}
func parseStart(value string) (time.Duration, bool) {
	if !startTime.MatchString(value) {
		return 0, false
	}
	parts := strings.Split(value, ":")
	hours, err := strconv.ParseInt(parts[0], 10, 32)
	if err != nil || hours > 596523 {
		return 0, false
	}
	minutes, _ := strconv.Atoi(parts[1])
	seconds, _ := strconv.Atoi(parts[2])
	return time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute + time.Duration(seconds)*time.Second, true
}

type staticTrip struct {
	trip schedule.Trip
	id   identity
}

func origin(t staticTrip) time.Duration {
	if t.id.known {
		return t.id.origin
	}
	return time.Duration(t.trip.Stops[0].Departure.Seconds) * time.Second
}
func nyctDirection(value string) realtime.Direction {
	switch value {
	case "N":
		return realtime.DirectionNorthbound
	case "S":
		return realtime.DirectionSouthbound
	case "E":
		return realtime.DirectionEastbound
	case "W":
		return realtime.DirectionWestbound
	}
	return realtime.DirectionUnknown
}

func matchTrip(m *Match, trips []staticTrip, dataset *schedule.Dataset, now time.Time) {
	train := m.Train
	id := parseID(train.TripID)
	dates := dataset.CandidateServiceDates(now)
	if train.ServiceDate != "" {
		date, err := schedule.ParseDate(train.ServiceDate)
		if err != nil {
			m.Outcome, m.Reason = Invalid, "invalid_service_date"
			return
		}
		dates = []schedule.Date{date}
	}
	var selected []staticTrip
	if trip, ok := dataset.Trip(train.TripID); ok {
		selected = append(selected, staticTrip{trip, id})
	}
	m.Rule = "exact_id"
	// ponytail: linear suffix scans; add indexes if measured runs delay refreshes.
	if len(selected) == 0 {
		if id.known && id.path != "" {
			m.Rule = "origin_path"
			for _, trip := range trips {
				if trip.id == id {
					selected = append(selected, trip)
				}
			}
		} else {
			m.Rule = "origin_route_direction"
			route, dir, at, hasOrigin := train.RouteID, "", time.Duration(0), false
			if id.known {
				route, dir, at, hasOrigin = id.route, id.direction, id.origin, true
			} else if train.StartTime != "" {
				at, hasOrigin = parseStart(train.StartTime)
			}
			for _, trip := range trips {
				directionKnown := false
				directionMatches := true
				if dir != "" {
					directionKnown = true
					directionMatches = trip.id.known && trip.id.direction == dir
				}
				if !id.known && train.NYCTDirection != nil {
					directionKnown = true
					directionMatches = directionMatches && trip.id.known && nyctDirection(trip.id.direction) == *train.NYCTDirection
				}
				if !id.known && train.DirectionID != nil {
					directionKnown = true
					directionMatches = directionMatches && trip.trip.Direction >= 0 && uint32(trip.trip.Direction) == *train.DirectionID
				}
				if hasOrigin && route != "" && directionKnown && directionMatches && trip.trip.RouteID == route && origin(trip) == at {
					selected = append(selected, trip)
				}
			}
		}
	}
	if len(selected) == 0 {
		m.Outcome, m.Reason = Unmatched, "no_identity_candidate"
		return
	}
	var compatible []Instance
	var potential []Instance
	reason := "service_inactive_or_outside_coverage"
	for _, candidate := range selected {
		for _, date := range dates {
			instance := Instance{candidate.trip.ID, date}
			potential = append(potential, instance)
			if !dataset.Active(candidate.trip.ServiceID, date) {
				m.Rejections = append(m.Rejections, Rejection{instance, "service_inactive_or_outside_coverage"})
				continue
			}
			if conflict := identityConflict(train, id, candidate); conflict != "" {
				reason = conflict
				m.Rejections = append(m.Rejections, Rejection{instance, conflict})
				continue
			}
			compatible = append(compatible, instance)
		}
	}
	m.Candidates = compatible
	switch len(compatible) {
	case 0:
		m.Candidates = potential
		m.Outcome, m.Reason = Invalid, reason
	case 1:
		m.Outcome, m.Reason = Matched, "unique_identity"
	default:
		m.Outcome, m.Reason = Ambiguous, "multiple_scheduled_instances"
	}
}

func identityConflict(train realtime.Train, id identity, candidate staticTrip) string {
	trip, staticID := candidate.trip, candidate.id
	if train.RouteID != "" && train.RouteID != trip.RouteID {
		return "route_conflict"
	}
	if train.DirectionID != nil && (trip.Direction < 0 || uint32(trip.Direction) != *train.DirectionID) {
		return "direction_id_conflict"
	}
	if id.known {
		if id.route != trip.RouteID || (train.RouteID != "" && train.RouteID != id.route) {
			return "identifier_route_conflict"
		}
		if id.origin != origin(candidate) {
			return "identifier_origin_conflict"
		}
		if staticID.known && (id.direction != staticID.direction || (id.path != "" && id.path != staticID.path)) {
			return "identifier_path_conflict"
		}
	}
	if train.StartTime != "" {
		at, ok := parseStart(train.StartTime)
		if !ok {
			return "invalid_start_time"
		}
		if at != origin(candidate) || (id.known && at != id.origin) {
			return "start_time_conflict"
		}
	}
	if train.NYCTDirection != nil {
		if !staticID.known || *train.NYCTDirection == realtime.DirectionUnknown || *train.NYCTDirection != nyctDirection(staticID.direction) || (id.known && *train.NYCTDirection != nyctDirection(id.direction)) {
			return "nyct_direction_conflict"
		}
	}
	return ""
}

func resolveStops(updates []realtime.StopPrediction, stops []schedule.StopTime) []StopResolution {
	out := make([]StopResolution, len(updates))
	counts := map[int]int{}
	for i, update := range updates {
		result := StopResolution{UpdateIndex: i, ScheduledIndex: -1, Outcome: Invalid, Reason: "missing_stop_identity"}
		var candidates []int
		for j, stop := range stops {
			if update.Sequence != nil {
				if *update.Sequence == stop.Sequence {
					candidates = append(candidates, j)
				}
			} else if update.StopID != "" && update.StopID == stop.StopID {
				candidates = append(candidates, j)
			}
		}
		if update.Sequence != nil || update.StopID != "" {
			result.Reason = "unknown_stop"
		}
		if len(candidates) > 1 {
			result.Outcome, result.Reason = Ambiguous, "repeated_stop"
		}
		if len(candidates) == 1 {
			j := candidates[0]
			if update.StopID != "" && update.StopID != stops[j].StopID {
				result.Reason = "stop_id_sequence_conflict"
			} else {
				result.ScheduledIndex, result.StopID, result.Sequence = j, stops[j].StopID, stops[j].Sequence
				result.Outcome, result.Reason = Matched, "resolved"
				counts[j]++
			}
		}
		out[i] = result
	}
	order := make([]int, len(updates))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		ai, bi := a, b
		if updates[a].InputIndex != nil {
			ai = int(*updates[a].InputIndex)
		}
		if updates[b].InputIndex != nil {
			bi = int(*updates[b].InputIndex)
		}
		return ai - bi
	})
	for i := range out {
		if out[i].ScheduledIndex >= 0 && counts[out[i].ScheduledIndex] > 1 {
			out[i].Outcome, out[i].Reason = Ambiguous, "duplicate_stop_update"
		}
	}
	// Both passes mark every occurrence participating in an inversion, even
	// when the conflicting stops are not adjacent in the input.
	highest := -1
	for _, i := range order {
		if out[i].ScheduledIndex < 0 {
			continue
		}
		if out[i].ScheduledIndex < highest {
			out[i].Outcome, out[i].Reason = Invalid, "conflicting_stop_order"
		}
		highest = max(highest, out[i].ScheduledIndex)
	}
	lowest := len(stops)
	for _, i := range slices.Backward(order) {
		if out[i].ScheduledIndex < 0 {
			continue
		}
		if out[i].ScheduledIndex > lowest {
			out[i].Outcome, out[i].Reason = Invalid, "conflicting_stop_order"
		}
		lowest = min(lowest, out[i].ScheduledIndex)
	}
	return out
}

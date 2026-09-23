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

func matchTrip(m *Match, trips []staticTrip, dataset *schedule.Dataset) {
	train := m.Train
	id := parseID(train.TripID)
	var dates []schedule.Date
	if train.ServiceDate != "" {
		date, err := schedule.ParseDate(train.ServiceDate)
		if err != nil {
			m.Outcome, m.Reason = Invalid, "invalid_service_date"
			return
		}
		dates = []schedule.Date{date}
	} else {
		// Trip observations survive refreshed headers; absent observations use
		// the retained feed time. Neither can acquire a date from publication.
		at := m.Reference.Source.SourceTimestamp
		if train.TripUpdateTimestampPresent || !train.TripUpdateTimestamp.IsZero() {
			at = train.TripUpdateTimestamp
			receivedAt := m.Reference.Source.ReceivedAt
			if receivedAt.IsZero() {
				receivedAt = m.Reference.Source.SourceTimestamp
			}
			if at.IsZero() || at.Unix() <= 0 || at.Year() > 9999 || at.After(receivedAt) {
				m.Outcome, m.Reason = Invalid, "invalid_trip_update_timestamp"
				return
			}
		}
		dates = dataset.CandidateServiceDates(at)
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
	var warnings []string
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
			conflict := identityConflict(train, id, candidate)
			tolerated := conflict == "start_time_conflict" && (m.Rule == "exact_id" || m.Rule == "origin_path")
			if conflict != "" && !tolerated {
				reason = conflict
				m.Rejections = append(m.Rejections, Rejection{instance, conflict})
				continue
			}
			compatible = append(compatible, instance)
			warnings = nil
			if tolerated {
				warnings = []string{conflict}
			}
		}
	}
	m.Candidates = compatible
	switch len(compatible) {
	case 0:
		m.Candidates = potential
		m.Outcome, m.Reason = Invalid, reason
	case 1:
		m.Outcome, m.Reason = Matched, "unique_identity"
		m.Warnings = warnings
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
	// Check every other supplied identity field before allowing a start-time
	// disagreement to become a warning for a stronger identity rule.
	if train.NYCTDirection != nil {
		if !staticID.known || *train.NYCTDirection == realtime.DirectionUnknown || *train.NYCTDirection != nyctDirection(staticID.direction) || (id.known && *train.NYCTDirection != nyctDirection(id.direction)) {
			return "nyct_direction_conflict"
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
	return ""
}

func resolveStops(updates []realtime.StopPrediction, stops []schedule.StopTime, tolerateIDConflict bool) []StopResolution {
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
				if tolerateIDConflict {
					var byID []int
					for k, stop := range stops {
						if stop.StopID == update.StopID {
							byID = append(byID, k)
						}
					}
					if len(byID) == 1 {
						j = byID[0]
						result.Warnings = []string{"stop_id_sequence_conflict"}
					}
				}
			}
			if update.StopID == "" || update.StopID == stops[j].StopID {
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
	for i := range out {
		if out[i].Outcome != Matched {
			out[i].Warnings = nil
		}
	}
	return out
}

// ResolveStop keeps ID/sequence agreement strict for vehicle observations.
func ResolveStop(update realtime.StopPrediction, stops []schedule.StopTime) StopResolution {
	return resolveStops([]realtime.StopPrediction{update}, stops, false)[0]
}

// MatchRoutes retains conflicting route evidence so each affected route can
// expose the null outcome. A sole configured route can attribute an otherwise
// unlocalized source record, but multiple configured routes cannot.
func (b *Bundle) MatchRoutes(m Match) []string {
	var routes []string
	add := func(route string) {
		if route != "" && !slices.Contains(routes, route) {
			routes = append(routes, route)
		}
	}
	add(m.Train.RouteID)
	add(parseID(m.Train.TripID).route)
	if b != nil && b.version != nil {
		ids := []string{m.Train.TripID, m.Instance.TripID}
		for _, candidate := range m.Candidates {
			ids = append(ids, candidate.TripID)
		}
		for _, id := range ids {
			if trip, ok := b.version.Dataset().Trip(id); ok {
				add(trip.RouteID)
			}
		}
	}
	if len(routes) == 0 && len(m.Reference.Source.Routes) == 1 {
		add(m.Reference.Source.Routes[0])
	}
	slices.Sort(routes)
	return routes
}

func (b *Bundle) RouteMatches(routeID string) []Match {
	var out []Match
	for _, match := range b.Matches() {
		if slices.Contains(b.MatchRoutes(match), routeID) {
			out = append(out, match)
		}
	}
	return out
}

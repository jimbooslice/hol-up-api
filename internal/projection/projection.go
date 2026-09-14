// Package projection calculates positions from one immutable reconciliation
// bundle and one evaluation time. It keeps no tracks or position history.
package projection

import (
	"encoding/base64"
	"encoding/json"
	"sort"
	"time"

	"hol-up-prototype/internal/realtime"
	"hol-up-prototype/internal/reconcile"
	"hol-up-prototype/internal/schedule"
)

type Position struct {
	RouteID                    string
	Direction                  realtime.Direction
	DirectionID                *int
	StaticTripID               string
	ServiceDate                string
	TripInstanceID             string
	RealtimeTripID             string
	RealtimeServiceDate        string
	SourceRecordID             string
	SourceName                 string
	EntityID                   string
	SourceIndex                int
	VehicleEntityID            string
	VehicleMatch               realtime.VehicleMatch
	ShapeID                    string
	PatternID                  string
	ScheduleVersion            string
	Latitude, Longitude        *float64
	ProjectionTime             time.Time
	PositionBasis              realtime.PositionBasis
	MovementState              realtime.MovementState
	SourceTimestamp            time.Time
	ReceivedAt                 time.Time
	TripUpdateTimestamp        time.Time
	VehicleTimestamp           time.Time
	ObservationTime            time.Time
	ScheduleValidatedAt        time.Time
	TripUpdateTimestampPresent bool
	VehicleTimestampPresent    bool
	Freshness                  realtime.Freshness
	TripUpdateFreshness        realtime.Freshness
	VehicleFreshness           realtime.Freshness
	ObservationBasis           string
	InterpolationMethod        string
	ReconciliationOutcome      reconcile.Outcome
	ReconciliationReason       string
	FallbackReason             string
	NullReason                 string
	distanceMeters             float64
}

// Positions preserves each attributable trip update, including duplicates and
// unidentified records. Only reconciliation can authorize additional runs.
func Positions(b *reconcile.Bundle, routeID string, now time.Time) []Position {
	out := []Position{}
	if b == nil {
		return out
	}
	meta := b.MetadataAt(now)
	present := map[reconcile.Instance]bool{}
	for _, m := range b.RouteMatches(routeID) {
		p := Position{RouteID: routeID, Direction: m.Train.Direction, RealtimeTripID: m.Train.TripID,
			RealtimeServiceDate: m.Train.ServiceDate, SourceName: m.Reference.Source.Name, EntityID: m.Train.EntityID,
			SourceIndex: m.Reference.Index, VehicleEntityID: m.Train.VehicleEntityID, VehicleMatch: m.Train.VehicleMatch,
			SourceTimestamp: m.Reference.Source.SourceTimestamp, ReceivedAt: m.Reference.Source.ReceivedAt,
			TripUpdateTimestamp: m.Train.TripUpdateTimestamp, VehicleTimestamp: m.Train.VehicleTimestamp,
			TripUpdateTimestampPresent: m.Train.TripUpdateTimestampPresent, VehicleTimestampPresent: m.Train.VehicleTimestampPresent,
			ProjectionTime: now, PositionBasis: realtime.PositionBasisRealtimePrediction, MovementState: realtime.MovementStateUnknown,
			ScheduleVersion: meta.Schedule.Version, ScheduleValidatedAt: meta.Schedule.ValidatedAt,
			ReconciliationOutcome: m.Outcome, ReconciliationReason: m.Reason}
		if p.Direction == "" {
			p.Direction = realtime.DirectionUnknown
		}
		if p.VehicleMatch == "" {
			p.VehicleMatch = realtime.VehicleUnmatched
		}
		if m.Train.DirectionID != nil && *m.Train.DirectionID <= 1 {
			direction := int(*m.Train.DirectionID)
			p.DirectionID = &direction
		}
		key, _ := json.Marshal([]any{p.SourceName, p.EntityID, m.Unidentified, p.SourceIndex})
		p.SourceRecordID = base64.RawURLEncoding.EncodeToString(key)
		p.Freshness = b.FreshnessAt(p.SourceTimestamp, now)
		p.TripUpdateFreshness, p.ObservationBasis = m.Train.PredictionFreshnessAt(p.SourceTimestamp, now, b.StaleAfter())
		p.ObservationTime = p.TripUpdateTimestamp
		if p.ObservationBasis == "feed_header" {
			p.ObservationTime = p.SourceTimestamp
		}
		p.VehicleFreshness = realtime.ObservationFreshness(p.VehicleTimestamp, p.Freshness, now, b.StaleAfter())
		if m.Instance.TripID != "" {
			present[m.Instance] = true
			setInstance(&p, b, m.Instance)
		}
		selectPosition(&p, b, m, now)
		checkVehicleInterval(&p, b, m)
		out = append(out, p)
	}
	if b.ScheduleEligible(now) {
		d := b.Schedule().Dataset()
		for _, date := range d.CandidateServiceDates(now) {
			for _, id := range d.RouteTrips(routeID) {
				instance := reconcile.Instance{TripID: id, ServiceDate: date}
				if present[instance] {
					continue
				}
				trip, _ := d.Trip(id)
				if !d.Active(trip.ServiceID, date) || now.Before(date.Instant(trip.Stops[0].Departure.Seconds)) || now.After(date.Instant(trip.Stops[len(trip.Stops)-1].Arrival.Seconds)) {
					continue
				}
				decision := b.SimulationDecision(instance, now)
				if !decision.Allowed {
					continue
				}
				p := Position{RouteID: routeID, Direction: realtime.DirectionUnknown, ProjectionTime: now,
					PositionBasis: realtime.PositionBasisScheduleFallback, MovementState: realtime.MovementStateUnknown,
					ScheduleVersion: meta.Schedule.Version, ScheduleValidatedAt: meta.Schedule.ValidatedAt,
					ReconciliationOutcome: reconcile.Unavailable, ReconciliationReason: "scheduled_without_realtime", FallbackReason: decision.Reason,
					TripUpdateFreshness: realtime.FreshnessUnavailable, VehicleFreshness: realtime.FreshnessUnavailable, VehicleMatch: realtime.VehicleUnmatched,
					ObservationBasis: "none"}
				for _, source := range b.Sources() {
					for _, route := range source.Routes {
						if route == routeID {
							p.SourceName, p.SourceTimestamp, p.ReceivedAt = source.Name, source.SourceTimestamp, source.ReceivedAt
						}
					}
				}
				p.Freshness = b.FreshnessAt(p.SourceTimestamp, now)
				setInstance(&p, b, instance)
				simulate(&p, b, instance, now)
				out = append(out, p)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, c := out[i], out[j]
		if a.TripInstanceID != c.TripInstanceID {
			return a.TripInstanceID < c.TripInstanceID
		}
		return a.SourceRecordID < c.SourceRecordID
	})
	return out
}

func setInstance(p *Position, b *reconcile.Bundle, instance reconcile.Instance) {
	p.StaticTripID, p.ServiceDate, p.TripInstanceID = instance.TripID, string(instance.ServiceDate), instance.ID()
	t, _ := b.Schedule().Dataset().Trip(instance.TripID)
	p.ShapeID = t.ShapeID
	if t.Direction >= 0 {
		direction := t.Direction
		p.DirectionID = &direction
	}
	if pattern, ok := b.Schedule().Dataset().TripPattern(t.ID); ok {
		p.PatternID = pattern.ID
	}
}

// Fresh current-stop evidence constrains an inferred moving position to the
// incoming segment. A compatible clock interval alone cannot override it.
func checkVehicleInterval(p *Position, b *reconcile.Bundle, m reconcile.Match) {
	if p.Latitude == nil || p.PositionBasis != realtime.PositionBasisRealtimePrediction || p.VehicleFreshness != realtime.FreshnessFresh || m.Train.VehicleMatch != realtime.VehicleMatched || m.VehicleStop == nil || (m.Train.VehicleStatus != "IN_TRANSIT_TO" && m.Train.VehicleStatus != "INCOMING_AT") {
		return
	}
	pattern, _ := b.Schedule().Dataset().TripPattern(m.Instance.TripID)
	to := m.VehicleStop.ScheduledIndex
	from := to - 1
	for from >= 0 {
		skipped := false
		for _, stop := range m.Stops {
			if stop.ScheduledIndex == from && m.Train.Stops[stop.UpdateIndex].Relationship == realtime.StopSkipped {
				skipped = true
			}
		}
		if !skipped {
			break
		}
		from--
	}
	if from < 0 || to >= len(pattern.Stops) || !pattern.Stops[from].HasDistance || !pattern.Stops[to].HasDistance || p.distanceMeters < pattern.Stops[from].DistanceMeters || p.distanceMeters > pattern.Stops[to].DistanceMeters {
		p.Latitude, p.Longitude, p.InterpolationMethod = nil, nil, ""
		p.MovementState, p.NullReason = realtime.MovementStateUnknown, "vehicle_interval_conflict"
	}
}

func selectPosition(p *Position, b *reconcile.Bundle, m reconcile.Match, now time.Time) {
	switch {
	case m.Train.Canceled():
		p.NullReason = "canceled"
		return
	case m.Outcome != reconcile.Matched:
		p.NullReason = m.Reason
		return
	case !b.ScheduleEligible(now):
		p.NullReason = "schedule_ineligible"
		return
	case !m.Owned:
		p.NullReason = "not_route_owner"
		return
	case m.Train.ScheduleRelationship != "" && m.Train.ScheduleRelationship != "SCHEDULED":
		p.NullReason = "unsupported_schedule_relationship"
		return
	case p.SourceTimestamp.After(now):
		p.NullReason = "future_source_timestamp"
		return
	case p.TripUpdateFreshness == realtime.FreshnessUnavailable:
		p.NullReason = p.ObservationBasis
		return
	}
	trip, _ := b.Schedule().Dataset().Trip(m.Instance.TripID)
	if reason := reconcile.PositionEvidenceReason(m, trip); reason != "" {
		p.NullReason = reason
		return
	}
	if p.TripUpdateFreshness == realtime.FreshnessStale {
		decision := b.SimulationDecision(m.Instance, now)
		p.FallbackReason = decision.Reason
		if !decision.Allowed {
			p.NullReason = decision.Reason
			return
		}
		p.PositionBasis = realtime.PositionBasisScheduleFallback
		simulate(p, b, m.Instance, now)
		return
	}
	t, pattern, shape, ok := geometry(p, b, m.Instance)
	if !ok {
		return
	}
	if m.Train.VehicleStatus == "STOPPED_AT" && m.Train.VehicleMatch == realtime.VehicleMatched && p.VehicleFreshness == realtime.FreshnessFresh && m.VehicleStop != nil {
		index := m.VehicleStop.ScheduledIndex
		if coordinate(p, pattern, shape, index, index, 0) {
			p.InterpolationMethod, p.MovementState = "vehicle_stop", realtime.MovementStateReportedStopped
			p.ObservationTime, p.ObservationBasis = p.VehicleTimestamp, "vehicle"
			return
		}
		return
	}
	times := make([]stopTimes, len(t.Stops))
	for _, stop := range m.Stops {
		u := m.Train.Stops[stop.UpdateIndex]
		i := stop.ScheduledIndex
		times[i].skipped = u.Relationship == realtime.StopSkipped
		times[i].blocked = u.Relationship == realtime.StopNoData
		if times[i].skipped || times[i].blocked {
			continue
		}
		times[i].arrival, times[i].arrivalMethod, _ = eventTime(u.Arrival, m.Train.TripDelaySeconds, t.Stops[i].Arrival, m.Instance.ServiceDate)
		times[i].departure, times[i].departureMethod, _ = eventTime(u.Departure, m.Train.TripDelaySeconds, t.Stops[i].Departure, m.Instance.ServiceDate)
	}
	if interpolate(p, pattern, shape, times, now, false) {
		return
	}
	if p.NullReason != "insufficient_timing" {
		return
	}
	// Only the next arrival can borrow a scheduled duration. It cannot cross
	// NO_DATA, extrapolate, or replace an explicit conflicting departure.
	for i, at := range times {
		if at.skipped || at.blocked || at.arrival.IsZero() || at.arrival.Before(now) {
			continue
		}
		j := i - 1
		for j >= 0 && times[j].skipped {
			j--
		}
		if j < 0 || times[j].blocked || !times[j].departure.IsZero() || !times[j].arrival.IsZero() {
			continue
		}
		from, to := t.Stops[j].Departure, t.Stops[i].Arrival
		if !from.Present || !to.Present || to.Seconds <= from.Seconds {
			continue
		}
		departure := at.arrival.Add(-time.Duration(to.Seconds-from.Seconds) * time.Second)
		if now.Before(departure) {
			continue
		}
		if coordinate(p, pattern, shape, j, i, now.Sub(departure).Seconds()/at.arrival.Sub(departure).Seconds()) {
			p.InterpolationMethod, p.MovementState = "next_prediction_schedule_duration", realtime.MovementStateInferredMoving
		}
		return
	}
}

func geometry(p *Position, b *reconcile.Bundle, instance reconcile.Instance) (schedule.Trip, schedule.Pattern, schedule.Shape, bool) {
	d := b.Schedule().Dataset()
	t, _ := d.Trip(instance.TripID)
	pattern, ok := d.TripPattern(t.ID)
	if !ok {
		p.NullReason = "pattern_missing"
		return t, pattern, schedule.Shape{}, false
	}
	shape, ok := d.Shape(t.ShapeID)
	if !ok || !shape.Usable {
		p.NullReason = "shape_missing"
		if shape.Reason != "" {
			p.NullReason = shape.Reason
		}
		return t, pattern, shape, false
	}
	return t, pattern, shape, true
}

func simulate(p *Position, b *reconcile.Bundle, instance reconcile.Instance, now time.Time) {
	t, pattern, shape, ok := geometry(p, b, instance)
	if !ok {
		return
	}
	times := make([]stopTimes, len(t.Stops))
	for i, s := range t.Stops {
		if s.Arrival.Present {
			times[i].arrival = instance.ServiceDate.Instant(s.Arrival.Seconds)
		}
		if s.Departure.Present {
			times[i].departure = instance.ServiceDate.Instant(s.Departure.Seconds)
		}
	}
	interpolate(p, pattern, shape, times, now, true)
}

type stopTimes struct {
	arrival, departure             time.Time
	arrivalMethod, departureMethod string
	skipped, blocked               bool
}

func eventTime(event *realtime.StopEvent, tripDelay *int32, scheduled schedule.OptionalTime, date schedule.Date) (time.Time, string, bool) {
	var instant time.Time
	if scheduled.Present {
		instant = date.Instant(scheduled.Seconds)
	}
	return realtime.EventTime(event, tripDelay, instant)
}

func interpolate(p *Position, pattern schedule.Pattern, shape schedule.Shape, stops []stopTimes, now time.Time, simulation bool) bool {
	p.NullReason = "insufficient_timing"
	left := -1
	var departure time.Time
	leftMethod := ""
	for i, s := range stops {
		if s.blocked {
			left = -1
			continue
		}
		if s.skipped {
			continue
		}
		if !s.arrival.IsZero() && !s.departure.IsZero() && !now.Before(s.arrival) && !now.After(s.departure) {
			if !coordinate(p, pattern, shape, i, i, 0) {
				return false
			}
			p.InterpolationMethod = "realtime_dwell"
			if s.arrivalMethod == "delay_derived" || s.departureMethod == "delay_derived" {
				p.InterpolationMethod = "delay_derived_dwell"
			}
			if simulation {
				p.InterpolationMethod = "schedule_dwell"
			}
			return true
		}
		arrival, method := s.arrival, s.arrivalMethod
		if arrival.IsZero() {
			arrival, method = s.departure, s.departureMethod
		}
		if left >= 0 && !arrival.IsZero() && arrival.After(departure) && !now.Before(departure) && !now.After(arrival) {
			if !coordinate(p, pattern, shape, left, i, now.Sub(departure).Seconds()/arrival.Sub(departure).Seconds()) {
				return false
			}
			p.InterpolationMethod, p.MovementState = "realtime_events", realtime.MovementStateInferredMoving
			if method == "delay_derived" || leftMethod == "delay_derived" {
				p.InterpolationMethod = "delay_derived"
			}
			if simulation {
				p.InterpolationMethod, p.MovementState = "schedule", realtime.MovementStateUnknown
			}
			return true
		}
		at, method := s.departure, s.departureMethod
		if at.IsZero() {
			at, method = s.arrival, s.arrivalMethod
		}
		if !at.IsZero() && !at.After(now) {
			left, departure, leftMethod = i, at, method
		}
	}
	return false
}

func coordinate(p *Position, pattern schedule.Pattern, shape schedule.Shape, from, to int, fraction float64) bool {
	if from < 0 || to < 0 || from >= len(pattern.Stops) || to >= len(pattern.Stops) {
		p.NullReason = "stop_anchor_missing"
		return false
	}
	a, b := pattern.Stops[from], pattern.Stops[to]
	if !a.HasDistance || !b.HasDistance {
		p.NullReason = "stop_anchor_missing"
		if !a.HasDistance && a.Reason != "" {
			p.NullReason = a.Reason
		} else if b.Reason != "" {
			p.NullReason = b.Reason
		}
		return false
	}
	if b.DistanceMeters < a.DistanceMeters || fraction < 0 || fraction > 1 {
		p.NullReason = "invalid_distance_interval"
		return false
	}
	p.distanceMeters = a.DistanceMeters + (b.DistanceMeters-a.DistanceMeters)*fraction
	lat, lon, ok := shape.At(p.distanceMeters)
	if !ok {
		p.NullReason = "invalid_shape_distance"
		return false
	}
	p.Latitude, p.Longitude, p.NullReason = &lat, &lon, ""
	return true
}

package reconcile

import (
	"slices"
	"time"

	"hol-up-prototype/internal/realtime"
	"hol-up-prototype/internal/schedule"
)

type SimulationPermission struct {
	Allowed bool
	Reason  string
}

// routeOwner returns -1 without an owner, or -2 for ambiguous ownership.
func (b *Bundle) routeOwner(routeID string) int {
	owner := -1
	for i, source := range b.sources {
		if slices.Contains(source.provenance.Routes, routeID) {
			if owner >= 0 {
				return -2
			}
			owner = i
		}
	}
	return owner
}

// SimulationDecision authorizes a scheduled position from retained evidence.
// Unlike Fallback, stale snapshots retain vetoes, and an identified stale run
// remains present in its original replacement window.
func (b *Bundle) SimulationDecision(instance Instance, now time.Time) SimulationPermission {
	if !b.ScheduleEligible(now) {
		return SimulationPermission{Reason: "schedule_ineligible"}
	}
	trip, ok := b.version.Dataset().Trip(instance.TripID)
	if !ok || !b.version.Dataset().Active(trip.ServiceID, instance.ServiceDate) {
		return SimulationPermission{Reason: "instance_ineligible"}
	}
	owner := b.routeOwner(trip.RouteID)
	if owner == -2 {
		return SimulationPermission{Reason: "ambiguous_route_owner"}
	}
	if owner < 0 {
		return SimulationPermission{Reason: "missing_route_owner"}
	}
	source := b.sources[owner]
	p := source.provenance
	if p.SourceTimestamp.IsZero() || p.ReceivedAt.IsZero() {
		return SimulationPermission{Reason: "source_never_loaded"}
	}
	if p.Name == "" || !p.Complete || p.NYCTVersion != "1.0" || p.SourceTimestamp.Unix() <= 0 || p.SourceTimestamp.After(now) || p.ReceivedAt.After(now) {
		return SimulationPermission{Reason: "source_metadata_unusable"}
	}
	present := false
	for _, m := range source.matches {
		if m.Outcome != Matched || m.Instance != instance {
			continue
		}
		present = true
		if m.Train.Canceled() {
			return SimulationPermission{Reason: "canceled"}
		}
		if m.Train.ScheduleRelationship != "" && m.Train.ScheduleRelationship != "SCHEDULED" {
			return SimulationPermission{Reason: "unsupported_trip_relationship"}
		}
		if reason := PositionEvidenceReason(m, trip); reason != "" {
			return SimulationPermission{Reason: reason}
		}
		if (m.Train.VehicleTimestampPresent || !m.Train.VehicleTimestamp.IsZero()) && (m.Train.VehicleTimestamp.IsZero() || m.Train.VehicleTimestamp.Unix() <= 0 || m.Train.VehicleTimestamp.After(now)) {
			return SimulationPermission{Reason: "invalid_vehicle_timestamp"}
		}
		freshness, _ := m.Train.PredictionFreshnessAt(p.SourceTimestamp, now, b.staleAfter)
		if freshness == realtime.FreshnessUnavailable {
			return SimulationPermission{Reason: "invalid_observation_timestamp"}
		}
		if freshness == realtime.FreshnessFresh {
			return SimulationPermission{Reason: "fresh_realtime_counterpart"}
		}
	}
	if source.blockedSource || source.blockedRoutes[trip.RouteID] || source.blocked[instance] {
		return SimulationPermission{Reason: "uncertain_counterpart_or_metadata"}
	}
	// Evidence in a non-owning source cannot establish a second run or absence.
	for i, other := range b.sources {
		if i == owner {
			continue
		}
		for _, match := range other.matches {
			if slices.Contains(b.MatchRoutes(match), trip.RouteID) {
				return SimulationPermission{Reason: "uncertain_source_association"}
			}
		}
	}
	departure := instance.ServiceDate.Instant(trip.Stops[0].Departure.Seconds)
	arrival := instance.ServiceDate.Instant(trip.Stops[len(trip.Stops)-1].Arrival.Seconds)
	if !present && replacementContains(source.rules, trip.RouteID, departure) {
		return SimulationPermission{Reason: "absent_from_replacement_period"}
	}
	if now.Before(departure) || now.After(arrival) {
		return SimulationPermission{Reason: "outside_scheduled_interval"}
	}
	if present {
		return SimulationPermission{Allowed: true, Reason: "stale_realtime_counterpart"}
	}
	return SimulationPermission{Allowed: true, Reason: "no_realtime_counterpart"}
}

// PositionEvidenceReason validates intrinsic conflicts before either realtime
// projection or stale simulation. Missing timing is not itself invalid, and
// NO_DATA breaks timing continuity without inheriting a trip-level delay.
func PositionEvidenceReason(m Match, trip schedule.Trip) string {
	switch m.Train.VehicleStatus {
	case "", "STOPPED_AT", "IN_TRANSIT_TO", "INCOMING_AT":
	default:
		return "unsupported_vehicle_status"
	}
	updates := make([]*realtime.StopPrediction, len(trip.Stops))
	for _, stop := range m.Stops {
		if stop.Outcome != Matched {
			if stop.Reason != "" {
				return stop.Reason
			}
			return "invalid_stop_evidence"
		}
		if stop.ScheduledIndex < 0 || stop.ScheduledIndex >= len(updates) || stop.UpdateIndex < 0 || stop.UpdateIndex >= len(m.Train.Stops) {
			return "invalid_stop_evidence"
		}
		updates[stop.ScheduledIndex] = &m.Train.Stops[stop.UpdateIndex]
	}
	if m.VehicleStop != nil && m.VehicleStop.Outcome != Matched {
		return m.VehicleStop.Reason
	}
	var previous time.Time
	for i, update := range updates {
		if update == nil {
			continue
		}
		if update.Relationship != "" && update.Relationship != realtime.StopScheduled && update.Relationship != realtime.StopSkipped && update.Relationship != realtime.StopNoData {
			return "unsupported_stop_relationship"
		}
		if m.Train.VehicleStatus == "STOPPED_AT" && m.VehicleStop != nil && m.VehicleStop.ScheduledIndex == i && (update.Relationship == realtime.StopSkipped || update.Relationship == realtime.StopNoData) {
			return "vehicle_stop_relationship_conflict"
		}
		if update.Relationship == realtime.StopNoData {
			previous = time.Time{}
			continue
		}
		if update.Relationship == realtime.StopSkipped {
			continue
		}
		for j, event := range []*realtime.StopEvent{update.Arrival, update.Departure} {
			scheduled := trip.Stops[i].Arrival
			if j == 1 {
				scheduled = trip.Stops[i].Departure
			}
			var scheduledAt time.Time
			if scheduled.Present {
				scheduledAt = m.Instance.ServiceDate.Instant(scheduled.Seconds)
			}
			at, _, valid := realtime.EventTime(event, m.Train.TripDelaySeconds, scheduledAt)
			if !valid {
				return "invalid_event_time"
			}
			if at.IsZero() {
				continue
			}
			if !previous.IsZero() && at.Before(previous) {
				return "conflicting_event_times"
			}
			previous = at
		}
	}
	return ""
}

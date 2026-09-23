package projection

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"hol-up-prototype/internal/realtime"
	"hol-up-prototype/internal/schedule"
)

func TestConflictingVehicleUsesIndependentPredictions(t *testing.T) {
	v, now := fixture(t, nil)
	for _, status := range []string{"STOPPED_AT", "IN_TRANSIT_TO", "INCOMING_AT"} {
		for _, age := range []time.Duration{0, 2 * time.Minute} {
			t.Run(status+"/"+age.String(), func(t *testing.T) {
				r := record(now)
				r.StartTime = "12:01:00" // Existing warnings must remain sorted alongside the new one.
				r.VehicleMatch, r.VehicleStatus = realtime.VehicleMatched, status
				r.VehicleStopID, r.VehicleStopSequence = "start", ptr(uint32(2))
				r.VehicleTimestamp, r.VehicleTimestampPresent = now.Add(-age), true
				b := bundle(v, now, r)
				before := b.Matches()
				p := only(t, b, now)
				clean := only(t, bundle(v, now, record(now)), now)
				if p.Latitude == nil || *p.Latitude != *clean.Latitude || *p.Longitude != *clean.Longitude || p.MovementState != realtime.MovementStateInferredMoving || p.ObservationBasis != "trip_update" || p.InterpolationMethod != "realtime_events" {
					t.Fatalf("position did not use independent predictions: %+v", p)
				}
				if !slices.Equal(p.ReconciliationWarnings, []string{"start_time_conflict", "vehicle_stop_sequence_conflict_ignored"}) {
					t.Fatalf("warnings: %v", p.ReconciliationWarnings)
				}
				if !reflect.DeepEqual(before, b.Matches()) || !reflect.DeepEqual(r, b.Matches()[0].Train) || p.VehicleTimestamp != r.VehicleTimestamp || p.VehicleMatch != r.VehicleMatch {
					t.Fatal("projection changed raw evidence or provenance")
				}
				later := now.Add(61 * time.Second)
				stale := only(t, b, later)
				permission := b.SimulationDecision(before[0].Instance, later)
				if stale.Latitude != nil || stale.NullReason != "stop_id_sequence_conflict" || permission.Allowed || permission.Reason != "stop_id_sequence_conflict" {
					t.Fatalf("conflict authorized stale simulation: %+v / %+v", stale, permission)
				}
			})
		}
	}
}

func TestVehicleExclusionRetainsOtherFailures(t *testing.T) {
	for _, tt := range []struct {
		name, reason string
		edit         func(*realtime.Train, *schedule.Data)
	}{
		{"missing timing", "insufficient_timing", func(r *realtime.Train, _ *schedule.Data) { r.Stops = nil }},
		{"unknown ID", "stop_id_sequence_conflict", func(r *realtime.Train, _ *schedule.Data) { r.VehicleStopID = "absent" }},
		{"unknown sequence", "unknown_stop", func(r *realtime.Train, _ *schedule.Data) { r.VehicleStopSequence = ptr(uint32(99)) }},
		{"repeated ID", "stop_id_sequence_conflict", func(_ *realtime.Train, d *schedule.Data) {
			last := d.Trips[0].Stops[0]
			last.Sequence, last.Arrival.Seconds, last.Departure.Seconds = 3, 44400, 44400
			d.Trips[0].Stops = append(d.Trips[0].Stops, last)
		}},
		{"invalid time", "invalid_event_time", func(r *realtime.Train, _ *schedule.Data) { r.Stops[0].Departure.Time = time.Unix(0, 0) }},
		{"reversed times", "conflicting_event_times", func(r *realtime.Train, _ *schedule.Data) {
			r.Stops[0].Departure.Time = r.Stops[1].Arrival.Time.Add(time.Minute)
		}},
		{"path change", "unknown_stop", func(r *realtime.Train, _ *schedule.Data) {
			r.Stops = append(r.Stops, realtime.StopPrediction{StopID: "absent"})
		}},
		{"canceled", "canceled", func(r *realtime.Train, _ *schedule.Data) { r.ScheduleRelationship = "CANCELED" }},
		{"wrong route", "route_conflict", func(r *realtime.Train, _ *schedule.Data) { r.RouteID = "C" }},
		{"unsupported status", "unsupported_vehicle_status", func(r *realtime.Train, _ *schedule.Data) { r.VehicleStatus = "UNKNOWN" }},
		{"missing shape", "shape_missing", func(_ *realtime.Train, d *schedule.Data) { d.Trips[0].ShapeID = "" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, now := fixture(t, nil)
			r := record(now)
			r.VehicleMatch, r.VehicleStatus = realtime.VehicleMatched, "STOPPED_AT"
			r.VehicleStopID, r.VehicleStopSequence, r.VehicleTimestamp = "start", ptr(uint32(2)), now
			v, _ := fixture(t, func(d *schedule.Data) { tt.edit(&r, d) })
			p := only(t, bundle(v, now, r), now)
			if p.Latitude != nil || p.NullReason != tt.reason {
				t.Fatalf("got %+v, want %s", p, tt.reason)
			}
		})
	}
}

func TestUpcomingDeparturesAndBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name, reason, method string
		edit                 func(*realtime.Train, *schedule.Data, time.Time)
	}{
		{"future first stop", "not_started", "", func(*realtime.Train, *schedule.Data, time.Time) {}},
		{"departure only", "not_started", "", func(r *realtime.Train, _ *schedule.Data, _ time.Time) { r.Stops[0].Arrival = nil }},
		{"without geometry", "not_started", "", func(_ *realtime.Train, d *schedule.Data, _ time.Time) { d.Trips[0].ShapeID = "" }},
		{"future vehicle timestamp without status", "not_started", "", func(r *realtime.Train, _ *schedule.Data, now time.Time) {
			r.VehicleMatch, r.VehicleTimestamp, r.VehicleTimestampPresent = realtime.VehicleMatched, now.Add(time.Minute), true
		}},
		{"current dwell", "", "realtime_dwell", func(r *realtime.Train, _ *schedule.Data, now time.Time) { r.Stops[0].Arrival.Time = now }},
		{"reported stop", "", "vehicle_stop", func(r *realtime.Train, _ *schedule.Data, now time.Time) {
			r.VehicleMatch, r.VehicleStatus, r.VehicleStopID, r.VehicleTimestamp = realtime.VehicleMatched, "STOPPED_AT", "start", now
		}},
		{"skipped", "insufficient_timing", "", func(r *realtime.Train, _ *schedule.Data, _ time.Time) { r.Stops[0].Relationship = realtime.StopSkipped }},
		{"no data", "insufficient_timing", "", func(r *realtime.Train, _ *schedule.Data, _ time.Time) { r.Stops[0].Relationship = realtime.StopNoData }},
		{"missing timing", "insufficient_timing", "", func(r *realtime.Train, _ *schedule.Data, _ time.Time) { r.Stops = nil }},
		{"later occurrence", "insufficient_timing", "", func(r *realtime.Train, _ *schedule.Data, _ time.Time) { r.Stops = r.Stops[1:] }},
		{"canceled", "canceled", "", func(r *realtime.Train, _ *schedule.Data, _ time.Time) { r.ScheduleRelationship = "CANCELED" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, now := fixture(t, nil)
			r := record(now)
			departure := now.Add(30 * time.Second)
			r.Stops[0].Arrival = &realtime.StopEvent{Time: departure}
			r.Stops[0].Departure.Time = departure
			r.Stops[1].Arrival.Time = now.Add(20 * time.Minute)
			v, _ := fixture(t, func(d *schedule.Data) { tt.edit(&r, d, now) })
			b := bundle(v, now, r)
			p := only(t, b, now)
			if p.NullReason != tt.reason || p.InterpolationMethod != tt.method || (p.Latitude != nil) != (tt.reason == "") {
				t.Fatalf("got %+v, want %s / %s", p, tt.reason, tt.method)
			}
			if (tt.reason == "not_started") != !p.PredictedDepartureAt.IsZero() || (tt.reason == "not_started" && (p.PredictedDepartureAt != departure || p.MovementState != realtime.MovementStateUnknown)) {
				t.Fatalf("upcoming contract: %+v", p)
			}
			if tt.name == "future first stop" {
				for _, offset := range []time.Duration{30 * time.Second, 31 * time.Second} {
					next := only(t, b, now.Add(offset))
					if next.Latitude == nil || !next.PredictedDepartureAt.IsZero() || next.TripInstanceID != p.TripInstanceID {
						t.Fatalf("departure transition: %+v", next)
					}
				}
			}
		})
	}
}

package projection

import (
	"math"
	"strings"
	"testing"
	"time"

	"hol-up-prototype/internal/realtime"
	"hol-up-prototype/internal/reconcile"
	"hol-up-prototype/internal/schedule"
)

func ptr[T any](v T) *T { return &v }

func fixture(t *testing.T, modify func(*schedule.Data)) (*schedule.Version, time.Time) {
	t.Helper()
	date := schedule.Date("20260902")
	at := date.Instant(12*3600 + 300)
	clock := func(s int32) schedule.OptionalTime { return schedule.OptionalTime{Seconds: s, Present: true} }
	d := schedule.Data{
		Agencies: []schedule.Agency{{ID: "mta", Name: "MTA", URL: "https://example.test", Timezone: schedule.Timezone}},
		Routes:   []schedule.Route{{ID: "A", AgencyID: "mta", ShortName: "A"}},
		Stops:    []schedule.Stop{{ID: "start", Latitude: 0, Longitude: 0, HasCoordinates: true}, {ID: "bend", Latitude: 0, Longitude: .01, HasCoordinates: true}, {ID: "end", Latitude: .01, Longitude: .01, HasCoordinates: true}},
		Trips: []schedule.Trip{{ID: "run", RouteID: "A", ServiceID: "daily", Direction: 0, ShapeID: "bent", Stops: []schedule.StopTime{
			{StopID: "start", Sequence: 1, Arrival: clock(43200), Departure: clock(43200)},
			{StopID: "end", Sequence: 2, Arrival: clock(43800), Departure: clock(43800)},
		}}},
		Calendars: []schedule.Calendar{{ServiceID: "daily", Start: "20260901", End: "20261130", Days: [7]bool{true, true, true, true, true, true, true}}},
		Shapes:    []schedule.Shape{{ID: "bent", Points: []schedule.ShapePoint{{Sequence: 0, Latitude: 0, Longitude: 0}, {Sequence: 1, Latitude: 0, Longitude: .01}, {Sequence: 2, Latitude: .01, Longitude: .01}}}},
	}
	if modify != nil {
		modify(&d)
	}
	dataset, err := schedule.NewDataset(d)
	if err != nil {
		t.Fatal(err)
	}
	v, err := schedule.NewVersion(dataset, schedule.Metadata{Source: schedule.Regular, ParserSchema: "2", ArchiveSHA256: strings.Repeat("a", 64), ValidatedAt: at.Add(-time.Hour), PublishedAt: at.Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	return v, at
}

func record(now time.Time) realtime.Train {
	return realtime.Train{TripID: "run", RouteID: "A", ServiceDate: "20260902", EntityID: "update", ScheduleRelationship: "SCHEDULED", TripUpdateTimestamp: now, TripUpdateTimestampPresent: true,
		Stops: []realtime.StopPrediction{{Sequence: ptr(uint32(1)), Departure: &realtime.StopEvent{Time: now.Add(-5 * time.Minute)}}, {Sequence: ptr(uint32(2)), Arrival: &realtime.StopEvent{Time: now.Add(5 * time.Minute)}}}}
}

func bundle(v *schedule.Version, now time.Time, records ...realtime.Train) *reconcile.Bundle {
	return reconcile.Reconcile(v, []reconcile.Source{{Name: "ace", Routes: []string{"A"}, Snapshot: realtime.Snapshot{SourceTimestamp: now, ReceivedAt: now, Complete: true, NYCTVersion: "1.0", Trains: records}}}, now, time.Minute, 24*time.Hour)
}

func only(t *testing.T, b *reconcile.Bundle, now time.Time) Position {
	t.Helper()
	got := Positions(b, "A", now)
	if len(got) != 1 {
		t.Fatalf("positions count=%d: %+v", len(got), got)
	}
	return got[0]
}

func TestBentPolylinePollingAndStableSimulationTransition(t *testing.T) {
	v, now := fixture(t, nil)
	b := bundle(v, now, record(now))
	first := only(t, b, now)
	if first.Latitude == nil || math.Abs(*first.Latitude) > 1e-8 || math.Abs(*first.Longitude-.01) > 1e-8 || first.InterpolationMethod != "realtime_events" {
		t.Fatalf("bent midpoint=%+v", first)
	}
	second := only(t, b, now.Add(30*time.Second))
	if second.Latitude == nil || *second.Latitude <= *first.Latitude || second.TripInstanceID != first.TripInstanceID {
		t.Fatalf("poll did not advance with stable identity: %+v", second)
	}
	stale := only(t, b, now.Add(61*time.Second))
	if stale.Latitude == nil || stale.PositionBasis != realtime.PositionBasisScheduleFallback || stale.TripInstanceID != first.TripInstanceID || stale.SourceRecordID != first.SourceRecordID || stale.Freshness != realtime.FreshnessStale || stale.MovementState != realtime.MovementStateUnknown {
		t.Fatalf("stale transition=%+v", stale)
	}
	freshTime := now.Add(70 * time.Second)
	r := record(now)
	r.TripUpdateTimestamp = freshTime
	recovered := only(t, bundle(v, freshTime, r), freshTime)
	if recovered.Latitude == nil || recovered.PositionBasis != realtime.PositionBasisRealtimePrediction || recovered.TripInstanceID != first.TripInstanceID {
		t.Fatalf("recovery=%+v", recovered)
	}
	outside := only(t, b, now.Add(6*time.Minute))
	if outside.Latitude != nil || outside.NullReason != "outside_scheduled_interval" {
		t.Fatalf("extrapolated late train: %+v", outside)
	}
	expired := only(t, b, now.Add(25*time.Hour))
	if expired.Latitude != nil || expired.NullReason != "schedule_ineligible" {
		t.Fatalf("expired schedule=%+v", expired)
	}
}

func TestRealtimeTimingAndNullOutcomes(t *testing.T) {
	v, now := fixture(t, nil)
	tests := []struct {
		name           string
		change         func(*realtime.Train)
		method, reason string
	}{
		{"sequence only", func(*realtime.Train) {}, "realtime_events", ""},
		{"next prediction", func(r *realtime.Train) { r.Stops = r.Stops[1:] }, "next_prediction_schedule_duration", ""},
		{"delay only", func(r *realtime.Train) {
			for i := range r.Stops {
				r.Stops[i].Arrival = nil
				r.Stops[i].Departure = nil
			}
			r.TripDelaySeconds = ptr(int32(0))
		}, "delay_derived", ""},
		{"absolute precedence", func(r *realtime.Train) {
			r.TripDelaySeconds = ptr(int32(900))
			r.Stops[0].Arrival = &realtime.StopEvent{Time: now.Add(-5 * time.Minute)}
			r.Stops[1].Departure = &realtime.StopEvent{Time: now.Add(5 * time.Minute)}
		}, "realtime_events", ""},
		{"missing observation", func(r *realtime.Train) { r.TripUpdateTimestamp = time.Time{}; r.TripUpdateTimestampPresent = false }, "realtime_events", ""},
		{"malformed observation", func(r *realtime.Train) { r.TripUpdateTimestamp = time.Time{} }, "", "invalid_trip_update_timestamp"},
		{"zero observation", func(r *realtime.Train) { r.TripUpdateTimestamp = time.Unix(0, 0) }, "", "invalid_trip_update_timestamp"},
		{"future observation", func(r *realtime.Train) { r.TripUpdateTimestamp = now.Add(time.Second) }, "", "invalid_trip_update_timestamp"},
		{"no timing", func(r *realtime.Train) { r.Stops = nil }, "", "insufficient_timing"},
		{"no data", func(r *realtime.Train) {
			r.Stops[1].Relationship = realtime.StopNoData
			r.TripDelaySeconds = ptr(int32(0))
		}, "", "insufficient_timing"},
		{"skipped endpoint", func(r *realtime.Train) { r.Stops[1].Relationship = realtime.StopSkipped }, "", "insufficient_timing"},
		{"unsupported stop relationship", func(r *realtime.Train) { r.Stops[1].Relationship = "UNKNOWN" }, "", "unsupported_stop_relationship"},
		{"canceled", func(r *realtime.Train) { r.ScheduleRelationship = "CANCELED" }, "", "canceled"},
		{"added", func(r *realtime.Train) { r.ScheduleRelationship = "ADDED" }, "", "unsupported_schedule_relationship"},
		{"invalid stop", func(r *realtime.Train) { r.Stops[1].StopID = "wrong" }, "", "stop_id_sequence_conflict"},
		{"invalid events", func(r *realtime.Train) { r.Stops[1].Arrival.Time = time.Unix(0, 0) }, "", "invalid_event_time"},
		{"reverse events", func(r *realtime.Train) { r.Stops[0].Departure.Time = now.Add(6 * time.Minute) }, "", "conflicting_event_times"},
		{"no extrapolation", func(r *realtime.Train) { r.Stops = r.Stops[1:]; r.Stops[0].Arrival.Time = now.Add(20 * time.Minute) }, "", "insufficient_timing"},
		{"predicted dwell", func(r *realtime.Train) {
			r.Stops = []realtime.StopPrediction{{Sequence: ptr(uint32(1)), Arrival: &realtime.StopEvent{Time: now.Add(-time.Second)}, Departure: &realtime.StopEvent{Time: now.Add(time.Second)}}}
		}, "realtime_dwell", ""},
		{"reported dwell", func(r *realtime.Train) {
			r.VehicleMatch = realtime.VehicleMatched
			r.VehicleTimestamp = now
			r.VehicleTimestampPresent = true
			r.VehicleStatus = "STOPPED_AT"
			r.VehicleStopSequence = ptr(uint32(1))
			r.Stops = nil
		}, "vehicle_stop", ""},
		{"stale vehicle", func(r *realtime.Train) {
			r.VehicleMatch = realtime.VehicleMatched
			r.VehicleTimestamp = now.Add(-2 * time.Minute)
			r.VehicleStatus = "STOPPED_AT"
			r.VehicleStopSequence = ptr(uint32(1))
			r.Stops = nil
		}, "", "insufficient_timing"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := record(now)
			test.change(&r)
			p := only(t, bundle(v, now, r), now)
			if p.InterpolationMethod != test.method || p.NullReason != test.reason || (p.Latitude == nil) != (test.reason != "") {
				t.Fatalf("position=%+v", p)
			}
			if test.name == "reported dwell" && p.MovementState != realtime.MovementStateReportedStopped {
				t.Fatalf("movement=%s", p.MovementState)
			}
			if test.name == "predicted dwell" && p.MovementState == realtime.MovementStateReportedStopped {
				t.Fatal("prediction claimed vehicle observation")
			}
			if test.name == "missing observation" && p.ObservationBasis != "feed_header" {
				t.Fatal("missing header label")
			}
		})
	}
}

func TestCompleteRecordSelectionAndMissingGeometry(t *testing.T) {
	v, now := fixture(t, nil)
	r := record(now)
	other := r.Clone()
	other.EntityID = "duplicate"
	b := reconcile.Reconcile(v, []reconcile.Source{{Name: "ace", Routes: []string{"A"}, Snapshot: realtime.Snapshot{SourceTimestamp: now, ReceivedAt: now, Complete: true, NYCTVersion: "1.0", Trains: []realtime.Train{r, other, {TripID: "unknown", RouteID: "A", EntityID: "unknown"}}, UnidentifiedTrips: []realtime.Train{{RouteID: "A", EntityID: "unidentified"}}}}}, now, time.Minute, 24*time.Hour)
	got := Positions(b, "A", now)
	if len(got) != 4 {
		t.Fatalf("lost input records: %+v", got)
	}
	seen := map[string]bool{}
	for _, p := range got {
		if p.Latitude != nil || p.TripInstanceID != "" || p.NullReason == "" || seen[p.SourceRecordID] {
			t.Fatalf("invalid null identity: %+v", p)
		}
		seen[p.SourceRecordID] = true
	}
	missing := only(t, bundle(nil, now, r), now)
	if missing.Latitude != nil || missing.NullReason != "schedule_missing" {
		t.Fatalf("missing schedule=%+v", missing)
	}
	v, now = fixture(t, func(d *schedule.Data) { d.Shapes = nil })
	missing = only(t, bundle(v, now, r), now)
	if missing.Latitude != nil || missing.PositionBasis != realtime.PositionBasisRealtimePrediction || missing.NullReason != "shape_missing" {
		t.Fatalf("missing shape=%+v", missing)
	}
}

func TestAbsentSimulationAndCalendarDST(t *testing.T) {
	for _, test := range []struct {
		date  schedule.Date
		start int32
	}{{"20260902", 25 * 3600}, {"20261101", 3600}, {"20260308", 0}} {
		t.Run(string(test.date), func(t *testing.T) {
			v, _ := fixture(t, func(d *schedule.Data) {
				d.Calendars[0].Start = "20260101"
				d.Trips[0].Stops[0].Arrival.Seconds = test.start
				d.Trips[0].Stops[0].Departure.Seconds = test.start
				d.Trips[0].Stops[1].Arrival.Seconds = test.start + 600
				d.Trips[0].Stops[1].Departure.Seconds = test.start + 600
			})
			now := test.date.Instant(test.start + 300)
			meta := v.Metadata()
			meta.ValidatedAt = now.Add(-time.Hour)
			meta.PublishedAt = meta.ValidatedAt
			v, err := schedule.NewVersion(v.Dataset(), meta)
			if err != nil {
				t.Fatal(err)
			}
			p := only(t, bundle(v, now), now)
			if p.Latitude == nil || p.ServiceDate != string(test.date) || p.PositionBasis != realtime.PositionBasisScheduleFallback || p.MovementState != realtime.MovementStateUnknown || p.SourceRecordID != "" {
				t.Fatalf("simulation=%+v", p)
			}
			p = only(t, bundle(v, now), now.Add(61*time.Second))
			if p.Latitude == nil || p.Freshness != realtime.FreshnessStale {
				t.Fatalf("absent stale=%+v", p)
			}
		})
	}
}

func TestFreshVehicleConstrainsIncomingOccurrence(t *testing.T) {
	v, now := fixture(t, func(d *schedule.Data) {
		end := d.Trips[0].Stops[1]
		end.Sequence, end.Arrival.Seconds, end.Departure.Seconds = 3, 44400, 44400
		d.Trips[0].Stops[1].StopID = "bend"
		d.Trips[0].Stops = append(d.Trips[0].Stops, end)
	})
	for _, sequence := range []uint32{2, 3} {
		r := record(now)
		r.VehicleMatch, r.VehicleStatus, r.VehicleTimestamp, r.VehicleStopSequence = realtime.VehicleMatched, "IN_TRANSIT_TO", now, &sequence
		p := only(t, bundle(v, now, r), now)
		if sequence == 2 && p.Latitude == nil {
			t.Fatalf("compatible vehicle interval rejected: %+v", p)
		}
		if sequence == 3 && (p.Latitude != nil || p.NullReason != "vehicle_interval_conflict") {
			t.Fatalf("contradictory vehicle interval accepted: %+v", p)
		}
	}
}

func TestSkippedInteriorAndNoDataBarrier(t *testing.T) {
	v, now := fixture(t, func(d *schedule.Data) {
		last := d.Trips[0].Stops[1]
		last.Sequence = 3
		middle := last
		middle.StopID = "bend"
		middle.Sequence = 2
		middle.Arrival.Seconds = 43500
		middle.Departure.Seconds = 43500
		d.Trips[0].Stops = []schedule.StopTime{d.Trips[0].Stops[0], middle, last}
	})
	for _, relationship := range []realtime.StopRelationship{realtime.StopSkipped, realtime.StopNoData} {
		r := record(now)
		r.Stops[1].Sequence = ptr(uint32(3))
		r.Stops = []realtime.StopPrediction{r.Stops[0], {Sequence: ptr(uint32(2)), Relationship: relationship}, r.Stops[1]}
		p := only(t, bundle(v, now, r), now)
		if relationship == realtime.StopSkipped && (p.Latitude == nil || math.Abs(*p.Longitude-.01) > 1e-8) {
			t.Fatalf("skipped interior changed polyline: %+v", p)
		}
		if relationship == realtime.StopNoData && (p.Latitude != nil || p.NullReason != "insufficient_timing") {
			t.Fatalf("crossed NO_DATA barrier: %+v", p)
		}
	}
}

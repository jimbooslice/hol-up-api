package reconcile

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"hol-up-prototype/internal/realtime"
	"hol-up-prototype/internal/schedule"
)

func TestSimulationStalePresentAndFreshRecovery(t *testing.T) {
	tr := realtime.Train{TripID: staticID, RouteID: "A", ServiceDate: string(date), TripUpdateTimestamp: now, TripUpdateTimestampPresent: true}
	b := run(t, source(tr))
	instance := b.Matches()[0].Instance
	if got := b.SimulationDecision(instance, now.Add(30*time.Second)); got.Allowed || got.Reason != "fresh_realtime_counterpart" {
		t.Fatal(got)
	}
	if got := b.SimulationDecision(instance, now.Add(2*time.Minute)); !got.Allowed || got.Reason != "stale_realtime_counterpart" {
		t.Fatal(got)
	}
	// The same retained window vetoes an absent run after departure and expiry.
	absent := run(t, source())
	if got := absent.SimulationDecision(instance, now.Add(2*time.Minute)); got.Allowed || got.Reason != "absent_from_replacement_period" {
		t.Fatal(got)
	}
	if !absent.Fallback(instance, now.Add(2*time.Minute)).Eligible {
		t.Fatal("legacy fallback contract changed")
	}
	updatedAt := now.Add(3 * time.Minute)
	tr.TripID, tr.TripUpdateTimestamp = "054050_A..N74R", updatedAt
	s := source(tr)
	s.Snapshot.SourceTimestamp, s.Snapshot.ReceivedAt = updatedAt, updatedAt
	fresh := Reconcile(b.Schedule(), []Source{s}, updatedAt, time.Minute, 24*time.Hour)
	if fresh.Matches()[0].Instance.ID() != instance.ID() {
		t.Fatal("recovery changed scheduled run identity")
	}
	if got := fresh.SimulationDecision(instance, updatedAt); got.Allowed || got.Reason != "fresh_realtime_counterpart" {
		t.Fatal(got)
	}
}

func TestSimulationPermissionVetoes(t *testing.T) {
	instance := Instance{staticID, date}
	matched := realtime.Train{TripID: staticID, RouteID: "A", ServiceDate: string(date)}
	tests := []struct {
		name    string
		edit    func(*Source)
		allowed bool
		reason  string
	}{
		{"absent without replacement", func(s *Source) { s.Snapshot.Trains = nil }, true, "no_realtime_counterpart"},
		{"stale feed with missing optional timestamp", func(*Source) {}, true, "stale_realtime_counterpart"},
		{"stale observation fresh feed", func(s *Source) {
			s.Snapshot.SourceTimestamp, s.Snapshot.ReceivedAt = now.Add(2*time.Minute), now.Add(2*time.Minute)
			s.Snapshot.Trains[0].TripUpdateTimestamp = now
		}, true, "stale_realtime_counterpart"},
		{"fresh observation", func(s *Source) {
			s.Snapshot.SourceTimestamp, s.Snapshot.ReceivedAt = now.Add(2*time.Minute), now.Add(2*time.Minute)
		}, false, "fresh_realtime_counterpart"},
		{"cancellation outside windows", func(s *Source) { s.Snapshot.Trains[0].ScheduleRelationship = "CANCELED" }, false, "canceled"},
		{"deletion outside windows", func(s *Source) { s.Snapshot.Trains[0].ScheduleRelationship = "DELETED" }, false, "canceled"},
		{"unsupported relationship", func(s *Source) { s.Snapshot.Trains[0].ScheduleRelationship = "ADDED" }, false, "unsupported_trip_relationship"},
		{"duplicate ambiguous", func(s *Source) { s.Snapshot.Trains = append(s.Snapshot.Trains, matched) }, false, "uncertain_counterpart_or_metadata"},
		{"invalid route", func(s *Source) { s.Snapshot.Trains[0].RouteID = "C" }, false, "uncertain_counterpart_or_metadata"},
		{"unidentified route uncertainty", func(s *Source) { s.Snapshot.UnidentifiedTrips = []realtime.Train{{RouteID: "A"}} }, false, "uncertain_counterpart_or_metadata"},
		{"unidentified source uncertainty", func(s *Source) { s.Snapshot.UnidentifiedTrips = []realtime.Train{{}} }, false, "uncertain_counterpart_or_metadata"},
		{"sequence only", func(s *Source) { s.Snapshot.Trains[0].Stops = []realtime.StopPrediction{{Sequence: ptr(uint32(3))}} }, true, "stale_realtime_counterpart"},
		{"invalid stop", func(s *Source) { s.Snapshot.Trains[0].Stops = []realtime.StopPrediction{{StopID: "unknown"}} }, false, "unknown_stop"},
		{"stop order conflict", func(s *Source) {
			s.Snapshot.Trains[0].Stops = []realtime.StopPrediction{{Sequence: ptr(uint32(3))}, {Sequence: ptr(uint32(1))}}
		}, false, "conflicting_stop_order"},
		{"unknown stop relationship", func(s *Source) {
			s.Snapshot.Trains[0].Stops = []realtime.StopPrediction{{Sequence: ptr(uint32(1)), Relationship: "UNKNOWN"}}
		}, false, "unsupported_stop_relationship"},
		{"epoch event", func(s *Source) {
			s.Snapshot.Trains[0].Stops = []realtime.StopPrediction{{Sequence: ptr(uint32(1)), Departure: &realtime.StopEvent{Time: time.Unix(0, 0)}}}
		}, false, "invalid_event_time"},
		{"overflow event", func(s *Source) {
			s.Snapshot.Trains[0].Stops = []realtime.StopPrediction{{Sequence: ptr(uint32(1)), Departure: &realtime.StopEvent{Time: time.Unix(1<<63-1, 0)}}}
		}, false, "invalid_event_time"},
		{"malformed event cannot use delay", func(s *Source) {
			s.Snapshot.Trains[0].Stops = []realtime.StopPrediction{{Sequence: ptr(uint32(1)), Departure: &realtime.StopEvent{TimePresent: true, DelaySeconds: ptr(int32(0))}}}
		}, false, "invalid_event_time"},
		{"reversed dwell events", func(s *Source) {
			s.Snapshot.Trains[0].Stops = []realtime.StopPrediction{{Sequence: ptr(uint32(1)), Arrival: &realtime.StopEvent{Time: now.Add(time.Minute)}, Departure: &realtime.StopEvent{Time: now}}}
		}, false, "conflicting_event_times"},
		{"reversed segment events", func(s *Source) {
			s.Snapshot.Trains[0].Stops = []realtime.StopPrediction{{Sequence: ptr(uint32(1)), Departure: &realtime.StopEvent{Time: now.Add(time.Hour)}}, {Sequence: ptr(uint32(3)), Arrival: &realtime.StopEvent{Time: now}}}
		}, false, "conflicting_event_times"},
		{"reversed delay derived events", func(s *Source) {
			s.Snapshot.Trains[0].Stops = []realtime.StopPrediction{{Sequence: ptr(uint32(1)), Departure: &realtime.StopEvent{DelaySeconds: ptr(int32(8000))}}, {Sequence: ptr(uint32(3)), Arrival: &realtime.StopEvent{DelaySeconds: ptr(int32(0))}}}
		}, false, "conflicting_event_times"},
		{"absolute events supersede delay", func(s *Source) {
			s.Snapshot.Trains[0].Stops = []realtime.StopPrediction{{Sequence: ptr(uint32(1)), Departure: &realtime.StopEvent{Time: now, DelaySeconds: ptr(int32(8000))}}, {Sequence: ptr(uint32(3)), Arrival: &realtime.StopEvent{Time: now.Add(time.Hour)}}}
		}, true, "stale_realtime_counterpart"},
		{"no data blocks inherited delay", func(s *Source) {
			s.Snapshot.Trains[0].TripDelaySeconds = ptr(int32(8000))
			s.Snapshot.Trains[0].Stops = []realtime.StopPrediction{{Sequence: ptr(uint32(1)), Relationship: realtime.StopNoData}, {Sequence: ptr(uint32(3)), Arrival: &realtime.StopEvent{Time: now.Add(time.Hour)}}}
		}, true, "stale_realtime_counterpart"},
		{"future observation", func(s *Source) { s.Snapshot.Trains[0].TripUpdateTimestamp = now.Add(time.Hour) }, false, "invalid_observation_timestamp"},
		{"malformed observation", func(s *Source) { s.Snapshot.Trains[0].TripUpdateTimestampPresent = true }, false, "invalid_observation_timestamp"},
		{"epoch observation", func(s *Source) { s.Snapshot.Trains[0].TripUpdateTimestamp = time.Unix(0, 0) }, false, "invalid_observation_timestamp"},
		{"future vehicle observation", func(s *Source) { s.Snapshot.Trains[0].VehicleTimestamp = now.Add(time.Hour) }, false, "invalid_vehicle_timestamp"},
		{"unknown vehicle status", func(s *Source) { s.Snapshot.Trains[0].VehicleStatus = "UNKNOWN" }, false, "unsupported_vehicle_status"},
		{"vehicle stopped at skipped occurrence", func(s *Source) {
			s.Snapshot.Trains[0].VehicleMatch = realtime.VehicleMatched
			s.Snapshot.Trains[0].VehicleStatus = "STOPPED_AT"
			s.Snapshot.Trains[0].VehicleStopSequence = ptr(uint32(1))
			s.Snapshot.Trains[0].Stops = []realtime.StopPrediction{{Sequence: ptr(uint32(1)), Relationship: realtime.StopSkipped}}
		}, false, "vehicle_stop_relationship_conflict"},
		{"vehicle stopped at no-data occurrence", func(s *Source) {
			s.Snapshot.Trains[0].VehicleMatch = realtime.VehicleMatched
			s.Snapshot.Trains[0].VehicleStatus = "STOPPED_AT"
			s.Snapshot.Trains[0].VehicleStopSequence = ptr(uint32(1))
			s.Snapshot.Trains[0].Stops = []realtime.StopPrediction{{Sequence: ptr(uint32(1)), Relationship: realtime.StopNoData}}
		}, false, "vehicle_stop_relationship_conflict"},
		{"vehicle stop conflict", func(s *Source) {
			s.Snapshot.Trains[0].VehicleMatch = realtime.VehicleMatched
			s.Snapshot.Trains[0].VehicleStopSequence = ptr(uint32(1))
			s.Snapshot.Trains[0].VehicleStopID = "A02N"
		}, false, "stop_id_sequence_conflict"},
		{"no loaded snapshot", func(s *Source) { s.Snapshot = realtime.Snapshot{} }, false, "source_never_loaded"},
		{"missing receipt", func(s *Source) { s.Snapshot.ReceivedAt = time.Time{} }, false, "source_never_loaded"},
		{"incomplete source", func(s *Source) { s.Snapshot.Complete = false }, false, "source_metadata_unusable"},
		{"missing version", func(s *Source) { s.Snapshot.NYCTVersion = "" }, false, "source_metadata_unusable"},
		{"unsupported version", func(s *Source) { s.Snapshot.NYCTVersion = "2.0" }, false, "source_metadata_unusable"},
		{"future header", func(s *Source) { s.Snapshot.SourceTimestamp = now.Add(time.Hour) }, false, "source_metadata_unusable"},
		{"missing owner", func(s *Source) { s.Routes = []string{"C"} }, false, "missing_route_owner"},
		{"uncertain replacement", func(s *Source) { s.Snapshot.ReplacementPeriods = []realtime.ReplacementPeriod{{RouteID: "A"}} }, false, "uncertain_counterpart_or_metadata"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := source(matched)
			s.Snapshot.ReplacementPeriods = nil
			tt.edit(&s)
			got := run(t, s).SimulationDecision(instance, now.Add(2*time.Minute))
			if got.Allowed != tt.allowed || got.Reason != tt.reason {
				t.Fatal(got)
			}
		})
	}
}

func TestSimulationReplacementBoundariesAndScheduleInterval(t *testing.T) {
	instance := Instance{staticID, date}
	departure := date.Instant(32430)
	for _, tt := range []struct {
		name       string
		start, end *uint64
		allowed    bool
	}{
		{"start inclusive", ptr(uint64(departure.Unix())), nil, false},
		{"before start", ptr(uint64(departure.Unix() + 1)), nil, true},
		{"end exclusive", nil, ptr(uint64(departure.Unix())), true},
		{"before end", nil, ptr(uint64(departure.Unix() + 1)), false},
		{"explicit zero", ptr(uint64(0)), nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := source()
			s.Snapshot.ReplacementPeriods = []realtime.ReplacementPeriod{{RouteID: "A", Start: tt.start, End: tt.end}}
			b := run(t, s)
			for _, at := range []time.Time{departure, now.Add(time.Minute), now.Add(2 * time.Minute)} {
				if got := b.SimulationDecision(instance, at); got.Allowed != tt.allowed {
					t.Fatalf("%v: %+v", at, got)
				}
			}
		})
	}
	s := source()
	s.Snapshot.ReplacementPeriods = nil
	b := run(t, s)
	arrival := date.Instant(32430 + 7200)
	for _, tt := range []struct {
		at      time.Time
		allowed bool
	}{{departure.Add(-time.Nanosecond), false}, {departure, true}, {arrival, true}, {arrival.Add(time.Nanosecond), false}} {
		if got := b.SimulationDecision(instance, tt.at); got.Allowed != tt.allowed {
			t.Fatalf("%v: %+v", tt.at, got)
		}
	}
	if b.SimulationDecision(instance, now.Add(25*time.Hour)).Allowed || b.MetadataAt(now.Add(25*time.Hour)).ScheduleEligible {
		t.Fatal("schedule expiry did not revoke simulation")
	}
	if b.SimulationDecision(Instance{staticID, "20260914"}, now).Allowed {
		t.Fatal("inactive service date simulated")
	}
}

func TestSimulationServiceDaysAndSourceAssociations(t *testing.T) {
	for _, d := range []schedule.Date{"20260308", "20261101", "20260913"} {
		trip := scheduled("opaque", 25*3600)
		at := d.Instant(25*3600 + 120)
		s := source()
		s.Snapshot.SourceTimestamp, s.Snapshot.ReceivedAt = at.Add(-2*time.Minute), at.Add(-2*time.Minute)
		s.Snapshot.ReplacementPeriods = nil
		b := Reconcile(version(t, at.Add(-time.Hour), trip), []Source{s}, at, time.Minute, 24*time.Hour)
		if got := b.SimulationDecision(Instance{"opaque", d}, at); !got.Allowed {
			t.Fatalf("%s: %+v", d, got)
		}
	}
	tr := realtime.Train{TripID: staticID, RouteID: "A", ServiceDate: string(date)}
	owner, other := source(tr), source(tr)
	other.Name, other.Routes = "other", []string{"C"}
	other.Snapshot.ReplacementPeriods = nil
	b := Reconcile(version(t, now), []Source{owner, other}, now, time.Minute, 24*time.Hour)
	for _, m := range b.Matches() {
		if m.Outcome != Ambiguous || m.Instance.ID() != "" || m.Reason != "multiple_realtime_counterparts" {
			t.Fatalf("duplicate cross-source run retained identity: %+v", m)
		}
	}
	if b.SimulationDecision(Instance{staticID, date}, now.Add(2*time.Minute)).Allowed {
		t.Fatal("duplicate cross-source run simulated")
	}
	other.Snapshot.Trains = nil
	other.Routes = []string{"A"}
	b = Reconcile(version(t, now), []Source{owner, other}, now, time.Minute, 24*time.Hour)
	if m := b.Matches()[0]; m.Outcome != Ambiguous || m.Reason != "ambiguous_route_owner" {
		t.Fatal(m)
	}
	if got := b.SimulationDecision(Instance{staticID, date}, now.Add(2*time.Minute)); got.Allowed || got.Reason != "ambiguous_route_owner" {
		t.Fatal(got)
	}
}

func TestProjectionIdentityAttributionAndVehicleOccurrence(t *testing.T) {
	instance := Instance{TripID: `trip\"|/`, ServiceDate: date}
	decoded, err := base64.RawURLEncoding.DecodeString(instance.ID())
	if err != nil {
		t.Fatal(err)
	}
	var pair [2]string
	if err := json.Unmarshal(decoded, &pair); err != nil || pair != [2]string{instance.TripID, string(date)} {
		t.Fatalf("%s: %v", decoded, err)
	}
	tr := realtime.Train{TripID: staticID, RouteID: "C", ServiceDate: string(date)}
	b := run(t, source(tr))
	if routes := b.MatchRoutes(b.Matches()[0]); !reflect.DeepEqual(routes, []string{"A", "C"}) {
		t.Fatal(routes)
	}
	missing := Reconcile(nil, []Source{source(tr)}, now, time.Minute, 24*time.Hour)
	if routes := missing.MatchRoutes(missing.Matches()[0]); !reflect.DeepEqual(routes, []string{"A", "C"}) {
		t.Fatal(routes)
	}
	s := source(realtime.Train{})
	s.Routes = []string{"A"}
	b = run(t, s)
	if routes := b.MatchRoutes(b.Matches()[0]); !reflect.DeepEqual(routes, []string{"A"}) {
		t.Fatal(routes)
	}
	tr.RouteID, tr.VehicleMatch, tr.VehicleStopSequence = "A", realtime.VehicleMatched, ptr(uint32(3))
	b = run(t, source(tr))
	m := b.Matches()[0]
	if m.VehicleStop == nil || m.VehicleStop.Outcome != Matched || m.VehicleStop.ScheduledIndex != 1 {
		t.Fatal(m)
	}
	m.VehicleStop.ScheduledIndex = 99
	*tr.VehicleStopSequence = 99
	if b.Matches()[0].VehicleStop.ScheduledIndex != 1 || *b.Matches()[0].Train.VehicleStopSequence != 3 {
		t.Fatal("vehicle occurrence was mutable through lookup or input")
	}
}

func TestRetainedSnapshotCannotAcquireNextServiceDate(t *testing.T) {
	priorDate := schedule.Date("20260916")
	for _, tt := range []struct {
		name       string
		start, end int32
		observedAt time.Time
		outcome    Outcome
	}{
		{"daily run", 9 * 3600, 11 * 3600, priorDate.Instant(9*3600 + 60), Matched},
		{"late run", 23 * 3600, 25 * 3600, priorDate.Instant(23*3600 + 1800), Matched},
		{"ambiguous extended days", 0, 49 * 3600, priorDate.Instant(23*3600 + 1800), Ambiguous},
	} {
		t.Run(tt.name, func(t *testing.T) {
			trip := scheduled("opaque", tt.start)
			trip.Stops[1].Arrival.Seconds, trip.Stops[1].Departure.Seconds = tt.end, tt.end
			s := source(realtime.Train{TripID: "opaque", RouteID: "A"})
			s.Snapshot.SourceTimestamp, s.Snapshot.ReceivedAt = tt.observedAt, tt.observedAt
			s.Snapshot.ReplacementPeriods = nil
			initial := Reconcile(version(t, tt.observedAt, trip), []Source{s}, tt.observedAt, time.Minute, 24*time.Hour)
			before := initial.Matches()[0]
			if before.Outcome != tt.outcome {
				t.Fatalf("initial match: %+v", before)
			}
			later := tt.observedAt.Add(24 * time.Hour)
			// A newly validated schedule and another feed may publish while this
			// source retains yesterday's complete snapshot without service dates.
			sibling := Source{Name: "other", Routes: []string{"C"}, Snapshot: realtime.Snapshot{SourceTimestamp: later, ReceivedAt: later, Complete: true, NYCTVersion: "1.0"}}
			rebuilt := Reconcile(version(t, later, trip), []Source{s, sibling}, later, time.Minute, 24*time.Hour)
			after := rebuilt.Matches()[0]
			if after.Outcome != before.Outcome || after.Instance != before.Instance || !reflect.DeepEqual(after.Candidates, before.Candidates) {
				t.Fatalf("retained identity changed: before=%+v after=%+v", before, after)
			}
			if before.Outcome == Matched {
				if after.Instance.ServiceDate != priorDate || rebuilt.SimulationDecision(after.Instance, later).Allowed {
					t.Fatal("old record acquired a current scheduled run")
				}
			}
		})
	}
}

func TestTripObservationRetainsServiceDateAcrossRefreshedHeaders(t *testing.T) {
	priorDate := schedule.Date("20260916")
	observedAt := priorDate.Instant(9*3600 + 60)
	later := observedAt.Add(24 * time.Hour)
	trip := scheduled("opaque", 9*3600)
	tr := realtime.Train{TripID: "opaque", RouteID: "A", TripUpdateTimestamp: observedAt, TripUpdateTimestampPresent: true}
	s := source(tr)
	s.Snapshot.SourceTimestamp, s.Snapshot.ReceivedAt = observedAt, observedAt
	s.Snapshot.ReplacementPeriods = nil
	initial := Reconcile(version(t, observedAt, trip), []Source{s}, observedAt, time.Minute, 24*time.Hour).Matches()[0]
	if initial.Outcome != Matched || initial.Instance.ServiceDate != priorDate {
		t.Fatal(initial)
	}
	s.Snapshot.SourceTimestamp, s.Snapshot.ReceivedAt = later, later
	fresh := Reconcile(version(t, later, trip), []Source{s}, later, time.Minute, 24*time.Hour)
	current := fresh.Matches()[0]
	if current.Outcome != Matched || current.Instance != initial.Instance || fresh.SimulationDecision(current.Instance, later).Allowed {
		t.Fatalf("fresh header reassigned stale observation: %+v", current)
	}
	// A genuinely absent optional observation still uses the refreshed header.
	s.Snapshot.Trains[0].TripUpdateTimestampPresent = false
	s.Snapshot.Trains[0].TripUpdateTimestamp = time.Time{}
	current = Reconcile(version(t, later, trip), []Source{s}, later, time.Minute, 24*time.Hour).Matches()[0]
	if current.Outcome != Matched || current.Instance.ServiceDate != priorDate.AddDays(1) {
		t.Fatalf("absent observation did not use header: %+v", current)
	}
	for _, timestamp := range []time.Time{{}, time.Unix(0, 0), time.Unix(1<<63-1, 0), later.Add(time.Minute)} {
		s.Snapshot.Trains[0].TripUpdateTimestampPresent = true
		s.Snapshot.Trains[0].TripUpdateTimestamp = timestamp
		// Future-at-receipt observations remain invalid after later rebundles.
		for _, publishedAt := range []time.Time{later, later.Add(time.Hour)} {
			current = Reconcile(version(t, publishedAt, trip), []Source{s}, publishedAt, time.Minute, 24*time.Hour).Matches()[0]
			if current.Outcome != Invalid || current.Reason != "invalid_trip_update_timestamp" || current.Instance.ID() != "" {
				t.Fatalf("unusable observation borrowed header date: %+v", current)
			}
		}
	}
	// Explicit service dates stay authoritative even when an observation cannot
	// position a train; the projection freshness gate handles that timestamp.
	s.Snapshot.Trains[0].ServiceDate = string(priorDate)
	current = Reconcile(version(t, later, trip), []Source{s}, later, time.Minute, 24*time.Hour).Matches()[0]
	if current.Outcome != Matched || current.Instance.ServiceDate != priorDate {
		t.Fatalf("observation changed explicit service date: %+v", current)
	}
}

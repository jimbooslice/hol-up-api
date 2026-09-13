package reconcile

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"hol-up-prototype/internal/realtime"
	"hol-up-prototype/internal/schedule"
)

func ptr[T any](value T) *T { return &value }

var date = schedule.Date("20260913")
var now = date.Instant(9 * 3600)

const staticID = "BSP26GEN-A055-Sunday-00_054050_A..N74R"

func scheduled(id string, seconds int32) schedule.Trip {
	return schedule.Trip{ID: id, RouteID: "A", ServiceID: "daily", Direction: 0, Stops: []schedule.StopTime{
		{StopID: "A01N", Sequence: 1, Arrival: schedule.OptionalTime{Seconds: seconds, Present: true}, Departure: schedule.OptionalTime{Seconds: seconds, Present: true}},
		{StopID: "A02N", Sequence: 3, Arrival: schedule.OptionalTime{Seconds: seconds + 7200, Present: true}, Departure: schedule.OptionalTime{Seconds: seconds + 7200, Present: true}},
	}}
}
func version(t *testing.T, at time.Time, trips ...schedule.Trip) *schedule.Version {
	t.Helper()
	if len(trips) == 0 {
		trips = []schedule.Trip{scheduled(staticID, 9*3600+30)}
	}
	data, err := schedule.NewDataset(schedule.Data{
		Agencies: []schedule.Agency{{ID: "mta", Name: "MTA", URL: "https://www.mta.info", Timezone: schedule.Timezone}},
		Routes:   []schedule.Route{{ID: "A", AgencyID: "mta", ShortName: "A"}, {ID: "C", AgencyID: "mta", ShortName: "C"}},
		Stops:    []schedule.Stop{{ID: "A01N"}, {ID: "A02N"}}, Trips: trips,
		Calendars:  []schedule.Calendar{{ServiceID: "daily", Start: "20260101", End: "20261231", Days: [7]bool{true, true, true, true, true, true, true}}},
		Exceptions: []schedule.Exception{{ServiceID: "daily", Date: "20260914", Added: false}},
	})
	if err != nil {
		t.Fatal(err)
	}
	v, err := schedule.NewVersion(data, schedule.Metadata{Source: schedule.Regular, ParserSchema: "test", ArchiveSHA256: strings.Repeat("a", 64), ValidatedAt: at, PublishedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func source(trains ...realtime.Train) Source {
	return Source{Name: "ace", Routes: []string{"A", "C"}, Snapshot: realtime.Snapshot{SourceTimestamp: now, ReceivedAt: now, SourceName: "ace", Complete: true, NYCTVersion: "1.0", Trains: trains,
		ReplacementPeriods: []realtime.ReplacementPeriod{{RouteID: "A", Start: ptr(uint64(now.Unix())), End: ptr(uint64(now.Add(time.Hour).Unix()))}}}}
}
func run(t *testing.T, s Source, trips ...schedule.Trip) *Bundle {
	return Reconcile(version(t, now, trips...), []Source{s}, now, time.Minute, 24*time.Hour)
}
func TestIdentityMatching(t *testing.T) {
	tests := []struct {
		name, id     string
		edit         func(*realtime.Train)
		outcome      Outcome
		rule, reason string
	}{
		{name: "exact", id: staticID, outcome: Matched, rule: "exact_id"},
		{name: "suffix", id: "054050_A..N74R", outcome: Matched, rule: "origin_path"},
		{name: "historical opaque prefix", id: "A20111204SAT_054050_A..N74R", outcome: Matched, rule: "origin_path"},
		{name: "without path", id: "054050_A..N", outcome: Matched, rule: "origin_route_direction"},
		{name: "unknown exact", id: "opaque", outcome: Matched, rule: "exact_id"},
		{name: "explicit origin", id: "unknown", edit: func(tr *realtime.Train) { tr.StartTime = "09:00:30"; tr.DirectionID = ptr(uint32(0)) }, outcome: Matched, rule: "origin_route_direction"},
		{name: "no guessing", id: "unknown", outcome: Unmatched, rule: "origin_route_direction"},
		{name: "route conflict", id: staticID, edit: func(tr *realtime.Train) { tr.RouteID = "C" }, outcome: Invalid, reason: "route_conflict"},
		{name: "direction conflict", id: staticID, edit: func(tr *realtime.Train) { tr.DirectionID = ptr(uint32(1)) }, outcome: Invalid, reason: "direction_id_conflict"},
		{name: "supplied NYCT conflict", id: "054050_A..N", edit: func(tr *realtime.Train) { tr.NYCTDirection = ptr(realtime.DirectionSouthbound) }, outcome: Invalid, reason: "nyct_direction_conflict"},
		{name: "display inference ignored", id: staticID, edit: func(tr *realtime.Train) { tr.Direction = realtime.DirectionSouthbound }, outcome: Matched},
		{name: "NYCT unknown", id: staticID, edit: func(tr *realtime.Train) { tr.NYCTDirection = ptr(realtime.DirectionUnknown) }, outcome: Invalid, reason: "nyct_direction_conflict"},
		{name: "start conflict", id: staticID, edit: func(tr *realtime.Train) { tr.StartTime = "09:00:00" }, outcome: Invalid, reason: "start_time_conflict"},
		{name: "bad start", id: staticID, edit: func(tr *realtime.Train) { tr.StartTime = "nine" }, outcome: Invalid, reason: "invalid_start_time"},
		{name: "bad date", id: staticID, edit: func(tr *realtime.Train) { tr.ServiceDate = "20260230" }, outcome: Invalid, reason: "invalid_service_date"},
		{name: "inactive date", id: staticID, edit: func(tr *realtime.Train) { tr.ServiceDate = "20260914" }, outcome: Invalid, reason: "service_inactive_or_outside_coverage"},
		{name: "outside coverage", id: staticID, edit: func(tr *realtime.Train) { tr.ServiceDate = "20270101" }, outcome: Invalid, reason: "service_inactive_or_outside_coverage"},
		{name: "path cannot weaken", id: "054050_A..N99X", outcome: Unmatched, rule: "origin_path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := realtime.Train{TripID: tt.id, RouteID: "A", ServiceDate: string(date)}
			if tt.edit != nil {
				tt.edit(&tr)
			}
			trip := scheduled(staticID, 9*3600+30)
			if tt.name == "unknown exact" {
				trip.ID = "opaque"
			}
			got := run(t, source(tr), trip).Matches()[0]
			if got.Outcome != tt.outcome || (tt.rule != "" && got.Rule != tt.rule) || (tt.reason != "" && got.Reason != tt.reason) {
				t.Fatalf("got %+v", got)
			}
		})
	}
}
func TestPrecisionAndCurrentPrefix(t *testing.T) {
	for _, id := range []string{"BSP26GEN-A055-Sunday-00_000950_A..S74R", "A20111204SAT_000950_A..S74R"} {
		parsed := parseID(id)
		if !parsed.known || parsed.origin != 9*time.Minute+30*time.Second || parsed.route != "A" || parsed.direction != "S" || parsed.path != "74R" {
			t.Fatalf("%s: %+v", id, parsed)
		}
	}
	trip := scheduled("opaque_054001_A..N74R", 32400)
	for _, tt := range []struct {
		id, start string
		want      Outcome
	}{{"054001_A..N74R", "", Matched}, {"054000_A..N74R", "", Unmatched}, {trip.ID, "09:00:00", Invalid}} {
		got := run(t, source(realtime.Train{TripID: tt.id, ServiceDate: string(date), StartTime: tt.start}), trip).Matches()[0]
		if got.Outcome != tt.want {
			t.Fatalf("precision lost: %+v", got)
		}
	}
	if parseID("prefix_054050_A-N74R").known || parseID("prefix_054050_A..N74R_garbage").known {
		t.Fatal("guessed unknown format")
	}
}
func TestBidirectionalUniquenessAndStrongerConflict(t *testing.T) {
	first := realtime.Train{TripID: staticID, RouteID: "A", ServiceDate: string(date)}
	second := first
	second.TripID = "054050_A..N74R"
	for _, records := range [][]realtime.Train{{first, second}, {second, first}} {
		b := run(t, source(records...))
		for _, m := range b.Matches() {
			if m.Outcome != Ambiguous || m.Instance.TripID != "" {
				t.Fatalf("priority resolved duplicate: %+v", m)
			}
		}
		if b.Fallback(Instance{staticID, date}, now).Suppressed {
			t.Fatal("ambiguous counterpart suppressed")
		}
	}
	other := scheduled("historical_054050_A..N74R", 32430)
	b := run(t, source(second), scheduled(staticID, 32430), other)
	if m := b.Matches()[0]; m.Outcome != Ambiguous || len(m.Candidates) != 2 {
		t.Fatalf("%+v", m)
	}
	first.RouteID = "C" // Exact conflict must not use a weaker suffix candidate.
	b = run(t, source(first), scheduled(staticID, 32430), other)
	if m := b.Matches()[0]; m.Outcome != Invalid || m.Rule != "exact_id" || len(m.Candidates) != 1 {
		t.Fatalf("%+v", m)
	}
	s := source(second)
	s.Snapshot.Complete = false
	if run(t, s).Matches()[0].Outcome != Ambiguous {
		t.Fatal("filtered feed established uniqueness")
	}
}
func TestStopResolutionAndPredictionsUnchanged(t *testing.T) {
	trip := scheduled(staticID, 32430)
	trip.Stops = append(trip.Stops, schedule.StopTime{StopID: "A01N", Sequence: 5, Arrival: schedule.OptionalTime{Seconds: 40000, Present: true}, Departure: schedule.OptionalTime{Seconds: 40000, Present: true}})
	tests := []struct {
		name  string
		stops []realtime.StopPrediction
		want  []string
	}{
		{"sequence only", []realtime.StopPrediction{{Sequence: ptr(uint32(3))}}, []string{"resolved"}},
		{"ID only unique", []realtime.StopPrediction{{StopID: "A02N"}}, []string{"resolved"}},
		{"repeated ID", []realtime.StopPrediction{{StopID: "A01N"}}, []string{"repeated_stop"}},
		{"both", []realtime.StopPrediction{{StopID: "A01N", Sequence: ptr(uint32(5))}}, []string{"resolved"}},
		{"conflict", []realtime.StopPrediction{{StopID: "A02N", Sequence: ptr(uint32(1))}}, []string{"stop_id_sequence_conflict"}},
		{"missing", []realtime.StopPrediction{{}}, []string{"missing_stop_identity"}},
		{"unknown", []realtime.StopPrediction{{StopID: "missing"}}, []string{"unknown_stop"}},
		{"duplicate", []realtime.StopPrediction{{Sequence: ptr(uint32(1))}, {Sequence: ptr(uint32(1))}}, []string{"duplicate_stop_update", "duplicate_stop_update"}},
		{"order", []realtime.StopPrediction{{Sequence: ptr(uint32(3))}, {Sequence: ptr(uint32(1))}}, []string{"conflicting_stop_order", "conflicting_stop_order"}},
		{"nonadjacent order", []realtime.StopPrediction{{Sequence: ptr(uint32(5))}, {Sequence: ptr(uint32(1))}, {Sequence: ptr(uint32(3))}}, []string{"conflicting_stop_order", "conflicting_stop_order", "conflicting_stop_order"}},
		{"original order", []realtime.StopPrediction{{Sequence: ptr(uint32(1)), InputIndex: ptr(uint32(1))}, {Sequence: ptr(uint32(3)), InputIndex: ptr(uint32(0))}}, []string{"conflicting_stop_order", "conflicting_stop_order"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := realtime.Train{TripID: staticID, RouteID: "A", ServiceDate: string(date), Stops: tt.stops}
			tr.Stops[0].Arrival = &realtime.StopEvent{Time: now.Add(time.Hour), DelaySeconds: ptr(int32(0))}
			tr.Stops[0].Relationship = realtime.StopNoData
			m := run(t, source(tr), trip).Matches()[0]
			if m.Outcome != Matched || !reflect.DeepEqual(tr, m.Train) {
				t.Fatalf("changed original: %+v", m)
			}
			for i, reason := range tt.want {
				if m.Stops[i].Reason != reason {
					t.Fatalf("%+v", m.Stops)
				}
			}
		})
	}
}
func TestReplacementWindowsAndAuthority(t *testing.T) {
	departure := uint64(date.Instant(32430).Unix())
	tests := []struct {
		name     string
		edit     func(*Source)
		suppress bool
		valid    bool
	}{
		{"start inclusive", func(s *Source) { s.Snapshot.ReplacementPeriods[0].Start = ptr(departure) }, true, true},
		{"before start", func(s *Source) { s.Snapshot.ReplacementPeriods[0].Start = ptr(departure + 1) }, false, true},
		{"end exclusive", func(s *Source) { s.Snapshot.ReplacementPeriods[0].End = ptr(departure) }, false, true},
		{"before end", func(s *Source) { s.Snapshot.ReplacementPeriods[0].End = ptr(departure + 1) }, true, true},
		{"omitted start", func(s *Source) { s.Snapshot.ReplacementPeriods[0].Start = nil }, true, true},
		{"omitted end", func(s *Source) { s.Snapshot.ReplacementPeriods[0].End = nil }, true, true},
		{"explicit zero", func(s *Source) { s.Snapshot.ReplacementPeriods[0].Start = ptr(uint64(0)) }, true, true},
		{"no bounds", func(s *Source) {
			s.Snapshot.ReplacementPeriods[0].Start = nil
			s.Snapshot.ReplacementPeriods[0].End = nil
		}, false, false},
		{"empty", func(s *Source) {
			s.Snapshot.ReplacementPeriods[0].Start = ptr(departure)
			s.Snapshot.ReplacementPeriods[0].End = ptr(departure)
		}, false, false},
		{"reversed", func(s *Source) {
			s.Snapshot.ReplacementPeriods[0].Start = ptr(departure + 1)
			s.Snapshot.ReplacementPeriods[0].End = ptr(departure)
		}, false, false},
		{"overflow", func(s *Source) { s.Snapshot.ReplacementPeriods[0].End = ptr(uint64(1 << 63)) }, false, false},
		{"time overflow", func(s *Source) { s.Snapshot.ReplacementPeriods[0].End = ptr(uint64(1<<63 - 1)) }, false, false},
		{"wrong owner", func(s *Source) { s.Routes = []string{"C"} }, false, false},
		{"incomplete", func(s *Source) { s.Snapshot.Complete = false }, false, false},
		{"unsupported version", func(s *Source) { s.Snapshot.NYCTVersion = "2.0" }, false, false},
		{"missing route", func(s *Source) { s.Snapshot.ReplacementPeriods[0].RouteID = "" }, false, false},
		{"stale", func(s *Source) { s.Snapshot.SourceTimestamp = now.Add(-time.Minute - time.Nanosecond) }, false, true},
		{"unavailable", func(s *Source) { s.Snapshot.SourceTimestamp = time.Time{} }, false, true},
		{"malformed sibling", func(s *Source) {
			s.Snapshot.ReplacementPeriods = append(s.Snapshot.ReplacementPeriods, realtime.ReplacementPeriod{RouteID: "A"})
		}, false, false},
		{"unlocalizable sibling", func(s *Source) {
			s.Snapshot.ReplacementPeriods = append(s.Snapshot.ReplacementPeriods, realtime.ReplacementPeriod{})
		}, false, false},
		{"other route malformed", func(s *Source) {
			s.Snapshot.ReplacementPeriods = append(s.Snapshot.ReplacementPeriods, realtime.ReplacementPeriod{RouteID: "C"})
		}, true, true},
		{"union", func(s *Source) {
			s.Snapshot.ReplacementPeriods = append(s.Snapshot.ReplacementPeriods, s.Snapshot.ReplacementPeriods[0])
			s.Snapshot.ReplacementPeriods[0].End = ptr(departure)
		}, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := source()
			tt.edit(&s)
			b := run(t, s)
			if got := b.Fallback(Instance{staticID, date}, now); got.Suppressed != tt.suppress {
				t.Fatalf("%+v", got)
			}
			if rules := b.ReplacementRules(); rules[0].Valid != tt.valid {
				t.Fatalf("%+v", rules)
			}
		})
	}
}
func TestCounterpartsAndLookupFreshness(t *testing.T) {
	instance := Instance{staticID, date}
	for _, relationship := range []string{"SCHEDULED", "CANCELED", "DELETED"} {
		tr := realtime.Train{TripID: staticID, RouteID: "A", ServiceDate: string(date), ScheduleRelationship: relationship}
		b := run(t, source(tr))
		decision := b.Fallback(instance, now)
		if relationship == "SCHEDULED" {
			if decision.Eligible || decision.Suppressed || decision.Reason != "active_realtime_counterpart" {
				t.Fatal(decision)
			}
		} else if !decision.Suppressed {
			t.Fatal(decision)
		}
	}
	for _, tr := range []realtime.Train{{TripID: staticID, RouteID: "C", ServiceDate: string(date)}, {TripID: "unknown", RouteID: "A"}, {}, {TripID: staticID, ServiceDate: "bad"}} {
		if got := run(t, source(tr)).Fallback(instance, now); got.Suppressed {
			t.Fatalf("uncertainty suppressed: %+v", tr)
		}
	}
	b := run(t, source())
	if !b.Fallback(instance, now).Suppressed {
		t.Fatal("empty complete feed not authoritative")
	}
	if b.Fallback(instance, now.Add(time.Minute+time.Nanosecond)).Suppressed {
		t.Fatal("outage retained authority")
	}
	// A future trip keeps freshness expiry independent of departure passing.
	future := Instance{"opaque", date}
	b = run(t, source(), scheduled("opaque", 33000))
	if !b.Fallback(future, now.Add(time.Minute)).Suppressed || b.Fallback(future, now.Add(time.Minute+time.Nanosecond)).Suppressed {
		t.Fatal("freshness boundary")
	}
	if b.Fallback(future, now.Add(25*time.Hour)).Eligible {
		t.Fatal("expired schedule eligible")
	}
	if b.Fallback(Instance{"missing", date}, now).Eligible || b.Fallback(Instance{"opaque", "20260914"}, now).Eligible {
		t.Fatal("invalid instance eligible")
	}
	b = run(t, source(), scheduled("opaque", 32400))
	if b.Fallback(future, now).Suppressed {
		t.Fatal("nonfuture suppressed")
	}
	for _, v := range []*schedule.Version{nil, version(t, now.Add(-25*time.Hour))} {
		b = Reconcile(v, []Source{source(realtime.Train{TripID: staticID})}, now, time.Minute, 24*time.Hour)
		if b.Metadata().ScheduleEligible || b.Fallback(instance, now).Eligible || b.Matches()[0].Outcome != Unavailable {
			t.Fatal("missing/ineligible authority")
		}
	}
	s := source()
	b = Reconcile(version(t, now), []Source{s, s}, now, time.Minute, 24*time.Hour)
	if b.Fallback(instance, now).Suppressed {
		t.Fatal("duplicate owners authoritative")
	}
}
func TestServiceDaysMidnightDSTAndExtendedHours(t *testing.T) {
	for _, d := range []schedule.Date{"20260308", "20261101", "20260913"} {
		trip := scheduled("opaque", 25*3600)
		at := d.Instant(25*3600 - 30)
		s := source(realtime.Train{TripID: "opaque", RouteID: "A", ServiceDate: string(d)})
		s.Snapshot.SourceTimestamp = at
		s.Snapshot.ReplacementPeriods = []realtime.ReplacementPeriod{{RouteID: "A", End: ptr(uint64(at.Add(time.Hour).Unix()))}}
		b := Reconcile(version(t, at, trip), []Source{s}, at, time.Minute, 24*time.Hour)
		m := b.Matches()[0]
		if m.Outcome != Matched || m.Instance.ServiceDate != d {
			t.Fatalf("%s: %+v", d, m)
		}
		s.Snapshot.Trains[0].ServiceDate = ""
		inferred := Reconcile(version(t, at, trip), []Source{s}, at, time.Minute, 24*time.Hour).Matches()[0]
		found := false
		for _, candidate := range inferred.Candidates {
			if candidate.ServiceDate == d {
				found = true
			}
		}
		if !found || (len(inferred.Candidates) > 1 && inferred.Outcome != Ambiguous) {
			t.Fatalf("inferred dates: %+v", inferred)
		}
		s.Snapshot.Trains = nil
		b = Reconcile(version(t, at, trip), []Source{s}, at, time.Minute, 24*time.Hour)
		if !b.Fallback(Instance{"opaque", d}, at).Suppressed {
			t.Fatalf("%s: wrong departure conversion", d)
		}
	}
	// An extended schedule can leave two active dates: never choose by date order.
	trip := scheduled("opaque", 0)
	trip.Stops[1].Arrival.Seconds = 49 * 3600
	trip.Stops[1].Departure.Seconds = 49 * 3600
	if m := run(t, source(realtime.Train{TripID: "opaque"}), trip).Matches()[0]; m.Outcome != Ambiguous {
		t.Fatalf("%+v", m)
	}
}
func TestBundleCopiesMutableInputsAndLookups(t *testing.T) {
	tr := realtime.Train{TripID: staticID, RouteID: "A", ServiceDate: string(date), NYCTDirection: ptr(realtime.DirectionNorthbound), Stops: []realtime.StopPrediction{{Sequence: ptr(uint32(1)), Relationship: realtime.StopSkipped, Arrival: &realtime.StopEvent{DelaySeconds: ptr(int32(0))}}}}
	s := source(tr)
	b := run(t, s)
	*s.Snapshot.ReplacementPeriods[0].Start = 1
	s.Routes[0] = "bad"
	*tr.Stops[0].Sequence = 99
	matches := b.Matches()
	*matches[0].Train.Stops[0].Arrival.DelaySeconds = 5
	matches[0].Candidates[0].TripID = "bad"
	matches[0].Stops[0].StopID = "bad"
	matches[0].Reference.Source.Routes[0] = "bad"
	rules := b.ReplacementRules()
	*rules[0].Period.Start = 99
	rules[0].Reference.Source.Routes[0] = "bad"
	sources := b.Sources()
	sources[0].Routes[0] = "bad"
	m := b.Matches()[0]
	if *m.Train.Stops[0].Sequence != 1 || *m.Train.Stops[0].Arrival.DelaySeconds != 0 || m.Candidates[0].TripID != staticID || m.Stops[0].StopID != "A01N" || b.Sources()[0].Routes[0] != "A" || *b.ReplacementRules()[0].Period.Start != uint64(now.Unix()) {
		t.Fatal("bundle mutated")
	}
}

func TestCandidateRejectionsAndRouteConflictProtection(t *testing.T) {
	tr := realtime.Train{TripID: staticID, RouteID: "C", ServiceDate: string(date)}
	other := scheduled("C-trip", 32430)
	other.RouteID = "C"
	s := source(tr)
	s.Snapshot.ReplacementPeriods = append(s.Snapshot.ReplacementPeriods, realtime.ReplacementPeriod{RouteID: "C", End: ptr(uint64(now.Add(time.Hour).Unix()))})
	b := run(t, s, scheduled(staticID, 32430), other)
	if b.Fallback(Instance{staticID, date}, now).Suppressed || b.Fallback(Instance{"C-trip", date}, now).Suppressed {
		t.Fatal("conflicting route identity established absence")
	}
	matches := b.Matches()
	if len(matches[0].Rejections) != 1 || matches[0].Rejections[0].Reason != "route_conflict" {
		t.Fatal("candidate rejection missing")
	}
	matches[0].Rejections[0].Reason = "changed"
	if b.Matches()[0].Rejections[0].Reason != "route_conflict" {
		t.Fatal("rejection lookup aliased")
	}
}
func TestUnidentifiedCounterpartAndUnboundedFutureLookup(t *testing.T) {
	tr := realtime.Train{RouteID: "A", ServiceDate: string(date), StartTime: "09:00:30", DirectionID: ptr(uint32(0))}
	s := source(realtime.Train{TripID: staticID, ServiceDate: string(date)})
	s.Snapshot.UnidentifiedTrips = []realtime.Train{tr}
	b := run(t, s)
	matches := b.Matches()
	if matches[0].Outcome != Ambiguous || matches[1].Outcome != Ambiguous || !matches[1].Unidentified {
		t.Fatal("unidentified descriptor did not participate in reverse uniqueness")
	}
	s = source()
	s.Snapshot.UnidentifiedTrips = []realtime.Train{{}}
	if run(t, s).Fallback(Instance{staticID, date}, now).Suppressed {
		t.Fatal("unlocalized unidentified record allowed suppression")
	}
	s = source()
	s.Snapshot.ReplacementPeriods[0].End = nil
	if !run(t, s).Fallback(Instance{staticID, "20261213"}, now).Suppressed {
		t.Fatal("unbounded window requires enumeration")
	}
	// Ownership follows the resolved schedule when the realtime route is omitted.
	m := run(t, source(realtime.Train{TripID: staticID, ServiceDate: string(date)})).Matches()[0]
	if !m.Owned {
		t.Fatal("resolved route ownership missing")
	}
}

package schedule_test

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"hol-up-prototype/internal/schedule"
)

func fixture(t *testing.T, id string) (schedule.Data, *schedule.Dataset) {
	t.Helper()
	data := schedule.Data{
		Agencies: []schedule.Agency{{ID: "mta", Name: "MTA", URL: "https://www.mta.info", Timezone: schedule.Timezone}},
		Routes:   []schedule.Route{{ID: id, ShortName: id}},
		Stops:    []schedule.Stop{{ID: "S1"}, {ID: "S2"}},
		Trips: []schedule.Trip{{ID: id, RouteID: id, ServiceID: "daily", Direction: -1, Stops: []schedule.StopTime{
			{StopID: "S2", Sequence: 9, Arrival: schedule.OptionalTime{Seconds: 26 * 3600, Present: true}, Departure: schedule.OptionalTime{Seconds: 26 * 3600, Present: true}},
			{StopID: "S1", Sequence: 1, Arrival: schedule.OptionalTime{Seconds: 23 * 3600, Present: true}, Departure: schedule.OptionalTime{Seconds: 23 * 3600, Present: true}},
		}}},
		Calendars:  []schedule.Calendar{{ServiceID: "daily", Start: "20260101", End: "20261231", Days: [7]bool{true, true, true, true, true, true, true}}},
		Exceptions: []schedule.Exception{{ServiceID: "daily", Date: "20260913", Added: false}},
	}
	d, err := schedule.NewDataset(data)
	if err != nil {
		t.Fatal(err)
	}
	return data, d
}

func TestServiceDaysDSTExceptionsAndOvernight(t *testing.T) {
	_, d := fixture(t, "A")
	for _, tc := range []struct {
		date    schedule.Date
		seconds int32
		utc     string
	}{
		{"20260308", 0, "2026-03-08T04:00:00Z"},
		{"20260308", 3 * 3600, "2026-03-08T07:00:00Z"},
		{"20261101", 0, "2026-11-01T05:00:00Z"},
		{"20261101", 3600, "2026-11-01T06:00:00Z"},
		{"20260912", 25 * 3600, "2026-09-13T05:00:00Z"},
	} {
		if got := tc.date.Instant(tc.seconds).UTC().Format(time.RFC3339); got != tc.utc {
			t.Errorf("%s + %d = %s, want %s", tc.date, tc.seconds, got, tc.utc)
		}
	}
	if d.Active("daily", "20260913") || !d.Active("daily", "20260912") || d.Active("missing", "20260912") || d.Active("daily", "20270101") {
		t.Fatal("calendar exception or bounds ignored")
	}
	for _, date := range []string{"20260229", "20260931", "2026-09-12", "00000101", "2026111"} {
		if _, err := schedule.ParseDate(date); err == nil {
			t.Errorf("accepted %s", date)
		}
	}
	at := time.Date(2026, 9, 13, 5, 0, 0, 0, time.UTC)
	if got := d.CandidateServiceDates(at); !slices.Contains(got, schedule.Date("20260912")) {
		t.Fatalf("lost preceding overnight date: %v", got)
	}
	spring := time.Date(2026, 3, 8, 4, 30, 0, 0, time.UTC) // 23:30 on March 7 locally.
	if got := d.CandidateServiceDates(spring); !slices.Contains(got, schedule.Date("20260308")) {
		t.Fatalf("lost spring origin: %v", got)
	}
	data, _ := fixture(t, "A")
	data.Calendars = nil
	data.Exceptions = []schedule.Exception{{ServiceID: "daily", Date: "20260912", Added: true}, {ServiceID: "daily", Date: "20260914", Added: true}}
	d, err := schedule.NewDataset(data)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Active("daily", "20260912") || d.Active("daily", "20260913") || !d.Active("daily", "20260914") {
		t.Fatal("exception-only calendar failed")
	}
}

func TestPublicationIsImmutableAndConsistent(t *testing.T) {
	data, d := fixture(t, "A")
	data.Trips[0].Stops[0].StopID = "changed"
	data.Calendars[0].Days = [7]bool{}
	data.Routes[0].ShortName = "changed"
	data.Agencies[0].Name = "changed"
	check := func(d *schedule.Dataset, id string) error {
		ids := d.RouteTrips(id)
		if len(ids) != 1 || ids[0] != id {
			return fmt.Errorf("route index: %v", ids)
		}
		trip, ok := d.Trip(ids[0])
		if !ok || trip.RouteID != id || trip.Stops[0].StopID != "S1" || trip.Stops[1].StopID != "S2" {
			return fmt.Errorf("trip/index mismatch: %+v", trip)
		}
		ids[0] = "changed"
		trip.Stops[0].StopID = "changed"
		d.ServiceTrips("daily")[0] = "changed"
		d.RouteIDs()[0] = "changed"
		d.Agencies()[0].Name = "changed"
		if !d.Active("daily", "20260912") {
			return fmt.Errorf("calendar mutated")
		}
		return nil
	}
	if err := check(d, "A"); err != nil {
		t.Fatal(err)
	}
	if r, _ := d.Route("A"); r.ShortName != "A" || d.Agencies()[0].Name != "MTA" {
		t.Fatal("input or lookup mutation changed agency/route data")
	}
	var store schedule.Store
	now := time.Date(2026, 9, 12, 16, 0, 0, 0, time.UTC)
	versions := make([]*schedule.Version, 2)
	for i, id := range []string{"A", "B"} {
		_, dataset := fixture(t, id)
		v, err := schedule.NewVersion(dataset, schedule.Metadata{Source: schedule.Supplemented, ParserSchema: "1", ArchiveSHA256: strings.Repeat(id, 64), ValidatedAt: now, PublishedAt: now})
		if err != nil {
			t.Fatal(err)
		}
		versions[i] = v
	}
	store.Publish(versions[0])
	held := store.Load()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			for j := 0; j < 500; j++ {
				v := store.Load()
				id := v.Metadata().ArchiveSHA256[:1]
				if err := check(v.Dataset(), id); err != nil {
					t.Error(err)
					return
				}
				m := v.Metadata()
				m.Version = "changed"
			}
		})
	}
	for i := 0; i < 1000; i++ {
		store.Publish(versions[i%2])
	}
	wg.Wait()
	if held != versions[0] || held.Metadata().Version == "changed" {
		t.Fatal("held publication changed")
	}
}

func TestEligibilityAndSeparatePlanningCoverage(t *testing.T) {
	_, d := fixture(t, "A")
	now := time.Date(2026, 9, 12, 16, 0, 0, 0, time.UTC)
	v, err := schedule.NewVersion(d, schedule.Metadata{Source: schedule.Supplemented, ParserSchema: "1", ArchiveSHA256: strings.Repeat("a", 64), ValidatedAt: now, PublishedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if v.Metadata().PlanningCoverage != (schedule.Coverage{Start: "20260912", End: "20260918"}) || v.Metadata().Coverage.End != "20261231" {
		t.Fatal(v.Metadata())
	}
	if !v.Eligible(now.Add(24*time.Hour), 24*time.Hour) || v.Eligible(now.Add(24*time.Hour+time.Nanosecond), 24*time.Hour) || v.Eligible(now.Add(-time.Second), 24*time.Hour) {
		t.Fatal("incorrect freshness boundaries")
	}
	if v.Eligible(time.Date(2027, 1, 1, 16, 0, 0, 0, time.UTC), 365*24*time.Hour) {
		t.Fatal("coverage ignored")
	}
}

func TestCoverageIncludesFinalOvernightService(t *testing.T) {
	data, _ := fixture(t, "A")
	data.FeedInfo.End = "20260912"
	d, err := schedule.NewDataset(data)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 12, 16, 0, 0, 0, time.UTC)
	v, err := schedule.NewVersion(d, schedule.Metadata{Source: schedule.Supplemented, ParserSchema: "1", ArchiveSHA256: strings.Repeat("a", 64), ValidatedAt: now, PublishedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	end := schedule.Date("20260912").Instant(26 * 3600)
	if !v.Eligible(end, 24*time.Hour) || v.Eligible(end.Add(time.Second), 24*time.Hour) {
		t.Fatal("last covered service date lost extended-hour coverage")
	}
	if d.Active("daily", "20260913") {
		t.Fatal("overnight coverage activated an unadvertised service date")
	}
}

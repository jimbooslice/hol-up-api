package httpapi

import (
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"hol-up-prototype/internal/realtime"
	"hol-up-prototype/internal/reconcile"
	"hol-up-prototype/internal/schedule"
	"hol-up-prototype/internal/telemetry"
)

func TestDocumentedProjectionExamplesMatchHTTPContracts(t *testing.T) {
	for _, test := range []struct {
		name string
		dto  any
	}{
		{"RouteGeometry", &geometryResponse{}},
		{"PositionsWithoutSchedule", &positionsResponse{}},
		{"RealtimePositions", &positionsResponse{}},
		{"StaleSchedulePositions", &positionsResponse{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			example := documentedExample(t, test.name)
			// JSON is a YAML subset, so the examples need no new YAML dependency.
			decoder := json.NewDecoder(strings.NewReader(example))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(test.dto); err != nil {
				t.Fatalf("example differs from HTTP DTO: %v", err)
			}
			var documented, encoded any
			if err := json.NewDecoder(strings.NewReader(example)).Decode(&documented); err != nil {
				t.Fatal(err)
			}
			wire, err := json.Marshal(test.dto)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(wire, &encoded); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(documented, encoded) {
				t.Fatalf("example omits or changes HTTP fields: %s", wire)
			}
		})
	}
}

func TestUpcomingAndExcludedVehicleHTTPContract(t *testing.T) {
	at := time.Date(2026, 9, 13, 4, 10, 0, 0, time.UTC)
	v := geometryVersion(t, at, "a", -73.9)
	run := realtime.Train{TripID: "trip", EntityID: "update", RouteID: "A", ServiceDate: "20260913",
		VehicleMatch: realtime.VehicleMatched, VehicleStopID: "platform", VehicleStopSequence: new(uint32(2)),
		VehicleTimestamp: at, VehicleStatus: "STOPPED_AT",
		Stops: []realtime.StopPrediction{
			{StopID: "platform", Arrival: &realtime.StopEvent{Time: at.Add(30 * time.Second)}, Departure: &realtime.StopEvent{Time: at.Add(30 * time.Second)}},
			{StopID: "second", Arrival: &realtime.StopEvent{Time: at.Add(5 * time.Minute)}},
		},
	}
	results := &reconcile.Store{}
	results.Publish(reconcile.Reconcile(v, []reconcile.Source{{Name: "ace", Routes: []string{"A"}, Snapshot: realtime.Snapshot{
		SourceTimestamp: at, ReceivedAt: at, Complete: true, NYCTVersion: "1.0", Trains: []realtime.Train{run},
	}}}, at, time.Minute, time.Hour))
	now := at
	h := NewHandler(nil, nil, func() time.Time { return now }, time.Minute, results)
	for _, offset := range []time.Duration{0, 31 * time.Second} {
		now = at.Add(offset)
		response := get(h, "/api/v1/trains?route_id=A&view=positions")
		var body positionsResponse
		decode(t, response, &body)
		if response.Code != http.StatusOK || len(body.Trains) != 1 {
			t.Fatalf("response: %s", response.Body.String())
		}
		p := body.Trains[0]
		if !slices.Equal(p.ReconciliationWarnings, []string{"vehicle_stop_sequence_conflict_ignored"}) {
			t.Fatalf("warnings: %+v", p)
		}
		if offset == 0 {
			if p.Latitude != nil || p.ShapeDistanceMeters != nil || p.NextStationID != nil || p.NextArrivalAt != nil || value(p.NoPositionReason) != "not_started" || value(p.PredictedDepartureAt) != "2026-09-13T04:10:30Z" || p.MovementState != realtime.MovementStateUnknown {
				t.Fatalf("upcoming: %+v", p)
			}
		} else if p.Latitude == nil || p.ShapeDistanceMeters == nil || value(p.NextStationID) != "second" || p.NextStopSequence == nil || *p.NextStopSequence != 2 || value(p.NextArrivalAt) != "2026-09-13T04:15:00Z" || p.NoPositionReason != nil || p.PredictedDepartureAt != nil || p.MovementState != realtime.MovementStateInferredMoving || !strings.Contains(response.Body.String(), `"predicted_departure_at":null`) {
			t.Fatalf("positioned: %+v", p)
		}
	}
}

func documentedExample(t *testing.T, name string) string {
	t.Helper()
	_, examples, found := strings.Cut(string(openAPI), "\n  examples:\n")
	if !found {
		t.Fatal("OpenAPI component examples missing")
	}
	_, example, found := strings.Cut(examples, "    "+name+":\n")
	if !found {
		t.Fatal("documented example missing")
	}
	_, example, found = strings.Cut(example, "      value: ")
	if !found {
		t.Fatal("example value missing")
	}
	return example
}

func TestProjectionUnavailableAndCapturedSourceState(t *testing.T) {
	at := time.Date(2026, 9, 13, 4, 10, 0, 0, time.UTC)
	store, results := realtime.NewStore(), &reconcile.Store{}
	h := NewHandler([]Source{{Name: "ace", Routes: []string{"A"}, Store: store}}, telemetry.New(), func() time.Time { return at }, time.Minute, results)
	for _, path := range []string{"/api/v1/routes/A/geometry", "/api/v1/trains?route_id=A&view=positions"} {
		if got := get(h, path); got.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s = %d/%s", path, got.Code, got.Body.String())
		}
	}
	if got := get(h, "/api/v1/trains?route_id=A&view=unknown"); got.Code != http.StatusBadRequest || !strings.Contains(got.Body.String(), "invalid_view") {
		t.Fatalf("invalid view = %d/%s", got.Code, got.Body.String())
	}
	// An independently newer source must not change this bundle's provenance.
	replace(t, store, realtime.Snapshot{SourceTimestamp: at.Add(time.Second)})
	results.Publish(reconcile.Reconcile(nil, []reconcile.Source{{Name: "ace", Routes: []string{"A"}, Snapshot: realtime.Snapshot{
		SourceTimestamp: at, Trains: []realtime.Train{{RouteID: "A", TripID: "unprojectable", EntityID: "one"}},
		UnidentifiedTrips: []realtime.Train{{RouteID: "A", EntityID: "two"}},
	}}}, at, time.Minute, time.Hour))
	var response positionsResponse
	r := get(h, "/api/v1/trains?route_id=A&view=positions")
	decode(t, r, &response)
	if r.Code != http.StatusOK || len(response.Trains) != 2 || value(response.AsOf) != at.Format(time.RFC3339) || response.ScheduleVersion != nil {
		t.Fatalf("captured missing-schedule response = %d/%s", r.Code, r.Body.String())
	}
	for _, train := range response.Trains {
		if train.Latitude != nil || train.Longitude != nil || train.NoPositionReason == nil || train.SourceRecordID == nil || train.TripInstanceID != nil {
			t.Fatalf("unprojectable record = %+v", train)
		}
	}
	if *response.Trains[0].SourceRecordID == *response.Trains[1].SourceRecordID {
		t.Fatal("collapsed source record identity")
	}
}

func TestGeometryAndPositionsUseOneVersionDuringPublication(t *testing.T) {
	at := time.Date(2026, 9, 13, 4, 10, 0, 0, time.UTC)
	first, second := geometryVersion(t, at, "a", -73.9), geometryVersion(t, at, "b", -73.8)
	results := &reconcile.Store{}
	sources := []reconcile.Source{{Name: "ace", Routes: []string{"A"}, Snapshot: realtime.Snapshot{
		SourceTimestamp: at, ReceivedAt: at, Complete: true, NYCTVersion: "1.0", Trains: []realtime.Train{{
			TripID: "trip", EntityID: "update", RouteID: "A", ServiceDate: "20260913", VehicleMatch: realtime.VehicleUnmatched,
			Stops: []realtime.StopPrediction{
				{StopID: "platform", Departure: &realtime.StopEvent{Time: at.Add(-30 * time.Second)}},
				{StopID: "second", Arrival: &realtime.StopEvent{Time: at.Add(570 * time.Second)}},
			},
		}},
	}}}
	bundles := []*reconcile.Bundle{
		reconcile.Reconcile(first, sources, at, time.Minute, time.Hour),
		reconcile.Reconcile(second, sources, at, time.Minute, time.Hour),
	}
	results.Publish(bundles[0])
	clockCalls := 0
	h := NewHandler(nil, telemetry.New(), func() time.Time { clockCalls++; return at }, time.Minute, results)
	unknown := get(h, "/api/v1/routes/X/geometry")
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown geometry = %d/%s", unknown.Code, unknown.Body.String())
	}
	var done sync.WaitGroup
	done.Go(func() {
		for i := range 500 {
			results.Publish(bundles[i%2])
		}
	})
	for range 100 {
		var response geometryResponse
		r := get(h, "/api/v1/routes/A/geometry")
		decode(t, r, &response)
		if r.Code != http.StatusOK || len(response.Stations) != 2 || len(response.Shapes) != 1 || len(response.Patterns) != 1 || !response.ScheduleEligible {
			t.Fatalf("geometry = %d/%s", r.Code, r.Body.String())
		}
		lon := -73.9
		if value(response.ScheduleVersion) == second.Metadata().Version {
			lon = -73.8
		}
		if response.Stations[0].ID != "parent" || response.Stations[0].Geometry.Coordinates != [2]float64{lon, 40.8} || response.Shapes[0].Geometry.Coordinates[0] != [2]float64{lon, 40.8} || response.Patterns[0].Stops[0].StopID != "platform" || response.Patterns[0].Stops[0].StationID != "parent" {
			t.Fatalf("mixed geometry or wrong coordinate/stop identities: %s", r.Body.String())
		}
		var positions positionsResponse
		r = get(h, "/api/v1/trains?route_id=A&view=positions")
		decode(t, r, &positions)
		if r.Code != http.StatusOK || len(positions.Trains) != 1 || positions.Trains[0].Latitude == nil {
			t.Fatalf("missing concurrent position: %d/%s", r.Code, r.Body.String())
		}
		for _, train := range positions.Trains {
			if value(train.ScheduleVersion) != value(positions.ScheduleVersion) {
				t.Fatalf("mixed position version: %s", r.Body.String())
			}
		}
	}
	done.Wait()
	if clockCalls != 201 {
		t.Fatalf("clock captures = %d, want one per request", clockCalls)
	}
	at = at.Add(time.Hour + time.Second)
	var expired geometryResponse
	r := get(h, "/api/v1/routes/A/geometry")
	decode(t, r, &expired)
	if r.Code != http.StatusOK || expired.ScheduleEligible || expired.ScheduleVersion == nil {
		t.Fatalf("expired geometry = %d/%s", r.Code, r.Body.String())
	}
}

func geometryVersion(t *testing.T, at time.Time, hash string, longitude float64) *schedule.Version {
	t.Helper()
	dataset, err := schedule.NewDataset(schedule.Data{
		Agencies: []schedule.Agency{{ID: "mta", Name: "MTA", URL: "https://example.com", Timezone: schedule.Timezone}},
		Routes:   []schedule.Route{{ID: "A", AgencyID: "mta", ShortName: "A", Type: 1}},
		Stops: []schedule.Stop{
			{ID: "parent", Name: "First", Latitude: 40.8, Longitude: longitude, HasCoordinates: true, LocationType: 1},
			{ID: "platform", Name: "First platform", ParentID: "parent", Latitude: 40.8, Longitude: longitude, HasCoordinates: true},
			{ID: "second", Name: "Second", Latitude: 40.7, Longitude: -74, HasCoordinates: true},
		},
		Trips: []schedule.Trip{{ID: "trip", RouteID: "A", ServiceID: "daily", ShapeID: "shape", Direction: 1, Stops: []schedule.StopTime{
			{StopID: "platform", Sequence: 1, Arrival: schedule.OptionalTime{Present: true, Seconds: 570}, Departure: schedule.OptionalTime{Present: true, Seconds: 570}},
			{StopID: "second", Sequence: 2, Arrival: schedule.OptionalTime{Present: true, Seconds: 1170}, Departure: schedule.OptionalTime{Present: true, Seconds: 1170}},
		}}},
		Calendars: []schedule.Calendar{{ServiceID: "daily", Start: "20260101", End: "20261231", Days: [7]bool{true, true, true, true, true, true, true}}},
		Shapes: []schedule.Shape{{ID: "shape", Points: []schedule.ShapePoint{
			{Sequence: 1, Latitude: 40.8, Longitude: longitude}, {Sequence: 2, Latitude: 40.8, Longitude: -74}, {Sequence: 3, Latitude: 40.7, Longitude: -74},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	version, err := schedule.NewVersion(dataset, schedule.Metadata{Source: schedule.Supplemented, ParserSchema: "2", ArchiveSHA256: strings.Repeat(hash, 64), ValidatedAt: at, PublishedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	return version
}

func TestPositionWarningsAreAlwaysArraysAndRemainSeparateFromFailures(t *testing.T) {
	at := time.Date(2026, 9, 13, 4, 10, 0, 0, time.UTC)
	v := geometryVersion(t, at, "a", -73.9)
	sequence := uint32(2)
	trip := realtime.Train{TripID: "trip", RouteID: "A", ServiceDate: "20260913", StartTime: "00:10:00",
		Stops: []realtime.StopPrediction{{StopID: "platform", Sequence: &sequence}}}
	results := &reconcile.Store{}
	sources := []reconcile.Source{{Name: "ace", Routes: []string{"A"}, Snapshot: realtime.Snapshot{SourceTimestamp: at, ReceivedAt: at, Complete: true, NYCTVersion: "1.0", Trains: []realtime.Train{trip}}}}
	results.Publish(reconcile.Reconcile(v, sources, at, time.Minute, time.Hour))
	h := NewHandler(nil, nil, func() time.Time { return at }, time.Minute, results)
	for _, variant := range []string{"missing timing", "authorized simulation", "clean replacement", "absent run"} {
		if variant == "authorized simulation" {
			at = at.Add(61 * time.Second)
		}
		if variant == "clean replacement" {
			sources[0].Snapshot.Trains[0].StartTime, sources[0].Snapshot.Trains[0].Stops = "", nil
			results.Publish(reconcile.Reconcile(v, sources, at, time.Minute, time.Hour))
		}
		if variant == "absent run" {
			sources[0].Snapshot.Trains = nil
			results.Publish(reconcile.Reconcile(v, sources, at, time.Minute, time.Hour))
		}
		response := get(h, "/api/v1/trains?route_id=A&view=positions")
		var body positionsResponse
		decode(t, response, &body)
		if response.Code != 200 || len(body.Trains) != 1 {
			t.Fatalf("%s: %s", variant, response.Body.String())
		}
		p := body.Trains[0]
		warnings := []string{}
		if variant == "missing timing" || variant == "authorized simulation" {
			warnings = []string{"start_time_conflict", "stop_id_sequence_conflict"}
		}
		if p.ReconciliationWarnings == nil || !slices.Equal(p.ReconciliationWarnings, warnings) {
			t.Fatalf("%s: %+v", variant, p)
		}
		if variant == "missing timing" {
			if p.Latitude != nil || value(p.NoPositionReason) != "insufficient_timing" || p.ReconciliationOutcome != reconcile.Matched || p.ReconciliationReason != "unique_identity" {
				t.Fatalf("%+v", p)
			}
		} else if p.Latitude == nil || p.PositionBasis != realtime.PositionBasisScheduleFallback {
			t.Fatalf("%s: %+v", variant, p)
		}
	}
}

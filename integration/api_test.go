package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"hol-up-prototype/internal/gen/gtfs"
	"hol-up-prototype/internal/httpapi"
	"hol-up-prototype/internal/ingest"
	"hol-up-prototype/internal/realtime"
	"hol-up-prototype/internal/telemetry"
)

func TestProtobufFeedToTrainsAPI(t *testing.T) {
	now := time.Date(2026, time.September, 2, 17, 0, 0, 0, time.UTC)
	aceTime := now
	nqrwTime := now.Add(30 * time.Second)
	ace := serveFeed(t, trainFeed(t, aceTime, "A", "090000_A..N55R", "A02N"))
	defer ace.Close()
	nqrw := serveFeed(t, trainFeed(t, nqrwTime, "N", "090030_N..N55R", "R16N"))
	defer nqrw.Close()

	aceStore, nqrwStore := realtime.NewStore(), realtime.NewStore()
	if err := ingest.NewLoader(ace.Client(), aceStore, "fixture", ace.URL, "", 30*time.Second, func() time.Time { return now }).Load(t.Context()); err != nil {
		t.Fatalf("load ACE: %v", err)
	}
	if err := ingest.NewLoader(nqrw.Client(), nqrwStore, "fixture", nqrw.URL, "", 30*time.Second, func() time.Time { return now }).Load(t.Context()); err != nil {
		t.Fatalf("load N/Q/R/W: %v", err)
	}
	sources := []httpapi.Source{
		{Name: "ace", Routes: []string{"A", "C", "E"}, Store: aceStore},
		{Name: "nqrw", Routes: []string{"N", "Q", "R", "W"}, Store: nqrwStore},
	}
	handler := httpapi.NewHandler(sources, telemetry.New(), func() time.Time { return nqrwTime }, time.Minute, nil)
	recorder := request(handler, "/api/v1/trains?route_id=A")

	if recorder.Code != http.StatusOK {
		t.Fatalf("trains status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	for _, want := range []string{
		`"trip_id":"090000_A..N55R"`,
		`"direction":"northbound"`,
		`"next_stop_id":"A02N"`,
		`"predicted_at":"2026-09-02T17:01:00Z"`,
		`"position_basis":"realtime_prediction"`,
		`"movement_state":"inferred_moving"`,
		`"freshness":"fresh"`,
	} {
		if !strings.Contains(recorder.Body.String(), want) {
			t.Errorf("trains body = %s, missing %s", recorder.Body.String(), want)
		}
	}
	nqrwRecorder := request(handler, "/api/v1/trains?route_id=N")
	if nqrwRecorder.Code != http.StatusOK || !strings.Contains(nqrwRecorder.Body.String(), `"trip_id":"090030_N..N55R"`) {
		t.Fatalf("N trains status/body = %d/%s", nqrwRecorder.Code, nqrwRecorder.Body.String())
	}

	staleHandler := httpapi.NewHandler(sources, telemetry.New(), func() time.Time { return now.Add(61 * time.Second) }, time.Minute, nil)
	if got := request(staleHandler, "/readyz"); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"coverage":"complete"`) {
		t.Fatalf("mixed-source readiness = %d/%s, want complete 200", got.Code, got.Body.String())
	}
	staleN := request(staleHandler, "/api/v1/trains?route_id=N")
	var response struct {
		Freshness realtime.Freshness `json:"freshness"`
		Trains    []struct {
			Freshness realtime.Freshness `json:"freshness"`
		} `json:"trains"`
	}
	if err := json.Unmarshal(staleN.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if staleN.Code != http.StatusOK || response.Freshness != realtime.FreshnessFresh || len(response.Trains) != 1 || response.Trains[0].Freshness != realtime.FreshnessFresh {
		t.Fatalf("route-specific N response = %d/%#v", staleN.Code, response)
	}
	staleA := request(staleHandler, "/api/v1/trains?route_id=A")
	if staleA.Code != http.StatusOK || !strings.Contains(staleA.Body.String(), `"freshness":"stale"`) {
		t.Fatalf("route-specific A response = %d/%s", staleA.Code, staleA.Body.String())
	}
}

func trainFeed(t *testing.T, sourceTime time.Time, routeID, tripID, stopID string) []byte {
	t.Helper()
	trip := &gtfs.TripDescriptor{TripId: proto.String(tripID), RouteId: proto.String(routeID), StartDate: proto.String("20260902"), StartTime: proto.String("15:00:00")}
	wire, err := proto.Marshal(&gtfs.FeedMessage{
		Header: &gtfs.FeedHeader{GtfsRealtimeVersion: proto.String("1.0"), Timestamp: proto.Uint64(uint64(sourceTime.Unix()))},
		Entity: []*gtfs.FeedEntity{
			{Id: proto.String("update-" + routeID), TripUpdate: &gtfs.TripUpdate{
				Trip: trip,
				StopTimeUpdate: []*gtfs.TripUpdate_StopTimeUpdate{{
					StopId: proto.String(stopID),
					Arrival: &gtfs.TripUpdate_StopTimeEvent{
						Time: proto.Int64(sourceTime.Add(time.Minute).Unix()),
					},
				}},
			}},
			{Id: proto.String("vehicle-" + routeID), Vehicle: &gtfs.VehiclePosition{Trip: trip, CurrentStatus: gtfs.VehiclePosition_IN_TRANSIT_TO.Enum(), Timestamp: proto.Uint64(uint64(sourceTime.Unix())), StopId: proto.String(stopID)}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func serveFeed(t *testing.T, wire []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Errorf("feed method = %s, want GET", request.Method)
		}
		_, _ = response.Write(wire)
	}))
}

func request(handler http.Handler, target string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	return recorder
}

func TestRealtimeSemanticsFromFeedThroughAPI(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	for _, test := range []struct {
		name                                      string
		delay                                     *int32
		absolute                                  *int64
		noData                                    bool
		tripTimestamp, vehicleTimestamp           *uint64
		wantDelay, wantTime                       any
		tripFreshness, vehicleFreshness, movement string
	}{
		{"omitted", nil, nil, false, nil, nil, nil, nil, "unavailable", "unavailable", "unknown"},
		{"zero", proto.Int32(0), nil, false, proto.Uint64(1000), proto.Uint64(1000), float64(0), nil, "fresh", "fresh", "inferred_moving"},
		{"absolute only and old vehicle", nil, proto.Int64(1060), false, proto.Uint64(1000), proto.Uint64(900), nil, "1970-01-01T00:17:40Z", "fresh", "stale", "unknown"},
		{"no data suppresses trip delay", nil, nil, true, proto.Uint64(1000), nil, nil, nil, "fresh", "unavailable", "unknown"},
		{"old prediction fresh vehicle", proto.Int32(30), proto.Int64(1060), false, proto.Uint64(900), proto.Uint64(1000), float64(30), "1970-01-01T00:17:40Z", "stale", "fresh", "inferred_moving"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var feed gtfs.FeedMessage
			if err := proto.Unmarshal(trainFeed(t, now, "A", "trip", "A01N"), &feed); err != nil {
				t.Fatal(err)
			}
			update := feed.Entity[0].TripUpdate
			update.Timestamp = test.tripTimestamp
			update.StopTimeUpdate[0].Arrival = &gtfs.TripUpdate_StopTimeEvent{Time: test.absolute, Delay: test.delay}
			if test.noData {
				update.Delay = proto.Int32(99)
				update.StopTimeUpdate[0].ScheduleRelationship = gtfs.TripUpdate_StopTimeUpdate_NO_DATA.Enum()
			}
			feed.Entity[1].Vehicle.Timestamp = test.vehicleTimestamp
			wire, err := proto.Marshal(&feed)
			if err != nil {
				t.Fatal(err)
			}
			server := serveFeed(t, wire)
			defer server.Close()
			store := realtime.NewStore()
			if err := ingest.NewLoader(server.Client(), store, "ace", server.URL, "", 0, func() time.Time { return now }).Load(t.Context()); err != nil {
				t.Fatal(err)
			}
			handler := httpapi.NewHandler([]httpapi.Source{{Name: "ace", Routes: []string{"A"}, Store: store}}, telemetry.New(), func() time.Time { return now }, time.Minute, nil)
			recorder := request(handler, "/api/v1/trains?route_id=A")
			var body struct {
				Trains []map[string]any `json:"trains"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != http.StatusOK || len(body.Trains) != 1 {
				t.Fatalf("response = %d/%s", recorder.Code, recorder.Body.String())
			}
			for field, want := range map[string]any{
				"prediction_delay_seconds": test.wantDelay, "predicted_at": test.wantTime,
				"trip_update_freshness": test.tripFreshness, "vehicle_freshness": test.vehicleFreshness,
				"movement_state": test.movement, "freshness": "fresh", "source_name": "ace",
				"received_at": "1970-01-01T00:16:40Z", "source_timestamp": "1970-01-01T00:16:40Z",
				"start_time": "15:00:00", "vehicle_entity_id": "vehicle-A", "vehicle_match": "matched",
			} {
				got, present := body.Trains[0][field]
				if !present || got != want {
					t.Errorf("%s = %v (present %t), want %v", field, got, present, want)
				}
			}
			for field, value := range map[string]*uint64{"trip_update_timestamp": test.tripTimestamp, "vehicle_timestamp": test.vehicleTimestamp} {
				var want any
				if value != nil {
					want = time.Unix(int64(*value), 0).UTC().Format(time.RFC3339)
				}
				if got, exists := body.Trains[0][field]; !exists || got != want {
					t.Errorf("%s = %v, want %v", field, got, want)
				}
			}
		})
	}
}

func TestCancellationsRemainNormalizedWithoutAppearingAsActiveTrains(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	var feed gtfs.FeedMessage
	if err := proto.Unmarshal(trainFeed(t, now, "S", "canceled-trip", "A01N"), &feed); err != nil {
		t.Fatal(err)
	}
	feed.Entity = feed.Entity[:1]
	feed.Entity[0].TripUpdate.Trip.ScheduleRelationship = gtfs.TripDescriptor_CANCELED.Enum()
	feed.Entity[0].TripUpdate.StopTimeUpdate = nil
	deleted := proto.Clone(feed.Entity[0]).(*gtfs.FeedEntity)
	deleted.Id = proto.String("deleted")
	deleted.TripUpdate.Trip.TripId = proto.String("deleted-trip")
	deleted.TripUpdate.Trip.ScheduleRelationship = gtfs.TripDescriptor_DELETED.Enum()
	skipped := proto.Clone(feed.Entity[0]).(*gtfs.FeedEntity)
	skipped.Id = proto.String("skipped")
	skipped.TripUpdate.Trip.TripId = proto.String("skipped-trip")
	skipped.TripUpdate.Trip.ScheduleRelationship = gtfs.TripDescriptor_SCHEDULED.Enum()
	skipped.TripUpdate.StopTimeUpdate = []*gtfs.TripUpdate_StopTimeUpdate{{StopId: proto.String("A01N"), ScheduleRelationship: gtfs.TripUpdate_StopTimeUpdate_SKIPPED.Enum()}}
	routeless := proto.Clone(deleted).(*gtfs.FeedEntity)
	routeless.Id = proto.String("routeless-cancellation")
	routeless.TripUpdate.Trip.RouteId = nil
	feed.Entity = append(feed.Entity, deleted, skipped, routeless)
	wire, err := proto.Marshal(&feed)
	if err != nil {
		t.Fatal(err)
	}
	server := serveFeed(t, wire)
	defer server.Close()
	store := realtime.NewStore()
	if err := ingest.NewLoader(server.Client(), store, "ace", server.URL, "", 0, func() time.Time { return now }).Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot := store.Load(now, time.Minute)
	if len(snapshot.Trains) != 4 {
		t.Fatalf("retained records = %+v", snapshot.Trains)
	}
	for _, trip := range snapshot.Trains {
		if trip.SourceName != "ace" || !trip.SourceTimestamp.Equal(now) || !trip.ReceivedAt.Equal(now) {
			t.Fatalf("missing provenance: %+v", trip)
		}
	}
	if _, _, active := store.Status(now, time.Minute); active != 0 {
		t.Fatalf("active count = %d", active)
	}
	handler := httpapi.NewHandler([]httpapi.Source{{Name: "ace", Routes: []string{"A"}, Store: store}, {Name: "g", Routes: []string{"G"}, Store: realtime.NewStore()}}, telemetry.New(), func() time.Time { return now }, time.Minute, nil)
	recorder := request(handler, "/api/v1/trains?route_id=S")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"trains":[]`) || !strings.Contains(recorder.Body.String(), `"coverage":"partial"`) {
		t.Fatalf("cancellation-only observed route = %d/%s", recorder.Code, recorder.Body.String())
	}
	if got := request(handler, "/api/v1/routes"); !strings.Contains(got.Body.String(), `"routes":["S"]`) {
		t.Fatalf("lost observed route: %s", got.Body.String())
	}
	if got := request(handler, "/metrics"); !strings.Contains(got.Body.String(), "holup_realtime_trains 0\n") {
		t.Fatalf("cancellations inflated metrics: %s", got.Body.String())
	}
	// Full replacement clears cancellations; an invalid replacement retains them.
	if err := store.Replace(realtime.Snapshot{}); err == nil {
		t.Fatal("invalid replacement accepted")
	}
	if len(store.Load(now, time.Minute).Trains) != 4 {
		t.Fatal("failed replacement lost cancellations")
	}
	if err := store.Replace(realtime.Snapshot{SourceTimestamp: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if len(store.Load(now.Add(time.Second), time.Minute).Trains) != 0 {
		t.Fatal("full replacement retained old cancellations")
	}
}

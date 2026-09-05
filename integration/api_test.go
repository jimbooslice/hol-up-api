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
	if err := ingest.NewLoader(ace.Client(), aceStore, ace.URL, "").Load(t.Context()); err != nil {
		t.Fatalf("load ACE: %v", err)
	}
	if err := ingest.NewLoader(nqrw.Client(), nqrwStore, nqrw.URL, "").Load(t.Context()); err != nil {
		t.Fatalf("load N/Q/R/W: %v", err)
	}
	combined, ok := realtime.Combine(aceStore.Load(now, 0), nqrwStore.Load(now, 0))
	if !ok {
		t.Fatal("Combine() did not publish two loaded feeds")
	}
	store := realtime.NewStore()
	if err := store.Replace(combined); err != nil {
		t.Fatal(err)
	}

	handler := httpapi.NewHandler(store, telemetry.New(), func() time.Time { return nqrwTime }, time.Minute)
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

	staleHandler := httpapi.NewHandler(store, telemetry.New(), func() time.Time { return now.Add(61 * time.Second) }, time.Minute)
	if got := request(staleHandler, "/readyz"); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("stale readiness status = %d, want 503", got.Code)
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
	if staleN.Code != http.StatusOK || response.Freshness != realtime.FreshnessStale || len(response.Trains) != 1 || response.Trains[0].Freshness != realtime.FreshnessFresh {
		t.Fatalf("stale-source response = %d/%#v", staleN.Code, response)
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

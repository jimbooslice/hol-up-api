package integration

import (
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
)

func TestProtobufFeedToTrainsAPI(t *testing.T) {
	now := time.Date(2026, time.September, 2, 17, 0, 0, 0, time.UTC)
	trip := &gtfs.TripDescriptor{
		TripId:    proto.String("090000_A..N55R"),
		RouteId:   proto.String("A"),
		StartDate: proto.String("20260902"),
		StartTime: proto.String("15:00:00"),
	}
	wire, err := proto.Marshal(&gtfs.FeedMessage{
		Header: &gtfs.FeedHeader{GtfsRealtimeVersion: proto.String("1.0"), Timestamp: proto.Uint64(uint64(now.Unix()))},
		Entity: []*gtfs.FeedEntity{
			{Id: proto.String("update"), TripUpdate: &gtfs.TripUpdate{
				Trip: trip,
				StopTimeUpdate: []*gtfs.TripUpdate_StopTimeUpdate{{
					StopId:  proto.String("A02N"),
					Arrival: &gtfs.TripUpdate_StopTimeEvent{Time: proto.Int64(now.Add(time.Minute).Unix())},
				}},
			}},
			{Id: proto.String("vehicle"), Vehicle: &gtfs.VehiclePosition{
				Trip:          trip,
				CurrentStatus: gtfs.VehiclePosition_IN_TRANSIT_TO.Enum(),
				Timestamp:     proto.Uint64(uint64(now.Unix())),
				StopId:        proto.String("A02N"),
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	source := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Errorf("feed method = %s, want GET", request.Method)
		}
		_, _ = response.Write(wire)
	}))
	defer source.Close()

	store := realtime.NewStore()
	if err := ingest.NewLoader(source.Client(), store, source.URL, "A").Load(t.Context()); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	recorder := httptest.NewRecorder()
	httpapi.NewHandler(store, func() time.Time { return now }, time.Minute).ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/api/v1/trains?route_id=A", nil),
	)

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
}

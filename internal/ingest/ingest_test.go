package ingest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"hol-up-prototype/internal/gen/gtfs"
	"hol-up-prototype/internal/realtime"
)

func TestDecodePairsTripAndVehicle(t *testing.T) {
	sourceTime := time.Date(2026, time.September, 2, 17, 0, 0, 0, time.UTC)
	trip := tripDescriptor("trip-a", "A", "20260902", "13:00:00")
	proto.SetExtension(trip, gtfs.E_NyctTripDescriptor, &gtfs.NyctTripDescriptor{
		Direction: gtfs.NyctTripDescriptor_SOUTH.Enum(),
	})
	feed := &gtfs.FeedMessage{
		Header: &gtfs.FeedHeader{
			GtfsRealtimeVersion: proto.String("1.0"),
			Timestamp:           proto.Uint64(uint64(sourceTime.Unix())),
		},
		Entity: []*gtfs.FeedEntity{
			{
				Id: proto.String("trip-entity"),
				TripUpdate: &gtfs.TripUpdate{
					Trip: trip,
					StopTimeUpdate: []*gtfs.TripUpdate_StopTimeUpdate{
						{StopId: proto.String("A10S"), ScheduleRelationship: gtfs.TripUpdate_StopTimeUpdate_SKIPPED.Enum()},
						{StopId: proto.String("A11S"), Arrival: &gtfs.TripUpdate_StopTimeEvent{Time: proto.Int64(sourceTime.Add(2 * time.Minute).Unix()), Delay: proto.Int32(45)}},
					},
				},
			},
			{
				Id: proto.String("vehicle-entity"),
				Vehicle: &gtfs.VehiclePosition{
					Trip:                tripDescriptor("trip-a", "A", "20260902", "13:00:00"),
					CurrentStopSequence: proto.Uint32(12),
					CurrentStatus:       gtfs.VehiclePosition_STOPPED_AT.Enum(),
					Timestamp:           proto.Uint64(uint64(sourceTime.Unix())),
					StopId:              proto.String("A11S"),
				},
			},
			{Id: proto.String("other-route"), TripUpdate: &gtfs.TripUpdate{Trip: tripDescriptor("trip-c", "C", "20260902", "13:00:00")}},
		},
	}

	wire, err := proto.Marshal(feed)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := Decode(wire, "A")
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if len(snapshot.Trains) != 1 {
		t.Fatalf("len(Trains) = %d, want 1", len(snapshot.Trains))
	}
	train := snapshot.Trains[0]
	if train.NextStopID != "A11S" || train.NextStopSequence != 12 {
		t.Fatalf("next stop = %q/%d, want A11S/12", train.NextStopID, train.NextStopSequence)
	}
	if train.MovementState != realtime.MovementStateReportedStopped {
		t.Fatalf("movement state = %q, want reported_stopped", train.MovementState)
	}
	if train.DirectionID != nil || train.Direction != realtime.DirectionSouthbound {
		t.Fatalf("direction = %v/%q, want nil/southbound", train.DirectionID, train.Direction)
	}
	if train.PredictionDelaySeconds != 45 || !train.PredictedAt.Equal(sourceTime.Add(2*time.Minute)) {
		t.Fatalf("prediction = %s/%d, want +2m/45", train.PredictedAt, train.PredictionDelaySeconds)
	}
}

func TestDecodePairsPartialVehicleDescriptorAndHonorsStoppedStatus(t *testing.T) {
	sourceTime := time.Date(2026, time.September, 2, 17, 0, 0, 0, time.UTC)
	feed := &gtfs.FeedMessage{
		Header: &gtfs.FeedHeader{GtfsRealtimeVersion: proto.String("1.0"), Timestamp: proto.Uint64(uint64(sourceTime.Unix()))},
		Entity: []*gtfs.FeedEntity{
			{Id: proto.String("update"), TripUpdate: &gtfs.TripUpdate{
				Trip:           tripDescriptor("trip-a", "A", "20260902", "13:00:00"),
				StopTimeUpdate: []*gtfs.TripUpdate_StopTimeUpdate{{StopId: proto.String("A01N")}},
			}},
			{Id: proto.String("vehicle"), Vehicle: &gtfs.VehiclePosition{
				Trip:                &gtfs.TripDescriptor{TripId: proto.String("trip-a")},
				CurrentStopSequence: proto.Uint32(7),
				CurrentStatus:       gtfs.VehiclePosition_STOPPED_AT.Enum(),
				StopId:              proto.String("A01N"),
			}},
		},
	}
	snapshot, err := Decode(mustMarshal(t, feed), "A")
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.Trains[0]; got.MovementState != realtime.MovementStateReportedStopped || got.NextStopSequence != 7 {
		t.Fatalf("train = %#v, want partial vehicle match and reported stop", got)
	}
}

func TestDecodeWithoutRouteFilterRetainsEveryRoute(t *testing.T) {
	sourceTime := time.Date(2026, time.September, 2, 17, 0, 0, 0, time.UTC)
	feed := &gtfs.FeedMessage{
		Header: &gtfs.FeedHeader{GtfsRealtimeVersion: proto.String("1.0"), Timestamp: proto.Uint64(uint64(sourceTime.Unix()))},
		Entity: []*gtfs.FeedEntity{
			tripUpdateEntity("a", "trip-a", "A", "A01N"),
			tripUpdateEntity("c", "trip-c", "C", "A02S"),
		},
	}

	all, err := Decode(mustMarshal(t, feed), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Trains) != 2 || all.Trains[0].RouteID != "A" || all.Trains[1].RouteID != "C" {
		t.Fatalf("unfiltered trains = %#v, want A and C", all.Trains)
	}

	filtered, err := Decode(mustMarshal(t, feed), "C")
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Trains) != 1 || filtered.Trains[0].RouteID != "C" {
		t.Fatalf("filtered trains = %#v, want only C", filtered.Trains)
	}
}

func TestDecodeLeavesAmbiguousVehicleUnmatched(t *testing.T) {
	sourceTime := time.Date(2026, time.September, 2, 17, 0, 0, 0, time.UTC)
	feed := &gtfs.FeedMessage{
		Header: &gtfs.FeedHeader{GtfsRealtimeVersion: proto.String("1.0"), Timestamp: proto.Uint64(uint64(sourceTime.Unix()))},
		Entity: []*gtfs.FeedEntity{
			{Id: proto.String("update"), TripUpdate: &gtfs.TripUpdate{
				Trip:           &gtfs.TripDescriptor{TripId: proto.String("trip-a"), RouteId: proto.String("A")},
				StopTimeUpdate: []*gtfs.TripUpdate_StopTimeUpdate{{StopId: proto.String("A01N")}},
			}},
			{Id: proto.String("vehicle-one"), Vehicle: &gtfs.VehiclePosition{Trip: tripDescriptor("trip-a", "A", "20260902", "13:00:00"), Timestamp: proto.Uint64(1)}},
			{Id: proto.String("vehicle-two"), Vehicle: &gtfs.VehiclePosition{Trip: tripDescriptor("trip-a", "A", "20260903", "13:00:00"), Timestamp: proto.Uint64(1)}},
		},
	}
	snapshot, err := Decode(mustMarshal(t, feed), "A")
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.Trains[0].MovementState; got != realtime.MovementStateUnknown {
		t.Fatalf("movement state = %q, want unknown for ambiguous vehicles", got)
	}
}

func TestDecodeRejectsDifferentialFeed(t *testing.T) {
	feed := &gtfs.FeedMessage{Header: &gtfs.FeedHeader{
		GtfsRealtimeVersion: proto.String("2.0"),
		Timestamp:           proto.Uint64(1),
		Incrementality:      gtfs.FeedHeader_DIFFERENTIAL.Enum(),
	}}
	wire, err := proto.Marshal(feed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(wire, "A"); !errors.Is(err, ErrDifferentialFeed) {
		t.Fatalf("Decode() error = %v, want ErrDifferentialFeed", err)
	}
}

func TestPredictionUsesAbsoluteDepartureWhenArrivalHasOnlyDelay(t *testing.T) {
	want := time.Date(2026, time.September, 2, 17, 1, 0, 0, time.UTC)
	stop := &gtfs.TripUpdate_StopTimeUpdate{
		Arrival:   &gtfs.TripUpdate_StopTimeEvent{Delay: proto.Int32(30)},
		Departure: &gtfs.TripUpdate_StopTimeEvent{Time: proto.Int64(want.Unix()), Delay: proto.Int32(45)},
	}
	got, delay := prediction(stop, &gtfs.TripUpdate{})
	if !got.Equal(want) || delay != 45 {
		t.Fatalf("prediction() = %s/%d, want %s/45", got, delay, want)
	}
}

func TestLoaderKeepsLastKnownGoodOnBadResponse(t *testing.T) {
	sourceTime := time.Date(2026, time.September, 2, 17, 0, 0, 0, time.UTC)
	valid := mustMarshal(t, &gtfs.FeedMessage{
		Header: &gtfs.FeedHeader{GtfsRealtimeVersion: proto.String("1.0"), Timestamp: proto.Uint64(uint64(sourceTime.Unix()))},
		Entity: []*gtfs.FeedEntity{{Id: proto.String("trip"), TripUpdate: &gtfs.TripUpdate{
			Trip:           tripDescriptor("trip-a", "A", "20260902", "13:00:00"),
			StopTimeUpdate: []*gtfs.TripUpdate_StopTimeUpdate{{StopId: proto.String("A01N")}},
		}}},
	})
	responses := [][]byte{valid, []byte("not protobuf")}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write(responses[0])
		responses = responses[1:]
	}))
	defer server.Close()

	store := realtime.NewStore()
	loader := NewLoader(server.Client(), store, server.URL, "A")
	if err := loader.Load(context.Background()); err != nil {
		t.Fatalf("first Load() error = %v", err)
	}
	if err := loader.Load(context.Background()); err == nil {
		t.Fatal("second Load() error = nil, want protobuf error")
	}
	got := store.Load(sourceTime, time.Minute)
	if got.SourceTimestamp != sourceTime || len(got.Trains) != 1 {
		t.Fatalf("last known good snapshot = %#v", got)
	}
}

func TestLoaderRejectsOversizeFeed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write(make([]byte, MaxFeedBytes+1))
	}))
	defer server.Close()

	err := NewLoader(server.Client(), realtime.NewStore(), server.URL, "A").Load(context.Background())
	if !errors.Is(err, ErrFeedTooLarge) {
		t.Fatalf("Load() error = %v, want ErrFeedTooLarge", err)
	}
}

func tripDescriptor(tripID, routeID, startDate, startTime string) *gtfs.TripDescriptor {
	return &gtfs.TripDescriptor{
		TripId:    proto.String(tripID),
		RouteId:   proto.String(routeID),
		StartDate: proto.String(startDate),
		StartTime: proto.String(startTime),
	}
}

func tripUpdateEntity(entityID, tripID, routeID, stopID string) *gtfs.FeedEntity {
	return &gtfs.FeedEntity{Id: proto.String(entityID), TripUpdate: &gtfs.TripUpdate{
		Trip:           tripDescriptor(tripID, routeID, "20260902", "13:00:00"),
		StopTimeUpdate: []*gtfs.TripUpdate_StopTimeUpdate{{StopId: proto.String(stopID)}},
	}}
}

func mustMarshal(t *testing.T, message proto.Message) []byte {
	t.Helper()
	wire, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

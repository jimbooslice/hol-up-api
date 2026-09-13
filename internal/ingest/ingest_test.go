package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	if (train.PredictionDelaySeconds == nil || *train.PredictionDelaySeconds != 45) || !train.PredictedAt.Equal(sourceTime.Add(2*time.Minute)) {
		t.Fatalf("prediction = %s/%v, want +2m/45", train.PredictedAt, train.PredictionDelaySeconds)
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
	got, delay := (realtime.Train{}).Prediction(&stopPredictions([]*gtfs.TripUpdate_StopTimeUpdate{stop})[0])
	if !got.Equal(want) || (delay == nil || *delay != 45) {
		t.Fatalf("prediction() = %s/%v, want %s/45", got, delay, want)
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
	loader := NewLoader(server.Client(), store, "fixture", server.URL, "A", 30*time.Second, func() time.Time { return sourceTime })
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

func TestLoaderFollowsValidRedirects(t *testing.T) {
	sourceTime := time.Date(2026, time.September, 2, 17, 0, 0, 0, time.UTC)
	valid := mustMarshal(t, &gtfs.FeedMessage{
		Header: &gtfs.FeedHeader{GtfsRealtimeVersion: proto.String("1.0"), Timestamp: proto.Uint64(uint64(sourceTime.Unix()))},
		Entity: []*gtfs.FeedEntity{tripUpdateEntity("trip", "trip-a", "A", "A01N")},
	})
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/relative":
			response.Header().Set("Location", "/feed")
			response.WriteHeader(http.StatusFound)
		case "/absolute":
			response.Header().Set("Location", server.URL+"/feed")
			response.WriteHeader(http.StatusFound)
		default:
			_, _ = response.Write(valid)
		}
	}))
	defer server.Close()

	for _, path := range []string{"/relative", "/absolute"} {
		t.Run(path[1:], func(t *testing.T) {
			store := realtime.NewStore()
			if err := NewLoader(server.Client(), store, "fixture", server.URL+path, "A", 30*time.Second, func() time.Time { return sourceTime }).Load(context.Background()); err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			got := store.Load(sourceTime, time.Minute)
			if len(got.Trains) != 1 || got.Trains[0].TripID != "trip-a" || !got.SourceTimestamp.Equal(sourceTime) {
				t.Fatalf("redirected snapshot = %#v", got)
			}
		})
	}
}

func TestLoaderReturnsTypedHTTPStatusWithoutResponseDetails(t *testing.T) {
	sourceTime := time.Unix(1, 0)
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			const marker = "response-secret"
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("X-Diagnostic", marker)
				response.WriteHeader(status)
				_, _ = response.Write([]byte(marker))
			}))
			defer server.Close()

			store := realtime.NewStore()
			err := NewLoader(server.Client(), store, "fixture", server.URL, "A", 30*time.Second, func() time.Time { return sourceTime }).Load(context.Background())
			var statusError HTTPStatusError
			if !errors.As(err, &statusError) || int(statusError) != status {
				t.Fatalf("Load() error = %v, want HTTPStatusError(%d)", err, status)
			}
			want := fmt.Sprintf("fetch GTFS-Realtime feed: unexpected HTTP status %d", status)
			if err.Error() != want {
				t.Errorf("Load() error = %q, want %q", err, want)
			}
			if strings.Contains(err.Error(), marker) {
				t.Errorf("response details leaked in %q", err)
			}
			if _, freshness, count := store.Status(time.Time{}, 0); freshness != realtime.FreshnessUnavailable || count != 0 {
				t.Errorf("initial failure published a snapshot: freshness=%q count=%d", freshness, count)
			}
		})
	}
}

func TestLoaderRejectsOversizeFeed(t *testing.T) {
	sourceTime := time.Unix(1, 0)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write(make([]byte, MaxFeedBytes+1))
	}))
	defer server.Close()

	err := NewLoader(server.Client(), realtime.NewStore(), "fixture", server.URL, "A", 30*time.Second, func() time.Time { return sourceTime }).Load(context.Background())
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

func TestLoaderFutureTimestampBoundary(t *testing.T) {
	base := time.Date(2026, time.September, 2, 17, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name       string
		source     time.Time
		receivedAt time.Time
		tolerance  time.Duration
		reject     bool
	}{
		{"past", base.Add(-time.Second), base, 30 * time.Second, false},
		{"present", base, base, 30 * time.Second, false},
		{"just below", base.Add(30 * time.Second), base.Add(time.Nanosecond), 30 * time.Second, false},
		{"exact boundary", base.Add(30 * time.Second), base, 30 * time.Second, false},
		{"just above", base.Add(30 * time.Second), base.Add(-time.Nanosecond), 30 * time.Second, true},
		{"custom boundary", base.Add(time.Second), base.Add(500 * time.Millisecond), 500 * time.Millisecond, false},
		{"custom rejection", base.Add(time.Second), base, 500 * time.Millisecond, true},
		{"strict present", base, base, 0, false},
		{"strict future", base.Add(time.Second), base, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire := mustMarshal(t, &gtfs.FeedMessage{
				Header: &gtfs.FeedHeader{GtfsRealtimeVersion: proto.String("1.0"), Timestamp: proto.Uint64(uint64(test.source.Unix()))},
				Entity: []*gtfs.FeedEntity{tripUpdateEntity("trip", "trip-a", "A", "A01N")},
			})
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				_, _ = response.Write(wire)
			}))
			defer server.Close()
			store := realtime.NewStore()
			calls := 0
			loader := NewLoader(server.Client(), store, "fixture", server.URL, "", test.tolerance, func() time.Time {
				calls++
				return test.receivedAt
			})
			err := loader.Load(t.Context())
			if calls != 1 {
				t.Fatalf("receipt clock called %d times, want once", calls)
			}
			if test.reject {
				var futureError *FutureTimestampError
				if !errors.As(err, &futureError) {
					t.Fatalf("Load() = %v, want FutureTimestampError", err)
				}
				if !futureError.SourceTimestamp.Equal(test.source) || !futureError.ReceivedAt.Equal(test.receivedAt) || futureError.MaxFutureSkew != test.tolerance {
					t.Errorf("rejection details = %+v", futureError)
				}
				if _, freshness, count := store.Status(test.receivedAt, time.Minute); freshness != realtime.FreshnessUnavailable || count != 0 {
					t.Fatal("rejected feed published a snapshot")
				}
			} else if err != nil {
				t.Fatal(err)
			} else if got := store.Load(test.receivedAt, time.Minute); !got.SourceTimestamp.Equal(test.source) || len(got.Trains) != 1 {
				t.Fatalf("accepted snapshot = %+v", got)
			}
		})
	}
}

func TestLoaderCapturesReceiptAfterBodyRead(t *testing.T) {
	base := time.Date(2026, time.September, 2, 17, 0, 0, 0, time.UTC)
	receivedAt := base.Add(time.Minute)
	now := base
	wire := mustMarshal(t, &gtfs.FeedMessage{Header: &gtfs.FeedHeader{
		GtfsRealtimeVersion: proto.String("1.0"), Timestamp: proto.Uint64(uint64(receivedAt.Unix())),
	}})
	reader := strings.NewReader(string(wire))
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(readFunc(func(p []byte) (int, error) {
			n, err := reader.Read(p)
			if err == io.EOF {
				now = receivedAt
			}
			return n, err
		}))}, nil
	})}
	store := realtime.NewStore()
	if err := NewLoader(client, store, "fixture", "http://fixture.test/feed", "", 0, func() time.Time { return now }).Load(t.Context()); err != nil {
		t.Fatalf("receipt captured before full body read: %v", err)
	}
	if timestamp, _, _ := store.Status(receivedAt, time.Minute); !timestamp.Equal(receivedAt) {
		t.Fatalf("published timestamp = %v", timestamp)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type readFunc func([]byte) (int, error)

func (f readFunc) Read(p []byte) (int, error) { return f(p) }

func TestLoaderAllowsFutureStopPredictions(t *testing.T) {
	base := time.Date(2026, time.September, 2, 17, 0, 0, 0, time.UTC)
	arrival := tripUpdateEntity("arrival", "arrival", "A", "A01N")
	departure := tripUpdateEntity("departure", "departure", "A", "A02N")
	predictionTime := base.Add(time.Hour)
	arrival.TripUpdate.StopTimeUpdate[0].Arrival = &gtfs.TripUpdate_StopTimeEvent{Time: proto.Int64(predictionTime.Unix())}
	departure.TripUpdate.StopTimeUpdate[0].Departure = &gtfs.TripUpdate_StopTimeEvent{Time: proto.Int64(predictionTime.Unix())}
	wire := mustMarshal(t, &gtfs.FeedMessage{
		Header: &gtfs.FeedHeader{GtfsRealtimeVersion: proto.String("1.0"), Timestamp: proto.Uint64(uint64(base.Unix()))},
		Entity: []*gtfs.FeedEntity{arrival, departure},
	})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write(wire)
	}))
	defer server.Close()
	store := realtime.NewStore()
	if err := NewLoader(server.Client(), store, "fixture", server.URL, "", 0, func() time.Time { return base }).Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot := store.Load(base, time.Minute)
	if len(snapshot.Trains) != 2 {
		t.Fatalf("trains = %+v", snapshot.Trains)
	}
	for _, train := range snapshot.Trains {
		if !train.PredictedAt.Equal(predictionTime) {
			t.Errorf("prediction changed: %+v", train)
		}
	}
}

func TestDecodeTimestampValidation(t *testing.T) {
	for _, test := range []struct {
		name      string
		timestamp *uint64
		wantError string
	}{
		{"missing", nil, realtime.ErrMissingSourceTimestamp.Error()},
		{"zero", proto.Uint64(0), realtime.ErrMissingSourceTimestamp.Error()},
		{"out of range", proto.Uint64(1 << 63), "GTFS-Realtime timestamp is out of range"},
		{"distant future remains decodable", proto.Uint64(4102444800), ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire := mustMarshal(t, &gtfs.FeedMessage{Header: &gtfs.FeedHeader{GtfsRealtimeVersion: proto.String("1.0"), Timestamp: test.timestamp}})
			_, err := Decode(wire, "")
			if test.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || err.Error() != test.wantError {
				t.Fatalf("Decode() = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestDecodeRetainsCompleteOrderedState(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	update := tripUpdateEntity("update", "trip", "A", "A01N")
	update.TripUpdate.Timestamp = proto.Uint64(900)
	update.TripUpdate.Delay = proto.Int32(50)
	update.TripUpdate.StopTimeUpdate = []*gtfs.TripUpdate_StopTimeUpdate{
		{StopSequence: proto.Uint32(3), StopId: proto.String("A03N"), Arrival: &gtfs.TripUpdate_StopTimeEvent{Time: proto.Int64(1200), Uncertainty: proto.Int32(0)}, Departure: &gtfs.TripUpdate_StopTimeEvent{Time: proto.Int64(1230), Delay: proto.Int32(0)}},
		{StopSequence: proto.Uint32(1), StopId: proto.String("A01N"), ScheduleRelationship: gtfs.TripUpdate_StopTimeUpdate_SKIPPED.Enum()},
		{StopSequence: proto.Uint32(2), StopId: proto.String("A02N"), ScheduleRelationship: gtfs.TripUpdate_StopTimeUpdate_NO_DATA.Enum()},
	}
	canceled := tripUpdateEntity("canceled", "canceled-trip", "A", "A04N")
	canceled.TripUpdate.Trip.ScheduleRelationship = gtfs.TripDescriptor_CANCELED.Enum()
	canceled.TripUpdate.StopTimeUpdate = nil
	deleted := tripUpdateEntity("deleted", "deleted-trip", "A", "A05N")
	deleted.TripUpdate.Trip.ScheduleRelationship = gtfs.TripDescriptor_DELETED.Enum()
	skipped := tripUpdateEntity("all-skipped", "skipped-trip", "A", "A06N")
	skipped.TripUpdate.StopTimeUpdate[0].ScheduleRelationship = gtfs.TripUpdate_StopTimeUpdate_SKIPPED.Enum()
	snapshot, err := Decode(mustMarshal(t, &gtfs.FeedMessage{
		Header: &gtfs.FeedHeader{GtfsRealtimeVersion: proto.String("2.0"), Timestamp: proto.Uint64(uint64(now.Unix()))},
		Entity: []*gtfs.FeedEntity{update, canceled, deleted, skipped},
	}), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Trains) != 4 {
		t.Fatalf("lost trip records: %+v", snapshot)
	}
	trip := snapshot.Trains[0]
	if len(trip.Stops) != 3 || trip.NextStopID != "A02N" || trip.NextStopSequence != 2 || !trip.PredictedAt.IsZero() || trip.PredictionDelaySeconds != nil || trip.TripUpdateTimestamp.Unix() != 900 || trip.StartTime != "13:00:00" {
		t.Fatalf("trip = %+v", trip)
	}
	for i, stop := range trip.Stops {
		if *stop.Sequence != uint32(i+1) {
			t.Fatalf("stop order = %+v", trip.Stops)
		}
	}
	last := trip.Stops[2]
	if last.Arrival.Time.Unix() != 1200 || last.Arrival.DelaySeconds != nil || last.Arrival.Uncertainty == nil || *last.Arrival.Uncertainty != 0 || last.Departure.Time.Unix() != 1230 || last.Departure.DelaySeconds == nil || *last.Departure.DelaySeconds != 0 {
		t.Fatalf("events lost presence: %+v / %+v", last.Arrival, last.Departure)
	}
	if !snapshot.Trains[1].Canceled() || !snapshot.Trains[2].Canceled() || snapshot.Trains[3].NextStopID != "" || len(snapshot.Trains[3].Stops) != 1 {
		t.Fatalf("inactive state lost: %+v", snapshot.Trains)
	}
}

func TestDecodeVehicleMatchingIsUniqueInBothDirections(t *testing.T) {
	for _, test := range []struct {
		name    string
		other   func(*gtfs.TripDescriptor)
		vehicle func(*gtfs.TripDescriptor)
		want    realtime.VehicleMatch
	}{
		{"different dates partial vehicle", func(d *gtfs.TripDescriptor) { d.StartDate = proto.String("20260903") }, func(d *gtfs.TripDescriptor) { d.StartDate = nil }, realtime.VehicleAmbiguous},
		{"different starts partial vehicle", func(d *gtfs.TripDescriptor) { d.StartTime = proto.String("14:00:00") }, func(d *gtfs.TripDescriptor) { d.StartTime = nil }, realtime.VehicleAmbiguous},
		{"different routes before filtering", func(d *gtfs.TripDescriptor) { d.RouteId = proto.String("C") }, func(d *gtfs.TripDescriptor) { d.RouteId = nil }, realtime.VehicleAmbiguous},
		{"different directions", func(d *gtfs.TripDescriptor) { d.DirectionId = proto.Uint32(1) }, func(d *gtfs.TripDescriptor) { d.DirectionId = nil }, realtime.VehicleAmbiguous},
		{"duplicate descriptors", func(d *gtfs.TripDescriptor) {}, func(d *gtfs.TripDescriptor) {}, realtime.VehicleAmbiguous},
		{"exact instance", func(d *gtfs.TripDescriptor) { d.StartTime = proto.String("14:00:00") }, func(d *gtfs.TripDescriptor) {}, realtime.VehicleMatched},
		{"conflicting route", func(d *gtfs.TripDescriptor) {}, func(d *gtfs.TripDescriptor) { d.RouteId = proto.String("N") }, realtime.VehicleUnmatched},
	} {
		t.Run(test.name, func(t *testing.T) {
			first := tripUpdateEntity("first", "trip", "A", "A01N")
			first.TripUpdate.Trip.DirectionId = proto.Uint32(0)
			other := proto.Clone(first).(*gtfs.FeedEntity)
			other.Id = proto.String("other")
			other.TripUpdate.StopTimeUpdate = nil // Still participates in reverse uniqueness.
			test.other(other.TripUpdate.Trip)
			vehicleTrip := proto.Clone(first.TripUpdate.Trip).(*gtfs.TripDescriptor)
			test.vehicle(vehicleTrip)
			snapshot, err := Decode(mustMarshal(t, &gtfs.FeedMessage{
				Header: &gtfs.FeedHeader{GtfsRealtimeVersion: proto.String("2.0"), Timestamp: proto.Uint64(1000)},
				Entity: []*gtfs.FeedEntity{first, other, {Id: proto.String("vehicle"), Vehicle: &gtfs.VehiclePosition{Trip: vehicleTrip, Timestamp: proto.Uint64(1000), CurrentStatus: gtfs.VehiclePosition_STOPPED_AT.Enum()}}},
			}), "A")
			if err != nil {
				t.Fatal(err)
			}
			got := snapshot.Trains[0]
			if got.VehicleMatch != test.want {
				t.Fatalf("match = %s, want %s", got.VehicleMatch, test.want)
			}
			if test.want != realtime.VehicleMatched && (got.MovementState != realtime.MovementStateUnknown || got.VehicleEntityID != "" || !got.VehicleTimestamp.IsZero()) {
				t.Fatalf("ambiguous observation attached: %+v", got)
			}
			if test.want == realtime.VehicleMatched && (got.VehicleEntityID != "vehicle" || got.VehicleTimestamp.Unix() != 1000 || got.MovementState != realtime.MovementStateReportedStopped) {
				t.Fatalf("observation not retained: %+v", got)
			}
		})
	}
}

func TestStopOrderWithoutSequencesAndEventPresence(t *testing.T) {
	stops := stopPredictions([]*gtfs.TripUpdate_StopTimeUpdate{
		{StopId: proto.String("A03N"), StopSequence: proto.Uint32(3)},
		{StopId: proto.String("A01N"), Arrival: &gtfs.TripUpdate_StopTimeEvent{Time: proto.Int64(0), ScheduledTime: proto.Int64(10)}},
		{StopSequence: proto.Uint32(2), Departure: &gtfs.TripUpdate_StopTimeEvent{}},
	})
	if len(stops) != 3 || stops[0].StopID != "A03N" || stops[1].Sequence != nil || stops[1].Arrival.Time.IsZero() || stops[1].Arrival.Time.Unix() != 0 || stops[1].Arrival.ScheduledTime.Unix() != 10 || stops[2].StopID != "" || stops[2].Arrival != nil || stops[2].Departure == nil || stops[2].Departure.DelaySeconds != nil {
		t.Fatalf("lost field presence/order: %+v", stops)
	}
	if !observationTimestamp(nil).IsZero() || !observationTimestamp(proto.Uint64(1<<63)).IsZero() {
		t.Fatal("invalid observation timestamp accepted")
	}
}

func TestReplacementMetadataAndUnidentifiedRoundTrip(t *testing.T) {
	header := &gtfs.FeedHeader{GtfsRealtimeVersion: proto.String("2.0"), Timestamp: proto.Uint64(1000)}
	proto.SetExtension(header, gtfs.E_NyctFeedHeader, &gtfs.NyctFeedHeader{NyctSubwayVersion: proto.String("1.0"), TripReplacementPeriod: []*gtfs.TripReplacementPeriod{
		{RouteId: proto.String("A"), ReplacementPeriod: &gtfs.TimeRange{Start: proto.Uint64(0), End: proto.Uint64(2000)}},
		{RouteId: proto.String("C"), ReplacementPeriod: &gtfs.TimeRange{End: proto.Uint64(1 << 63)}},
		{ReplacementPeriod: &gtfs.TimeRange{Start: proto.Uint64(10)}},
		{RouteId: proto.String("E")},
	}})
	identified := tripUpdateEntity("known", "trip", "A", "A01N")
	unidentified := tripUpdateEntity("unknown", "", "A", "A01S")
	unidentified.TripUpdate.StopTimeUpdate[0].Arrival = &gtfs.TripUpdate_StopTimeEvent{Delay: proto.Int32(0)}
	proto.SetExtension(unidentified.TripUpdate.Trip, gtfs.E_NyctTripDescriptor, &gtfs.NyctTripDescriptor{Direction: gtfs.NyctTripDescriptor_NORTH.Enum()})
	wire := mustMarshal(t, &gtfs.FeedMessage{Header: header, Entity: []*gtfs.FeedEntity{identified, unidentified}})
	snapshot, err := Decode(wire, "")
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Complete || snapshot.NYCTVersion != "1.0" || len(snapshot.Trains) != 1 || len(snapshot.UnidentifiedTrips) != 1 || len(snapshot.ReplacementPeriods) != 4 {
		t.Fatalf("%+v", snapshot)
	}
	periods := snapshot.ReplacementPeriods
	if periods[0].Start == nil || *periods[0].Start != 0 || periods[1].Start != nil || *periods[1].End != 1<<63 || periods[2].RouteID != "" || periods[2].End != nil || periods[3].Start != nil || periods[3].End != nil {
		t.Fatalf("lost malformed metadata or presence: %+v", periods)
	}
	if snapshot.Trains[0].NYCTDirection != nil || snapshot.Trains[0].Direction != realtime.DirectionNorthbound || snapshot.UnidentifiedTrips[0].NYCTDirection == nil || *snapshot.UnidentifiedTrips[0].NYCTDirection != realtime.DirectionNorthbound {
		t.Fatal("supplied direction lost/inferred")
	}
	filtered, err := Decode(wire, "A")
	if err != nil || filtered.Complete {
		t.Fatalf("filtered completeness: %+v/%v", filtered, err)
	}
	snapshot.SourceName = "ace"
	snapshot.ReceivedAt = time.Unix(1001, 0)
	store := realtime.NewStore()
	if err := store.Replace(snapshot); err != nil {
		t.Fatal(err)
	}
	*snapshot.ReplacementPeriods[0].Start = 99
	*snapshot.UnidentifiedTrips[0].Stops[0].Arrival.DelaySeconds = 99
	*snapshot.UnidentifiedTrips[0].NYCTDirection = realtime.DirectionSouthbound
	read := store.Load(time.Unix(1001, 0), time.Minute)
	*read.ReplacementPeriods[0].End = 99
	*read.UnidentifiedTrips[0].Stops[0].InputIndex = 99
	again := store.Load(time.Unix(1001, 0), time.Minute)
	if *again.ReplacementPeriods[0].Start != 0 || *again.ReplacementPeriods[0].End != 2000 || *again.UnidentifiedTrips[0].Stops[0].Arrival.DelaySeconds != 0 || *again.UnidentifiedTrips[0].Stops[0].InputIndex != 0 || *again.UnidentifiedTrips[0].NYCTDirection != realtime.DirectionNorthbound {
		t.Fatal("nested state aliased")
	}
	if _, _, count := store.Status(time.Unix(1001, 0), time.Minute); count != 1 {
		t.Fatal("unidentified record changed compact count")
	}
}

func TestUnknownIncrementalityCannotEstablishCompleteness(t *testing.T) {
	wire := mustMarshal(t, &gtfs.FeedMessage{Header: &gtfs.FeedHeader{GtfsRealtimeVersion: proto.String("2.0"), Timestamp: proto.Uint64(1000), Incrementality: gtfs.FeedHeader_Incrementality(99).Enum()}})
	snapshot, err := Decode(wire, "")
	if err != nil || snapshot.Complete {
		t.Fatalf("unknown incrementality: %+v/%v", snapshot, err)
	}
}

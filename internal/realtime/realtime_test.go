package realtime

import (
	"errors"
	"testing"
	"time"
)

func TestStoreReplaceMakesSnapshotImmutable(t *testing.T) {
	store := NewStore()
	sourceTime := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	direction := uint32(1)
	next := Snapshot{
		SourceTimestamp: sourceTime,
		Trains: []Train{{
			EntityID:    "vehicle-b",
			RouteID:     "A",
			TripID:      "trip-b",
			DirectionID: &direction,
		}},
	}
	if err := store.Replace(next); err != nil {
		t.Fatalf("Replace() error = %v", err)
	}

	next.Trains[0].TripID = "mutated-input"
	*next.Trains[0].DirectionID = 99
	loaded := store.Load(sourceTime, time.Minute)
	if got := loaded.Trains[0].TripID; got != "trip-b" {
		t.Fatalf("stored trip ID = %q, want trip-b", got)
	}
	if got := *loaded.Trains[0].DirectionID; got != 1 {
		t.Fatalf("stored direction = %d, want 1", got)
	}

	loaded.Trains[0].TripID = "mutated-result"
	*loaded.Trains[0].DirectionID = 88
	reloaded := store.Load(sourceTime, time.Minute)
	if got := reloaded.Trains[0].TripID; got != "trip-b" {
		t.Fatalf("reloaded trip ID = %q, want trip-b", got)
	}
	if got := *reloaded.Trains[0].DirectionID; got != 1 {
		t.Fatalf("reloaded direction = %d, want 1", got)
	}
}

func TestStoreRetainsLastKnownGoodSnapshot(t *testing.T) {
	store := NewStore()
	sourceTime := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	if err := store.Replace(Snapshot{SourceTimestamp: sourceTime, Trains: []Train{{NextStopID: "A01N", RouteID: "A", TripID: "trip-a", MovementState: MovementStateReportedStopped}}}); err != nil {
		t.Fatalf("Replace(valid) error = %v", err)
	}
	if err := store.Replace(Snapshot{}); !errors.Is(err, ErrMissingSourceTimestamp) {
		t.Fatalf("Replace(invalid) error = %v, want ErrMissingSourceTimestamp", err)
	}

	loaded := store.Load(sourceTime, time.Minute)
	if len(loaded.Trains) != 1 || loaded.Trains[0].TripID != "trip-a" {
		t.Fatalf("Load() = %#v, want preserved valid snapshot", loaded)
	}
}

func TestStoreRejectsOutOfOrderSnapshot(t *testing.T) {
	store := NewStore()
	newer := time.Date(2026, time.September, 2, 12, 1, 0, 0, time.UTC)
	if err := store.Replace(Snapshot{SourceTimestamp: newer, Trains: []Train{{NextStopID: "A01N", TripID: "newer"}}}); err != nil {
		t.Fatalf("Replace(newer) error = %v", err)
	}
	if err := store.Replace(Snapshot{SourceTimestamp: newer.Add(-time.Minute), Trains: []Train{{NextStopID: "A01N", TripID: "older"}}}); !errors.Is(err, ErrOutOfOrderSnapshot) {
		t.Fatalf("Replace(older) error = %v, want ErrOutOfOrderSnapshot", err)
	}
	loaded := store.Load(newer, time.Minute)
	if len(loaded.Trains) != 1 || loaded.Trains[0].TripID != "newer" {
		t.Fatalf("Load() = %#v, want newer snapshot preserved", loaded)
	}
}

func TestStoreLoadCalculatesFreshness(t *testing.T) {
	store := NewStore()
	sourceTime := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	if err := store.Replace(Snapshot{SourceTimestamp: sourceTime, Trains: []Train{{NextStopID: "A01N", RouteID: "A", TripID: "trip-a"}}}); err != nil {
		t.Fatalf("Replace() error = %v", err)
	}

	fresh := store.Load(sourceTime.Add(time.Minute), time.Minute)
	if fresh.Freshness != FreshnessFresh || fresh.Trains[0].Freshness != FreshnessFresh {
		t.Fatalf("fresh Load() = %#v, want fresh snapshot and train", fresh)
	}
	stale := store.Load(sourceTime.Add(time.Minute+time.Nanosecond), time.Minute)
	if stale.Freshness != FreshnessStale || stale.Trains[0].Freshness != FreshnessStale {
		t.Fatalf("stale Load() = %#v, want stale snapshot and train", stale)
	}
	if stale.Trains[0].MovementState != MovementStateUnknown {
		t.Fatalf("stale movement state = %q, want unknown", stale.Trains[0].MovementState)
	}
}

func TestEmptyStoreIsUnavailable(t *testing.T) {
	loaded := NewStore().Load(time.Now(), time.Minute)
	if loaded.Freshness != FreshnessUnavailable || len(loaded.Trains) != 0 {
		t.Fatalf("Load() = %#v, want unavailable empty snapshot", loaded)
	}
}

func TestStoreSortsSnapshotOnReplace(t *testing.T) {
	store := NewStore()
	sourceTime := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	if err := store.Replace(Snapshot{SourceTimestamp: sourceTime, Trains: []Train{
		{NextStopID: "A01N", RouteID: "N", TripID: "trip-n"},
		{NextStopID: "A01N", RouteID: "A", TripID: "trip-z"},
		{NextStopID: "A01N", RouteID: "A", TripID: "trip-a"},
	}}); err != nil {
		t.Fatal(err)
	}

	loaded := store.Load(sourceTime, time.Minute)
	got := []string{loaded.Trains[0].TripID, loaded.Trains[1].TripID, loaded.Trains[2].TripID}
	want := []string{"trip-a", "trip-z", "trip-n"}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("train order = %v, want %v", got, want)
		}
	}
}

func TestStoreStatusReturnsMetadata(t *testing.T) {
	store := NewStore()
	if timestamp, freshness, trains := store.Status(time.Now(), time.Minute); !timestamp.IsZero() || freshness != FreshnessUnavailable || trains != 0 {
		t.Fatalf("empty Status() = %s/%q/%d, want zero/unavailable/0", timestamp, freshness, trains)
	}

	sourceTime := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	if err := store.Replace(Snapshot{SourceTimestamp: sourceTime, Trains: []Train{{NextStopID: "A01N", RouteID: "A", TripID: "trip-a"}}}); err != nil {
		t.Fatal(err)
	}
	if timestamp, freshness, trains := store.Status(sourceTime.Add(time.Minute), time.Minute); !timestamp.Equal(sourceTime) || freshness != FreshnessFresh || trains != 1 {
		t.Fatalf("Status() = %s/%q/%d, want %s/fresh/1", timestamp, freshness, trains, sourceTime)
	}
}

func TestPredictionPresenceAndRelationships(t *testing.T) {
	absolute := time.Unix(1000, 0).UTC()
	for _, test := range []struct {
		name      string
		stop      StopPrediction
		tripDelay *int32
		wantTime  time.Time
		wantDelay *int32
	}{
		{name: "omitted"},
		{name: "explicit zero", stop: StopPrediction{Arrival: &StopEvent{DelaySeconds: new(int32(0))}}, wantDelay: new(int32(0))},
		{name: "absolute only", stop: StopPrediction{Arrival: &StopEvent{Time: absolute}}, wantTime: absolute},
		{name: "empty arrival with departure delay", stop: StopPrediction{Arrival: &StopEvent{}, Departure: &StopEvent{DelaySeconds: new(int32(15))}}, wantDelay: new(int32(15))},
		{name: "departure only", stop: StopPrediction{Departure: &StopEvent{Time: absolute}}, wantTime: absolute},
		{name: "trip delay", tripDelay: new(int32(50)), wantDelay: new(int32(50))},
		{name: "event overrides trip", tripDelay: new(int32(50)), stop: StopPrediction{Arrival: &StopEvent{DelaySeconds: new(int32(-10))}}, wantDelay: new(int32(-10))},
		{name: "no data overrides predictions and trip delay", tripDelay: new(int32(50)), stop: StopPrediction{Relationship: StopNoData, Arrival: &StopEvent{Time: absolute, DelaySeconds: new(int32(10))}}},
		{name: "skipped", stop: StopPrediction{Relationship: StopSkipped, Arrival: &StopEvent{Time: absolute}}},
		{name: "arrival preferred", stop: StopPrediction{Arrival: &StopEvent{Time: absolute}, Departure: &StopEvent{Time: absolute.Add(time.Minute)}}, wantTime: absolute},
	} {
		t.Run(test.name, func(t *testing.T) {
			at, delay := (Train{TripDelaySeconds: test.tripDelay}).Prediction(&test.stop)
			if !at.Equal(test.wantTime) || (delay == nil) != (test.wantDelay == nil) || (delay != nil && *delay != *test.wantDelay) {
				t.Fatalf("prediction = %v/%v, want %v/%v", at, delay, test.wantTime, test.wantDelay)
			}
		})
	}
}

func TestObservationFreshnessDoesNotFollowFeedRefresh(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	for _, test := range []struct {
		name                            string
		feed, trip, vehicle             time.Time
		tripFreshness, vehicleFreshness Freshness
		movement                        MovementState
	}{
		{"fresh", now, now, now, FreshnessFresh, FreshnessFresh, MovementStateReportedStopped},
		{"exact boundary", now, now.Add(-time.Minute), now.Add(-time.Minute), FreshnessFresh, FreshnessFresh, MovementStateReportedStopped},
		{"old vehicle", now, now, now.Add(-time.Minute - time.Nanosecond), FreshnessFresh, FreshnessStale, MovementStateUnknown},
		{"old trip", now, now.Add(-2 * time.Minute), now, FreshnessStale, FreshnessFresh, MovementStateReportedStopped},
		{"missing", now, time.Time{}, time.Time{}, FreshnessUnavailable, FreshnessUnavailable, MovementStateUnknown},
		{"future", now, now.Add(time.Second), now.Add(time.Second), FreshnessUnavailable, FreshnessUnavailable, MovementStateUnknown},
		{"old feed", now.Add(-2 * time.Minute), now, now, FreshnessStale, FreshnessStale, MovementStateUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewStore()
			if err := store.Replace(Snapshot{SourceTimestamp: test.feed, Trains: []Train{{TripUpdateTimestamp: test.trip, VehicleTimestamp: test.vehicle, MovementState: MovementStateReportedStopped}}}); err != nil {
				t.Fatal(err)
			}
			got := store.Load(now, time.Minute).Trains[0]
			if got.TripUpdateFreshness != test.tripFreshness || got.VehicleFreshness != test.vehicleFreshness || got.MovementState != test.movement {
				t.Fatalf("observation state = %+v", got)
			}
			// A new feed must not rejuvenate either observation.
			if err := store.Replace(Snapshot{SourceTimestamp: now.Add(2 * time.Minute), Trains: []Train{got}}); err != nil {
				t.Fatal(err)
			}
			got = store.Load(now.Add(2*time.Minute), time.Minute).Trains[0]
			if got.VehicleFreshness == FreshnessFresh || got.TripUpdateFreshness == FreshnessFresh || got.MovementState != MovementStateUnknown {
				t.Fatalf("refreshed observations = %+v", got)
			}
		})
	}
}

func TestStoreCopiesCompleteStopStateAndRetainsCancellations(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	store := NewStore()
	next := Snapshot{SourceName: "ace", SourceTimestamp: now, ReceivedAt: now.Add(time.Second), Trains: []Train{{
		EntityID: "cancellation", TripID: "trip", StartTime: "12:00:00", ScheduleRelationship: "CANCELED",
		TripDelaySeconds: new(int32(20)), PredictionDelaySeconds: new(int32(10)),
		VehicleStopSequence: new(uint32(7)), VehicleStatus: "STOPPED_AT", VehicleStopID: "A01N",
		Stops: []StopPrediction{{StopID: "A01N", Sequence: new(uint32(1)), Relationship: StopSkipped,
			Arrival:   &StopEvent{Time: now, DelaySeconds: new(int32(10)), Uncertainty: new(int32(2))},
			Departure: &StopEvent{Time: now.Add(time.Minute), DelaySeconds: new(int32(15)), Uncertainty: new(int32(3))},
		}},
	}}}
	if err := store.Replace(next); err != nil {
		t.Fatal(err)
	}
	mutate := func(snapshot Snapshot) {
		trip := &snapshot.Trains[0]
		*trip.TripDelaySeconds, *trip.PredictionDelaySeconds = 99, 99
		*trip.VehicleStopSequence = 99
		stop := &trip.Stops[0]
		stop.StopID, *stop.Sequence = "changed", 99
		stop.Arrival.Time, *stop.Arrival.DelaySeconds, *stop.Arrival.Uncertainty = time.Time{}, 99, 99
		stop.Departure.Time, *stop.Departure.DelaySeconds, *stop.Departure.Uncertainty = time.Time{}, 99, 99
	}
	mutate(next)
	mutate(store.Load(now, time.Minute))
	trip := store.Load(now, time.Minute).Trains[0]
	if *trip.VehicleStopSequence != 7 {
		t.Fatal("vehicle occurrence mutated through snapshot")
	}
	stop := trip.Stops[0]
	if !trip.Canceled() || trip.NextServedStop() != nil || trip.StartTime != "12:00:00" || trip.SourceName != "ace" || !trip.ReceivedAt.Equal(now.Add(time.Second)) || *trip.TripDelaySeconds != 20 || *trip.PredictionDelaySeconds != 10 {
		t.Fatalf("retained trip = %+v", trip)
	}
	if stop.StopID != "A01N" || *stop.Sequence != 1 || !stop.Arrival.Time.Equal(now) || *stop.Arrival.DelaySeconds != 10 || *stop.Arrival.Uncertainty != 2 || !stop.Departure.Time.Equal(now.Add(time.Minute)) || *stop.Departure.DelaySeconds != 15 || *stop.Departure.Uncertainty != 3 {
		t.Fatalf("retained stop = %+v", stop)
	}
	if _, _, active := store.Status(now, time.Minute); active != 0 {
		t.Fatalf("active trains = %d", active)
	}
}

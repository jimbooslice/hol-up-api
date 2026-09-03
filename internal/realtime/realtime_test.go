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
	if err := store.Replace(Snapshot{SourceTimestamp: sourceTime, Trains: []Train{{RouteID: "A", TripID: "trip-a", MovementState: MovementStateReportedStopped}}}); err != nil {
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
	if err := store.Replace(Snapshot{SourceTimestamp: newer, Trains: []Train{{TripID: "newer"}}}); err != nil {
		t.Fatalf("Replace(newer) error = %v", err)
	}
	if err := store.Replace(Snapshot{SourceTimestamp: newer.Add(-time.Minute), Trains: []Train{{TripID: "older"}}}); !errors.Is(err, ErrOutOfOrderSnapshot) {
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
	if err := store.Replace(Snapshot{SourceTimestamp: sourceTime, Trains: []Train{{RouteID: "A", TripID: "trip-a"}}}); err != nil {
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

// Package realtime holds normalized realtime transit state.
package realtime

import (
	"errors"
	"sort"
	"sync/atomic"
	"time"
)

var (
	ErrMissingSourceTimestamp = errors.New("realtime snapshot is missing a source timestamp")
	ErrOutOfOrderSnapshot     = errors.New("realtime snapshot is older than the current snapshot")
)

type PositionBasis string

const (
	PositionBasisRealtimePrediction PositionBasis = "realtime_prediction"
	PositionBasisScheduleFallback   PositionBasis = "schedule_fallback"
)

type MovementState string

const (
	MovementStateInferredMoving  MovementState = "inferred_moving"
	MovementStateReportedStopped MovementState = "reported_stopped"
	MovementStateUnknown         MovementState = "unknown"
)

type Direction string

const (
	DirectionNorthbound Direction = "northbound"
	DirectionEastbound  Direction = "eastbound"
	DirectionSouthbound Direction = "southbound"
	DirectionWestbound  Direction = "westbound"
	DirectionUnknown    Direction = "unknown"
)

type Freshness string

const (
	FreshnessFresh       Freshness = "fresh"
	FreshnessStale       Freshness = "stale"
	FreshnessUnavailable Freshness = "unavailable"
)

// Train is a normalized GTFS-RT trip instance. PositionBasis makes clear
// whether its prediction came from realtime data or schedule fallback.
type Train struct {
	EntityID               string
	RouteID                string
	TripID                 string
	ServiceDate            string
	DirectionID            *uint32
	Direction              Direction
	NextStopID             string
	NextStopSequence       uint32
	PredictedAt            time.Time
	PredictionDelaySeconds int32
	PositionBasis          PositionBasis
	MovementState          MovementState
	Freshness              Freshness
	SourceTimestamp        time.Time
}

// Snapshot is one complete replacement set from a realtime source.
type Snapshot struct {
	SourceTimestamp time.Time
	Freshness       Freshness
	Trains          []Train
}

// Store atomically publishes complete snapshots. Invalid replacements leave
// the last known good snapshot intact.
type Store struct {
	snapshot atomic.Pointer[Snapshot]
}

func NewStore() *Store {
	return &Store{}
}

// Replace publishes next only when it is structurally usable.
func (s *Store) Replace(next Snapshot) error {
	if next.SourceTimestamp.IsZero() {
		return ErrMissingSourceTimestamp
	}

	next = cloneSnapshot(next)
	for index := range next.Trains {
		if next.Trains[index].SourceTimestamp.IsZero() {
			next.Trains[index].SourceTimestamp = next.SourceTimestamp
		}
	}
	sort.Slice(next.Trains, func(left, right int) bool {
		if next.Trains[left].RouteID != next.Trains[right].RouteID {
			return next.Trains[left].RouteID < next.Trains[right].RouteID
		}
		if next.Trains[left].TripID != next.Trains[right].TripID {
			return next.Trains[left].TripID < next.Trains[right].TripID
		}
		return next.Trains[left].EntityID < next.Trains[right].EntityID
	})
	for {
		current := s.snapshot.Load()
		if current != nil && next.SourceTimestamp.Before(current.SourceTimestamp) {
			return ErrOutOfOrderSnapshot
		}
		if s.snapshot.CompareAndSwap(current, &next) {
			return nil
		}
	}
}

// Load returns an independent copy and calculates freshness at now. A missing
// snapshot is unavailable rather than an error so callers can report state.
func (s *Store) Load(now time.Time, staleAfter time.Duration) Snapshot {
	current := s.snapshot.Load()
	if current == nil {
		return Snapshot{Freshness: FreshnessUnavailable}
	}

	result := cloneSnapshot(*current)
	result.Freshness = FreshnessAt(result.SourceTimestamp, now, staleAfter)
	for index := range result.Trains {
		result.Trains[index].Freshness = FreshnessAt(result.Trains[index].SourceTimestamp, now, staleAfter)
		// A stale stop observation cannot describe the train's current movement.
		if result.Trains[index].Freshness != FreshnessFresh {
			result.Trains[index].MovementState = MovementStateUnknown
		}
	}
	return result
}

// Status returns source metadata without copying the trains in the snapshot.
func (s *Store) Status(now time.Time, staleAfter time.Duration) (time.Time, Freshness, int) {
	current := s.snapshot.Load()
	if current == nil {
		return time.Time{}, FreshnessUnavailable, 0
	}
	return current.SourceTimestamp, FreshnessAt(current.SourceTimestamp, now, staleAfter), len(current.Trains)
}

// FreshnessAt classifies a source timestamp. A non-positive staleAfter leaves
// valid data fresh; callers that need a stale policy must supply a duration.
func FreshnessAt(sourceTimestamp, now time.Time, staleAfter time.Duration) Freshness {
	if sourceTimestamp.IsZero() {
		return FreshnessUnavailable
	}
	if staleAfter > 0 && now.Sub(sourceTimestamp) > staleAfter {
		return FreshnessStale
	}
	return FreshnessFresh
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	result := snapshot
	result.Trains = append([]Train(nil), snapshot.Trains...)
	for index := range result.Trains {
		if direction := result.Trains[index].DirectionID; direction != nil {
			directionCopy := *direction
			result.Trains[index].DirectionID = &directionCopy
		}
	}
	return result
}

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

type StopRelationship string

const (
	StopScheduled StopRelationship = "SCHEDULED"
	StopSkipped   StopRelationship = "SKIPPED"
	StopNoData    StopRelationship = "NO_DATA"
)

type VehicleMatch string

const (
	VehicleMatched   VehicleMatch = "matched"
	VehicleUnmatched VehicleMatch = "unmatched"
	VehicleAmbiguous VehicleMatch = "ambiguous"
)

// StopEvent retains presence independently for arrival and departure. Zero
// time means omitted; an explicit Unix epoch is a nonzero time.Time.
type StopEvent struct {
	Time          time.Time
	TimePresent   bool
	DelaySeconds  *int32
	Uncertainty   *int32
	ScheduledTime time.Time
}

type StopPrediction struct {
	StopID       string
	InputIndex   *uint32 // Original feed order, retained when compact normalization sorts stops.
	Sequence     *uint32
	Relationship StopRelationship
	Arrival      *StopEvent
	Departure    *StopEvent
}

// Train retains a complete trip update, including canceled trips and stops
// without predictions. Compact next-stop fields are derived from Stops.
type Train struct {
	EntityID                   string
	RouteID                    string
	TripID                     string
	ServiceDate                string
	StartTime                  string
	ScheduleRelationship       string
	Stops                      []StopPrediction
	TripDelaySeconds           *int32
	NYCTDirection              *Direction // Supplied extension value, never inferred from a stop.
	DirectionID                *uint32
	Direction                  Direction
	NextStopID                 string
	NextStopSequence           uint32
	PredictedAt                time.Time
	PredictionDelaySeconds     *int32
	PositionBasis              PositionBasis
	MovementState              MovementState
	Freshness                  Freshness
	SourceTimestamp            time.Time
	SourceName                 string
	ReceivedAt                 time.Time
	TripUpdateTimestamp        time.Time
	TripUpdateTimestampPresent bool
	TripUpdateFreshness        Freshness
	VehicleEntityID            string
	VehicleTimestamp           time.Time
	VehicleTimestampPresent    bool
	VehicleStopID              string
	VehicleStopSequence        *uint32
	VehicleStatus              string
	VehicleFreshness           Freshness
	VehicleMatch               VehicleMatch
}

func (t Train) Canceled() bool {
	return t.ScheduleRelationship == "CANCELED" || t.ScheduleRelationship == "DELETED"
}

// Active trains need an identified next served stop for the compact API.
// Sequence-only updates remain retained until schedule reconciliation exists.
func (t Train) Active() bool {
	return !t.Canceled() && t.RouteID != "" && t.NextStopID != ""
}

func (t Train) NextServedStop() *StopPrediction {
	if t.Canceled() {
		return nil
	}
	for i := range t.Stops {
		if t.Stops[i].Relationship != StopSkipped {
			return &t.Stops[i]
		}
	}
	return nil
}

// Prediction selects an absolute arrival, then an absolute departure, then
// a delay-only event. NO_DATA must not inherit a trip-level delay.
func (t Train) Prediction(stop *StopPrediction) (time.Time, *int32) {
	if stop == nil || stop.Relationship == StopNoData || stop.Relationship == StopSkipped || t.Canceled() {
		return time.Time{}, nil
	}
	event := stop.Arrival
	if event == nil || (event.Time.IsZero() && (event.DelaySeconds == nil || (stop.Departure != nil && !stop.Departure.Time.IsZero()))) {
		event = stop.Departure
	}
	delay := t.TripDelaySeconds
	if event == nil {
		return time.Time{}, delay
	}
	if event.DelaySeconds != nil {
		delay = event.DelaySeconds
	}
	return event.Time, delay
}

type ReplacementPeriod struct {
	RouteID    string
	Start, End *uint64
}

func (p ReplacementPeriod) Clone() ReplacementPeriod {
	p.Start, p.End = clonePointer(p.Start), clonePointer(p.End)
	return p
}

// Snapshot retains completeness separately from its compact API records.
type Snapshot struct {
	SourceTimestamp    time.Time
	SourceName         string
	ReceivedAt         time.Time
	Freshness          Freshness
	Trains             []Train
	UnidentifiedTrips  []Train
	ReplacementPeriods []ReplacementPeriod
	NYCTVersion        string
	Complete           bool // Unfiltered FULL_DATASET; false cannot establish absence or uniqueness.
	activeTrains       int
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

	next = next.Clone()
	next.activeTrains = 0
	for index := range next.Trains {
		if next.Trains[index].VehicleMatch == "" {
			next.Trains[index].VehicleMatch = VehicleUnmatched
		}
		if next.Trains[index].Active() {
			next.activeTrains++
		}
		next.Trains[index].SourceName = next.SourceName
		next.Trains[index].ReceivedAt = next.ReceivedAt
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

	result := current.Clone()
	result.Freshness = FreshnessAt(result.SourceTimestamp, now, staleAfter)
	for index := range result.Trains {
		result.Trains[index].Freshness = FreshnessAt(result.Trains[index].SourceTimestamp, now, staleAfter)
		result.Trains[index].TripUpdateFreshness = ObservationFreshness(result.Trains[index].TripUpdateTimestamp, result.Trains[index].Freshness, now, staleAfter)
		result.Trains[index].VehicleFreshness = ObservationFreshness(result.Trains[index].VehicleTimestamp, result.Trains[index].Freshness, now, staleAfter)
		// A stale stop observation cannot describe the train's current movement.
		if result.Trains[index].VehicleFreshness != FreshnessFresh || result.Trains[index].Canceled() {
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
	return current.SourceTimestamp, FreshnessAt(current.SourceTimestamp, now, staleAfter), current.activeTrains
}

func ObservationFreshness(observedAt time.Time, feedFreshness Freshness, now time.Time, staleAfter time.Duration) Freshness {
	// Future observations cannot establish current movement or prediction age.
	if observedAt.IsZero() || observedAt.After(now) {
		return FreshnessUnavailable
	}
	if feedFreshness != FreshnessFresh {
		return feedFreshness
	}
	return FreshnessAt(observedAt, now, staleAfter)
}

// PredictionFreshnessAt allows an omitted optional timestamp to use the feed
// header. A supplied but unusable observation never becomes an absent one.
func (t Train) PredictionFreshnessAt(feedTimestamp, now time.Time, staleAfter time.Duration) (Freshness, string) {
	feed := FreshnessAt(feedTimestamp, now, staleAfter)
	if !t.TripUpdateTimestampPresent && t.TripUpdateTimestamp.IsZero() {
		return feed, "feed_header"
	}
	if t.TripUpdateTimestamp.IsZero() || t.TripUpdateTimestamp.Unix() <= 0 || t.TripUpdateTimestamp.Year() > 9999 || t.TripUpdateTimestamp.After(now) {
		return FreshnessUnavailable, "invalid_trip_update_timestamp"
	}
	return ObservationFreshness(t.TripUpdateTimestamp, feed, now, staleAfter), "trip_update"
}

// EventTime gives an absolute prediction precedence over supplied delays.
// A zero scheduled instant means the static event was omitted.
func EventTime(event *StopEvent, tripDelay *int32, scheduled time.Time) (time.Time, string, bool) {
	if event != nil && (event.TimePresent || !event.Time.IsZero()) {
		return event.Time, "realtime_events", !event.Time.IsZero() && event.Time.Unix() > 0 && event.Time.Year() <= 9999
	}
	delay := tripDelay
	if event != nil && event.DelaySeconds != nil {
		delay = event.DelaySeconds
	}
	if delay != nil && !scheduled.IsZero() {
		return scheduled.Add(time.Duration(*delay) * time.Second), "delay_derived", true
	}
	return time.Time{}, "", true
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

// Clone returns an independent copy, including every optional nested value.
func (snapshot Snapshot) Clone() Snapshot {
	result := snapshot
	result.Trains = cloneTrains(snapshot.Trains)
	result.UnidentifiedTrips = cloneTrains(snapshot.UnidentifiedTrips)
	result.ReplacementPeriods = append([]ReplacementPeriod(nil), snapshot.ReplacementPeriods...)
	for i := range result.ReplacementPeriods {
		result.ReplacementPeriods[i] = result.ReplacementPeriods[i].Clone()
	}
	return result
}

func cloneTrains(trains []Train) []Train {
	result := append([]Train(nil), trains...)
	for i := range result {
		result[i] = result[i].Clone()
	}
	return result
}

func (train Train) Clone() Train {
	train.VehicleStopSequence = clonePointer(train.VehicleStopSequence)
	train.DirectionID = clonePointer(train.DirectionID)
	train.NYCTDirection = clonePointer(train.NYCTDirection)
	train.TripDelaySeconds = clonePointer(train.TripDelaySeconds)
	train.PredictionDelaySeconds = clonePointer(train.PredictionDelaySeconds)
	train.Stops = append([]StopPrediction(nil), train.Stops...)
	for i := range train.Stops {
		stop := &train.Stops[i]
		stop.Sequence = clonePointer(stop.Sequence)
		stop.InputIndex = clonePointer(stop.InputIndex)
		stop.Arrival = cloneEvent(stop.Arrival)
		stop.Departure = cloneEvent(stop.Departure)
	}
	return train
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneEvent(event *StopEvent) *StopEvent {
	copy := clonePointer(event)
	if copy != nil {
		copy.DelaySeconds = clonePointer(copy.DelaySeconds)
		copy.Uncertainty = clonePointer(copy.Uncertainty)
	}
	return copy
}

// Package ingest adapts a GTFS-Realtime protobuf feed into normalized state.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"time"

	"google.golang.org/protobuf/proto"
	"hol-up-prototype/internal/gen/gtfs"
	"hol-up-prototype/internal/realtime"
)

const MaxFeedBytes int64 = 10 << 20

var (
	ErrDifferentialFeed = errors.New("differential GTFS-Realtime feeds are unsupported")
	ErrFeedTooLarge     = errors.New("GTFS-Realtime feed exceeds 10 MiB")
)

type HTTPStatusError int

func (e HTTPStatusError) Error() string {
	return fmt.Sprintf("fetch GTFS-Realtime feed: unexpected HTTP status %d", e)
}

// FutureTimestampError rejects a feed header beyond the local receipt tolerance.
type FutureTimestampError struct {
	SourceTimestamp time.Time
	ReceivedAt      time.Time
	MaxFutureSkew   time.Duration
}

func (e *FutureTimestampError) Error() string {
	return "GTFS-Realtime source timestamp exceeds allowed future skew"
}

// Loader fetches one complete protobuf feed and atomically publishes it.
type Loader struct {
	client        *http.Client
	store         *realtime.Store
	url           string
	routeID       string
	sourceName    string
	maxFutureSkew time.Duration
	now           func() time.Time
}

func NewLoader(client *http.Client, store *realtime.Store, sourceName, url, routeID string, maxFutureSkew time.Duration, now func() time.Time) *Loader {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Loader{client: client, store: store, sourceName: sourceName, url: url, routeID: routeID, maxFutureSkew: maxFutureSkew, now: now}
}

func (l *Loader) Load(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.url, nil)
	if err != nil {
		return fmt.Errorf("create GTFS-Realtime request: %w", err)
	}
	req.Header.Set("Accept", "application/x-protobuf, application/octet-stream, text/plain")

	response, err := l.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch GTFS-Realtime feed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		return HTTPStatusError(response.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, MaxFeedBytes+1))
	if err != nil {
		return fmt.Errorf("read GTFS-Realtime feed: %w", err)
	}
	receivedAt := l.now()
	if int64(len(body)) > MaxFeedBytes {
		return ErrFeedTooLarge
	}

	snapshot, err := Decode(body, l.routeID)
	if err != nil {
		return err
	}
	if snapshot.SourceTimestamp.After(receivedAt.Add(l.maxFutureSkew)) {
		return &FutureTimestampError{SourceTimestamp: snapshot.SourceTimestamp, ReceivedAt: receivedAt, MaxFutureSkew: l.maxFutureSkew}
	}
	snapshot.SourceName = l.sourceName
	snapshot.ReceivedAt = receivedAt
	if err := l.store.Replace(snapshot); err != nil {
		return fmt.Errorf("publish GTFS-Realtime feed: %w", err)
	}
	return nil
}

// Decode maps a full GTFS-Realtime dataset into transport-neutral state.
func Decode(data []byte, routeID string) (realtime.Snapshot, error) {
	var feed gtfs.FeedMessage
	if err := proto.Unmarshal(data, &feed); err != nil {
		return realtime.Snapshot{}, fmt.Errorf("decode GTFS-Realtime protobuf: %w", err)
	}
	if feed.GetHeader() == nil || feed.GetHeader().GetTimestamp() == 0 {
		return realtime.Snapshot{}, realtime.ErrMissingSourceTimestamp
	}
	if feed.GetHeader().GetIncrementality() == gtfs.FeedHeader_DIFFERENTIAL {
		return realtime.Snapshot{}, ErrDifferentialFeed
	}
	if feed.GetHeader().GetTimestamp() > math.MaxInt64 {
		return realtime.Snapshot{}, fmt.Errorf("GTFS-Realtime timestamp is out of range")
	}

	sourceTimestamp := time.Unix(int64(feed.GetHeader().GetTimestamp()), 0).UTC()
	vehicles := make(map[string][]*gtfs.FeedEntity)
	trips := make(map[string][]*gtfs.TripDescriptor)
	for _, entity := range feed.GetEntity() {
		if entity.GetIsDeleted() {
			continue
		}
		if trip := entity.GetTripUpdate().GetTrip(); trip != nil && trip.GetTripId() != "" {
			// Count all descriptors before filtering routes or canceled/empty trips.
			trips[trip.GetTripId()] = append(trips[trip.GetTripId()], trip)
		}
		vehicle := entity.GetVehicle()
		if vehicle == nil || vehicle.GetTrip() == nil {
			continue
		}
		if tripID := vehicle.GetTrip().GetTripId(); tripID != "" {
			vehicles[tripID] = append(vehicles[tripID], entity)
		}
	}

	snapshot := realtime.Snapshot{SourceTimestamp: sourceTimestamp, Complete: routeID == "" && feed.GetHeader().GetIncrementality() == gtfs.FeedHeader_FULL_DATASET}
	if proto.HasExtension(feed.Header, gtfs.E_NyctFeedHeader) {
		if header, ok := proto.GetExtension(feed.Header, gtfs.E_NyctFeedHeader).(*gtfs.NyctFeedHeader); ok {
			snapshot.NYCTVersion = header.GetNyctSubwayVersion()
			for _, period := range header.GetTripReplacementPeriod() {
				value := realtime.ReplacementPeriod{RouteID: period.GetRouteId()}
				if bounds := period.GetReplacementPeriod(); bounds != nil {
					value.Start, value.End = bounds.Start, bounds.End
				}
				snapshot.ReplacementPeriods = append(snapshot.ReplacementPeriods, value)
			}
		}
	}
	for _, entity := range feed.GetEntity() {
		update := entity.GetTripUpdate()
		if entity.GetIsDeleted() || update == nil {
			continue
		}
		descriptor := update.GetTrip()
		if descriptor == nil {
			descriptor = &gtfs.TripDescriptor{}
		}
		if routeID != "" && descriptor.GetRouteId() != routeID {
			continue
		}
		vehicleEntity, match := matchingVehicle(descriptor, vehicles[descriptor.GetTripId()], trips[descriptor.GetTripId()])
		vehicle := vehicleEntity.GetVehicle()
		var vehicleTimestamp time.Time
		if vehicle != nil {
			vehicleTimestamp = observationTimestamp(vehicle.Timestamp)
		}
		train := realtime.Train{
			EntityID:                   entity.GetId(),
			RouteID:                    descriptor.GetRouteId(),
			TripID:                     descriptor.GetTripId(),
			ServiceDate:                descriptor.GetStartDate(),
			StartTime:                  descriptor.GetStartTime(),
			ScheduleRelationship:       descriptor.GetScheduleRelationship().String(),
			Stops:                      stopPredictions(update.GetStopTimeUpdate()),
			TripDelaySeconds:           update.Delay,
			DirectionID:                descriptor.DirectionId,
			Direction:                  direction(descriptor, ""),
			PositionBasis:              realtime.PositionBasisRealtimePrediction,
			MovementState:              movementState(vehicle),
			SourceTimestamp:            sourceTimestamp,
			TripUpdateTimestamp:        observationTimestamp(update.Timestamp),
			TripUpdateTimestampPresent: update.Timestamp != nil,
			VehicleEntityID:            vehicleEntity.GetId(),
			VehicleTimestamp:           vehicleTimestamp,
			VehicleMatch:               match,
		}
		if vehicle != nil {
			train.VehicleTimestampPresent = vehicle.Timestamp != nil
			train.VehicleStopID = vehicle.GetStopId()
			train.VehicleStopSequence = vehicle.CurrentStopSequence
			if vehicle.CurrentStatus != nil {
				train.VehicleStatus = vehicle.GetCurrentStatus().String()
			}
		}
		if proto.HasExtension(descriptor, gtfs.E_NyctTripDescriptor) {
			if ext, ok := proto.GetExtension(descriptor, gtfs.E_NyctTripDescriptor).(*gtfs.NyctTripDescriptor); ok && ext.Direction != nil {
				supplied := direction(descriptor, "")
				train.NYCTDirection = &supplied
			}
		}
		if nextStop := train.NextServedStop(); nextStop != nil {
			train.NextStopID = nextStop.StopID
			train.Direction = direction(descriptor, nextStop.StopID)
			if nextStop.Sequence != nil {
				train.NextStopSequence = *nextStop.Sequence
			} else if vehicle != nil && vehicle.GetStopId() == train.NextStopID {
				train.NextStopSequence = vehicle.GetCurrentStopSequence()
			}
			train.PredictedAt, train.PredictionDelaySeconds = train.Prediction(nextStop)
		}
		if train.Canceled() {
			train.MovementState = realtime.MovementStateUnknown
		}
		if train.TripID == "" {
			snapshot.UnidentifiedTrips = append(snapshot.UnidentifiedTrips, train)
		} else {
			snapshot.Trains = append(snapshot.Trains, train)
		}
	}

	return snapshot, nil
}

func matchingVehicle(trip *gtfs.TripDescriptor, candidates []*gtfs.FeedEntity, trips []*gtfs.TripDescriptor) (*gtfs.FeedEntity, realtime.VehicleMatch) {
	var match *gtfs.FeedEntity
	for _, candidate := range candidates {
		if !compatibleTrips(trip, candidate.GetVehicle().GetTrip()) {
			continue
		}
		if match != nil {
			return nil, realtime.VehicleAmbiguous
		}
		match = candidate
	}
	if match == nil {
		return nil, realtime.VehicleUnmatched
	}
	compatible := 0
	for _, other := range trips {
		if compatibleTrips(other, match.GetVehicle().GetTrip()) {
			compatible++
		}
	}
	if compatible != 1 {
		return nil, realtime.VehicleAmbiguous
	}
	return match, realtime.VehicleMatched
}

func compatibleTrips(left, right *gtfs.TripDescriptor) bool {
	if left == nil || right == nil || left.GetTripId() == "" || left.GetTripId() != right.GetTripId() {
		return false
	}
	if differentWhenPresent(left.GetRouteId(), right.GetRouteId()) ||
		differentWhenPresent(left.GetStartDate(), right.GetStartDate()) ||
		differentWhenPresent(left.GetStartTime(), right.GetStartTime()) {
		return false
	}
	return left.DirectionId == nil || right.DirectionId == nil || left.GetDirectionId() == right.GetDirectionId()
}

func differentWhenPresent(left, right string) bool {
	return left != "" && right != "" && left != right
}

func stopPredictions(updates []*gtfs.TripUpdate_StopTimeUpdate) []realtime.StopPrediction {
	stops := make([]realtime.StopPrediction, 0, len(updates))
	allSequences := true
	for index, update := range updates {
		if update == nil {
			continue
		}
		allSequences = allSequences && update.StopSequence != nil
		stops = append(stops, realtime.StopPrediction{
			StopID: update.GetStopId(), Sequence: update.StopSequence, InputIndex: proto.Uint32(uint32(index)),
			Relationship: realtime.StopRelationship(update.GetScheduleRelationship().String()),
			Arrival:      stopEvent(update.GetArrival()), Departure: stopEvent(update.GetDeparture()),
		})
	}
	// Without static GTFS, missing sequences cannot be inferred from stop IDs.
	// Preserve feed order unless every stop has an explicit sequence.
	if allSequences {
		sort.SliceStable(stops, func(i, j int) bool { return *stops[i].Sequence < *stops[j].Sequence })
	}
	return stops
}

func stopEvent(event *gtfs.TripUpdate_StopTimeEvent) *realtime.StopEvent {
	if event == nil {
		return nil
	}
	normalized := &realtime.StopEvent{DelaySeconds: event.Delay, Uncertainty: event.Uncertainty}
	if event.Time != nil {
		normalized.Time = time.Unix(*event.Time, 0).UTC()
		normalized.TimePresent = true
	}
	if event.ScheduledTime != nil {
		normalized.ScheduledTime = time.Unix(*event.ScheduledTime, 0).UTC()
	}
	return normalized
}

func observationTimestamp(value *uint64) time.Time {
	if value == nil || *value > math.MaxInt64 {
		return time.Time{}
	}
	return time.Unix(int64(*value), 0).UTC()
}

func movementState(vehicle *gtfs.VehiclePosition) realtime.MovementState {
	if vehicle == nil {
		return realtime.MovementStateUnknown
	}
	if vehicle.CurrentStatus != nil && vehicle.GetCurrentStatus() == gtfs.VehiclePosition_STOPPED_AT {
		return realtime.MovementStateReportedStopped
	}
	if vehicle.CurrentStatus != nil || vehicle.Timestamp != nil {
		return realtime.MovementStateInferredMoving
	}
	return realtime.MovementStateUnknown
}

func direction(trip *gtfs.TripDescriptor, stopID string) realtime.Direction {
	if proto.HasExtension(trip, gtfs.E_NyctTripDescriptor) {
		if extension, ok := proto.GetExtension(trip, gtfs.E_NyctTripDescriptor).(*gtfs.NyctTripDescriptor); ok && extension.Direction != nil {
			switch extension.GetDirection() {
			case gtfs.NyctTripDescriptor_NORTH:
				return realtime.DirectionNorthbound
			case gtfs.NyctTripDescriptor_EAST:
				return realtime.DirectionEastbound
			case gtfs.NyctTripDescriptor_SOUTH:
				return realtime.DirectionSouthbound
			case gtfs.NyctTripDescriptor_WEST:
				return realtime.DirectionWestbound
			}
		}
	}
	if len(stopID) > 0 {
		switch stopID[len(stopID)-1] {
		case 'N':
			return realtime.DirectionNorthbound
		case 'S':
			return realtime.DirectionSouthbound
		}
	}
	return realtime.DirectionUnknown
}

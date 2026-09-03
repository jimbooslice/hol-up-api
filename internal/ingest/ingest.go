// Package ingest adapts a GTFS-Realtime protobuf feed into normalized state.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
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

// Loader fetches one complete protobuf feed and atomically publishes it.
type Loader struct {
	client  *http.Client
	store   *realtime.Store
	url     string
	routeID string
}

func NewLoader(client *http.Client, store *realtime.Store, url, routeID string) *Loader {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Loader{client: client, store: store, url: url, routeID: routeID}
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
		return fmt.Errorf("fetch GTFS-Realtime feed: unexpected HTTP status %d", response.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, MaxFeedBytes+1))
	if err != nil {
		return fmt.Errorf("read GTFS-Realtime feed: %w", err)
	}
	if int64(len(body)) > MaxFeedBytes {
		return ErrFeedTooLarge
	}

	snapshot, err := Decode(body, l.routeID)
	if err != nil {
		return err
	}
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
	vehicles := make(map[string][]*gtfs.VehiclePosition)
	for _, entity := range feed.GetEntity() {
		vehicle := entity.GetVehicle()
		if entity.GetIsDeleted() || vehicle == nil || vehicle.GetTrip() == nil {
			continue
		}
		if tripID := vehicle.GetTrip().GetTripId(); tripID != "" {
			vehicles[tripID] = append(vehicles[tripID], vehicle)
		}
	}

	trains := make([]realtime.Train, 0, len(feed.GetEntity())/2)
	for _, entity := range feed.GetEntity() {
		update := entity.GetTripUpdate()
		if entity.GetIsDeleted() || update == nil || update.GetTrip() == nil {
			continue
		}
		descriptor := update.GetTrip()
		if descriptor.GetTripId() == "" || descriptor.GetRouteId() == "" {
			continue
		}
		if routeID != "" && descriptor.GetRouteId() != routeID {
			continue
		}
		if descriptor.GetScheduleRelationship() == gtfs.TripDescriptor_CANCELED {
			continue
		}

		nextStop := nextServedStop(update.GetStopTimeUpdate())
		if nextStop == nil || nextStop.GetStopId() == "" {
			continue
		}
		vehicle := matchingVehicle(descriptor, vehicles[descriptor.GetTripId()])
		train := realtime.Train{
			EntityID:        entity.GetId(),
			RouteID:         descriptor.GetRouteId(),
			TripID:          descriptor.GetTripId(),
			ServiceDate:     descriptor.GetStartDate(),
			DirectionID:     directionID(descriptor),
			Direction:       direction(descriptor, nextStop),
			PositionBasis:   realtime.PositionBasisRealtimePrediction,
			MovementState:   movementState(vehicle),
			SourceTimestamp: sourceTimestamp,
		}
		train.NextStopID = nextStop.GetStopId()
		train.NextStopSequence = nextStop.GetStopSequence()
		train.PredictedAt, train.PredictionDelaySeconds = prediction(nextStop, update)
		if train.NextStopSequence == 0 && vehicle != nil && vehicle.GetStopId() == train.NextStopID {
			train.NextStopSequence = vehicle.GetCurrentStopSequence()
		}
		trains = append(trains, train)
	}

	return realtime.Snapshot{SourceTimestamp: sourceTimestamp, Trains: trains}, nil
}

func matchingVehicle(trip *gtfs.TripDescriptor, candidates []*gtfs.VehiclePosition) *gtfs.VehiclePosition {
	var match *gtfs.VehiclePosition
	for _, candidate := range candidates {
		if candidate == nil || !compatibleTrips(trip, candidate.GetTrip()) {
			continue
		}
		if match != nil {
			return nil
		}
		match = candidate
	}
	return match
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

func nextServedStop(updates []*gtfs.TripUpdate_StopTimeUpdate) *gtfs.TripUpdate_StopTimeUpdate {
	for _, update := range updates {
		if update != nil && update.GetScheduleRelationship() != gtfs.TripUpdate_StopTimeUpdate_SKIPPED {
			return update
		}
	}
	return nil
}

func prediction(stop *gtfs.TripUpdate_StopTimeUpdate, trip *gtfs.TripUpdate) (time.Time, int32) {
	event := stop.GetArrival()
	departure := stop.GetDeparture()
	if event == nil || (event.GetTime() <= 0 && departure != nil && departure.GetTime() > 0) {
		event = departure
	}
	delay := trip.GetDelay()
	if event == nil {
		return time.Time{}, delay
	}
	if event.Delay != nil {
		delay = event.GetDelay()
	}
	if event.GetTime() <= 0 {
		return time.Time{}, delay
	}
	return time.Unix(event.GetTime(), 0).UTC(), delay
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

func directionID(trip *gtfs.TripDescriptor) *uint32 {
	if trip.DirectionId != nil {
		value := trip.GetDirectionId()
		return &value
	}
	return nil
}

func direction(trip *gtfs.TripDescriptor, stop *gtfs.TripUpdate_StopTimeUpdate) realtime.Direction {
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
	if stop != nil {
		if id := stop.GetStopId(); len(id) > 0 {
			switch id[len(id)-1] {
			case 'N':
				return realtime.DirectionNorthbound
			case 'S':
				return realtime.DirectionSouthbound
			}
		}
	}
	return realtime.DirectionUnknown
}

package httpapi

import (
	"net/http"
	"slices"
	"time"

	"hol-up-prototype/internal/projection"
	"hol-up-prototype/internal/realtime"
	"hol-up-prototype/internal/reconcile"
	"hol-up-prototype/internal/schedule"
)

type scheduleResponse struct {
	ScheduleVersion     *string         `json:"schedule_version"`
	ScheduleEligible    bool            `json:"schedule_eligible"`
	ScheduleSource      schedule.Source `json:"schedule_source"`
	ScheduleValidatedAt *string         `json:"schedule_validated_at"`
	ScheduleAgeSeconds  *float64        `json:"schedule_age_seconds"`
}

func scheduleDTO(meta reconcile.Metadata, now time.Time) scheduleResponse {
	return scheduleResponse{ScheduleVersion: nullableString(meta.Schedule.Version), ScheduleEligible: meta.ScheduleEligible,
		ScheduleSource: meta.Schedule.Source, ScheduleValidatedAt: timestamp(meta.Schedule.ValidatedAt), ScheduleAgeSeconds: ageSeconds(meta.Schedule.ValidatedAt, now)}
}

type pointGeometry struct {
	Type        string     `json:"type"`
	Coordinates [2]float64 `json:"coordinates"`
}

type stationFeature struct {
	Type       string         `json:"type"`
	ID         string         `json:"id"`
	Geometry   *pointGeometry `json:"geometry"`
	Properties struct {
		Name   string  `json:"name"`
		Reason *string `json:"reason"`
	} `json:"properties"`
}

type lineGeometry struct {
	Type        string       `json:"type"`
	Coordinates [][2]float64 `json:"coordinates"`
}

type shapeFeature struct {
	Type       string        `json:"type"`
	ID         string        `json:"id"`
	Geometry   *lineGeometry `json:"geometry"`
	Properties struct {
		Usable bool    `json:"usable"`
		Reason *string `json:"reason"`
	} `json:"properties"`
}

type occurrenceResponse struct {
	StopID         string   `json:"stop_id"`
	StationID      string   `json:"station_id"`
	Sequence       uint32   `json:"stop_sequence"`
	DistanceMeters *float64 `json:"distance_meters"`
	Association    string   `json:"association"`
	Reason         *string  `json:"reason"`
}

type patternResponse struct {
	ID          string               `json:"service_pattern_id"`
	RouteID     string               `json:"route_id"`
	ShapeID     *string              `json:"shape_id"`
	DirectionID *int                 `json:"direction_id"`
	Stops       []occurrenceResponse `json:"stops"`
	Reason      *string              `json:"reason"`
}

type geometryResponse struct {
	scheduleResponse
	RouteID   string            `json:"route_id"`
	ShortName string            `json:"short_name"`
	LongName  string            `json:"long_name"`
	Stations  []stationFeature  `json:"stations"`
	Shapes    []shapeFeature    `json:"shapes"`
	Patterns  []patternResponse `json:"patterns"`
}

func (h *Handler) bundle() *reconcile.Bundle {
	if h.bundles == nil {
		return nil
	}
	return h.bundles.Load()
}

func (h *Handler) geometry(w http.ResponseWriter, routeID string, now time.Time) {
	bundle := h.bundle()
	if bundle == nil || bundle.Schedule() == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse("schedule_unavailable", "schedule is unavailable"))
		return
	}
	dataset := bundle.Schedule().Dataset()
	route, ok := dataset.Route(routeID)
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse("route_not_found", "route is not in the loaded schedule"))
		return
	}
	response := geometryResponse{scheduleResponse: scheduleDTO(bundle.MetadataAt(now), now), RouteID: routeID,
		ShortName: route.ShortName, LongName: route.LongName, Stations: []stationFeature{}, Shapes: []shapeFeature{}, Patterns: []patternResponse{}}
	for _, station := range dataset.RouteStations(routeID) {
		feature := stationFeature{Type: "Feature", ID: station.ID}
		feature.Properties.Name = station.Name
		if station.HasCoordinates {
			feature.Geometry = &pointGeometry{Type: "Point", Coordinates: [2]float64{station.Longitude, station.Latitude}}
		} else {
			feature.Properties.Reason = nullableString("missing_station_coordinates")
		}
		response.Stations = append(response.Stations, feature)
	}
	for _, shape := range dataset.RouteShapes(routeID) {
		feature := shapeFeature{Type: "Feature", ID: shape.ID}
		feature.Properties.Usable, feature.Properties.Reason = shape.Usable, nullableString(shape.Reason)
		if len(shape.Points) >= 2 {
			feature.Geometry = &lineGeometry{Type: "LineString", Coordinates: make([][2]float64, len(shape.Points))}
			for i, point := range shape.Points {
				feature.Geometry.Coordinates[i] = [2]float64{point.Longitude, point.Latitude}
			}
		}
		response.Shapes = append(response.Shapes, feature)
	}
	for _, pattern := range dataset.RoutePatterns(routeID) {
		item := patternResponse{ID: pattern.ID, RouteID: pattern.RouteID, ShapeID: nullableString(pattern.ShapeID),
			Reason: nullableString(pattern.Reason), Stops: make([]occurrenceResponse, len(pattern.Stops))}
		if pattern.Direction >= 0 {
			item.DirectionID = &pattern.Direction
		}
		for i, stop := range pattern.Stops {
			item.Stops[i] = occurrenceResponse{StopID: stop.StopID, StationID: stop.StationID, Sequence: stop.Sequence,
				Association: stop.Association, Reason: nullableString(stop.Reason)}
			if stop.HasDistance {
				item.Stops[i].DistanceMeters = &stop.DistanceMeters
			}
		}
		response.Patterns = append(response.Patterns, item)
	}
	writeJSON(w, http.StatusOK, response)
}

type positionsResponse struct {
	scheduleResponse
	AsOf              *string            `json:"as_of"`
	ProjectedAt       *string            `json:"projected_at"`
	BundleEvaluatedAt *string            `json:"bundle_evaluated_at"`
	Freshness         realtime.Freshness `json:"freshness"`
	Coverage          Coverage           `json:"coverage"`
	Sources           []sourceResponse   `json:"sources"`
	Trains            []positionResponse `json:"trains"`
}

type positionResponse struct {
	RouteID                    string                 `json:"route_id"`
	Direction                  realtime.Direction     `json:"direction"`
	DirectionID                *int                   `json:"direction_id"`
	TripID                     *string                `json:"trip_id"`
	ServiceDate                *string                `json:"service_date"`
	TripInstanceID             *string                `json:"trip_instance_id"`
	RealtimeTripID             string                 `json:"realtime_trip_id"`
	RealtimeServiceDate        string                 `json:"realtime_service_date"`
	SourceRecordID             *string                `json:"source_record_id"`
	SourceName                 string                 `json:"source_name"`
	EntityID                   string                 `json:"entity_id"`
	SourceIndex                int                    `json:"source_index"`
	VehicleEntityID            string                 `json:"vehicle_entity_id"`
	VehicleMatch               realtime.VehicleMatch  `json:"vehicle_match"`
	ShapeID                    *string                `json:"shape_id"`
	PatternID                  *string                `json:"service_pattern_id"`
	ScheduleVersion            *string                `json:"schedule_version"`
	Latitude                   *float64               `json:"latitude"`
	Longitude                  *float64               `json:"longitude"`
	ProjectedAt                *string                `json:"projected_at"`
	PositionBasis              realtime.PositionBasis `json:"position_basis"`
	MovementState              realtime.MovementState `json:"movement_state"`
	SourceTimestamp            *string                `json:"source_timestamp"`
	SourceAgeSeconds           *float64               `json:"source_age_seconds"`
	ReceivedAt                 *string                `json:"received_at"`
	TripUpdateTimestamp        *string                `json:"trip_update_timestamp"`
	TripUpdateTimestampPresent bool                   `json:"trip_update_timestamp_present"`
	TripUpdateFreshness        realtime.Freshness     `json:"trip_update_freshness"`
	VehicleTimestamp           *string                `json:"vehicle_timestamp"`
	VehicleTimestampPresent    bool                   `json:"vehicle_timestamp_present"`
	VehicleFreshness           realtime.Freshness     `json:"vehicle_freshness"`
	Freshness                  realtime.Freshness     `json:"freshness"`
	ObservationTime            *string                `json:"observation_time"`
	ObservationAgeSeconds      *float64               `json:"observation_age_seconds"`
	ObservationBasis           string                 `json:"observation_basis"`
	ScheduleValidatedAt        *string                `json:"schedule_validated_at"`
	ScheduleAgeSeconds         *float64               `json:"schedule_age_seconds"`
	InterpolationMethod        string                 `json:"interpolation_method"`
	ReconciliationOutcome      reconcile.Outcome      `json:"reconciliation_outcome"`
	ReconciliationReason       string                 `json:"reconciliation_reason"`
	FallbackReason             *string                `json:"fallback_reason"`
	NoPositionReason           *string                `json:"no_position_reason"`
}

func positionDTO(p projection.Position, now time.Time) positionResponse {
	return positionResponse{RouteID: p.RouteID, Direction: p.Direction, DirectionID: p.DirectionID,
		TripID: nullableString(p.StaticTripID), ServiceDate: nullableString(p.ServiceDate), TripInstanceID: nullableString(p.TripInstanceID),
		RealtimeTripID: p.RealtimeTripID, RealtimeServiceDate: p.RealtimeServiceDate, SourceRecordID: nullableString(p.SourceRecordID), SourceName: p.SourceName, EntityID: p.EntityID, SourceIndex: p.SourceIndex,
		VehicleEntityID: p.VehicleEntityID, VehicleMatch: p.VehicleMatch, ShapeID: nullableString(p.ShapeID), PatternID: nullableString(p.PatternID),
		ScheduleVersion: nullableString(p.ScheduleVersion), Latitude: p.Latitude, Longitude: p.Longitude, ProjectedAt: timestamp(p.ProjectionTime),
		PositionBasis: p.PositionBasis, MovementState: p.MovementState, SourceTimestamp: timestamp(p.SourceTimestamp), SourceAgeSeconds: ageSeconds(p.SourceTimestamp, now),
		ReceivedAt: timestamp(p.ReceivedAt), TripUpdateTimestamp: timestamp(p.TripUpdateTimestamp), TripUpdateTimestampPresent: p.TripUpdateTimestampPresent,
		TripUpdateFreshness: p.TripUpdateFreshness, VehicleTimestamp: timestamp(p.VehicleTimestamp), VehicleTimestampPresent: p.VehicleTimestampPresent,
		VehicleFreshness: p.VehicleFreshness, Freshness: p.Freshness, ObservationTime: timestamp(p.ObservationTime), ObservationAgeSeconds: ageSeconds(p.ObservationTime, now),
		ObservationBasis: p.ObservationBasis, ScheduleValidatedAt: timestamp(p.ScheduleValidatedAt), ScheduleAgeSeconds: ageSeconds(p.ScheduleValidatedAt, now),
		InterpolationMethod: p.InterpolationMethod, ReconciliationOutcome: p.ReconciliationOutcome, ReconciliationReason: p.ReconciliationReason,
		FallbackReason: nullableString(p.FallbackReason), NoPositionReason: nullableString(p.NullReason)}
}

func (h *Handler) positions(w http.ResponseWriter, routeID string, now time.Time) {
	bundle := h.bundle()
	states := h.bundleSources(bundle, routeID, now)
	staleAfter := h.staleAfter
	if bundle != nil {
		staleAfter = bundle.StaleAfter()
	}
	asOf, freshness, coverage := summarize(states, now, staleAfter)
	if bundle == nil || coverage == CoverageUnavailable {
		writeJSON(w, http.StatusServiceUnavailable, sourceErrorResponse("realtime source for route is unavailable", coverage, states))
		return
	}
	positions := projection.Positions(bundle, routeID, now)
	if coverage == CoveragePartial && len(positions) == 0 {
		writeJSON(w, http.StatusServiceUnavailable, sourceErrorResponse("realtime source for route is unavailable", coverage, states))
		return
	}
	response := positionsResponse{scheduleResponse: scheduleDTO(bundle.MetadataAt(now), now), AsOf: timestamp(asOf), ProjectedAt: timestamp(now),
		BundleEvaluatedAt: timestamp(bundle.Metadata().EvaluatedAt), Freshness: freshness, Coverage: coverage, Sources: sourceDTOs(states), Trains: make([]positionResponse, len(positions))}
	for i, position := range positions {
		response.Trains[i] = positionDTO(position, now)
	}
	writeJSON(w, http.StatusOK, response)
}

// Read provenance from the same bundle used for projection, even if source
// stores have independently published a newer feed since reconciliation.
func (h *Handler) bundleSources(bundle *reconcile.Bundle, routeID string, now time.Time) []sourceSnapshot {
	var all, owned []sourceSnapshot
	if bundle != nil {
		for _, source := range bundle.Sources() {
			state := sourceSnapshot{source: Source{Name: source.Name, Routes: source.Routes},
				snapshot: realtime.Snapshot{SourceTimestamp: source.SourceTimestamp, Freshness: bundle.FreshnessAt(source.SourceTimestamp, now)}}
			all = append(all, state)
			if slices.Contains(source.Routes, routeID) {
				owned = append(owned, state)
			}
		}
	} else {
		for _, source := range h.sourcesForRoute(routeID) {
			all = append(all, sourceSnapshot{source: source, snapshot: realtime.Snapshot{Freshness: realtime.FreshnessUnavailable}})
		}
	}
	if len(owned) > 0 {
		return owned
	}
	return all
}

func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func ageSeconds(value, now time.Time) *float64 {
	if value.IsZero() {
		return nil
	}
	age := now.Sub(value).Seconds()
	return &age
}

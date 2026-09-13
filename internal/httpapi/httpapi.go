// Package httpapi exposes realtime state over HTTP.
package httpapi

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"hol-up-prototype/internal/realtime"
	"hol-up-prototype/internal/telemetry"
)

//go:embed openapi.yaml
var openAPI []byte

type Handler struct {
	sources    []Source
	metrics    *telemetry.Store
	now        func() time.Time
	staleAfter time.Duration
}

// Source identifies one independently published realtime feed and the routes
// it is expected to cover.
type Source struct {
	Name   string
	Routes []string
	Store  *realtime.Store
}

// NewHandler constructs the public HTTP handler.
func NewHandler(sources []Source, metrics *telemetry.Store, now func() time.Time, staleAfter time.Duration) http.Handler {
	return &Handler{sources: append([]Source(nil), sources...), metrics: metrics, now: now, staleAfter: staleAfter}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse("method_not_allowed", "method must be GET"))
		return
	}

	switch r.URL.Path {
	case "/openapi.yaml":
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		_, _ = w.Write(openAPI)
		return
	case "/healthz":
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	now := h.now()
	switch r.URL.Path {
	case "/readyz":
		states := h.loadSources(h.sources, now, false)
		status := http.StatusServiceUnavailable
		freshness := readinessFreshness(states)
		_, _, coverage := summarize(states, now, h.staleAfter)
		response := readinessResponse{Status: "not_ready", Freshness: freshness, Coverage: coverage, Sources: sourceDTOs(states)}
		if freshness == realtime.FreshnessFresh {
			status = http.StatusOK
			response.Status = "ready"
		}
		writeJSON(w, status, response)
	case "/api/v1/routes":
		states := h.loadSources(h.sources, now, true)
		asOf, freshness, coverage := summarize(states, now, h.staleAfter)
		if coverage == CoverageUnavailable {
			writeJSON(w, http.StatusServiceUnavailable, sourceErrorResponse("realtime sources are unavailable", coverage, states))
			return
		}
		writeJSON(w, http.StatusOK, routesResponse{AsOf: timestamp(asOf), Freshness: freshness, Routes: routes(states), Coverage: coverage, Sources: sourceDTOs(states)})
	case "/api/v1/trains":
		routeID := r.URL.Query().Get("route_id")
		if routeID == "" {
			writeJSON(w, http.StatusBadRequest, errorResponse("missing_route_id", "route_id is required"))
			return
		}
		states := h.loadSources(h.sourcesForRoute(routeID), now, true)
		asOf, freshness, coverage := summarize(states, now, h.staleAfter)
		matching := make([]realtime.Train, 0)
		observedRoute := false
		for _, state := range states {
			for _, train := range state.snapshot.Trains {
				if train.RouteID == routeID {
					observedRoute = true
					if train.Active() {
						matching = append(matching, train)
					}
				}
			}
		}
		if coverage == CoverageUnavailable || (coverage == CoveragePartial && !observedRoute) {
			writeJSON(w, http.StatusServiceUnavailable, sourceErrorResponse("realtime source for route is unavailable", coverage, states))
			return
		}
		sort.Slice(matching, func(left, right int) bool {
			if matching[left].TripID != matching[right].TripID {
				return matching[left].TripID < matching[right].TripID
			}
			return matching[left].EntityID < matching[right].EntityID
		})
		trains := make([]trainResponse, len(matching))
		for index := range matching {
			trains[index] = trainDTO(matching[index])
		}
		writeJSON(w, http.StatusOK, trainsResponse{AsOf: timestamp(asOf), Freshness: freshness, Trains: trains, Coverage: coverage, Sources: sourceDTOs(states)})
	case "/metrics":
		writeMetrics(w, h.loadSources(h.sources, now, false), h.metrics.Snapshot(), now, h.staleAfter)
	default:
		writeJSON(w, http.StatusNotFound, errorResponse("not_found", "endpoint not found"))
	}
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorEnvelope struct {
	Error errorDetail `json:"error"`
}

func errorResponse(code, message string) errorEnvelope {
	return errorEnvelope{Error: errorDetail{Code: code, Message: message}}
}

func sourceErrorResponse(message string, coverage Coverage, states []sourceSnapshot) sourceErrorEnvelope {
	return sourceErrorEnvelope{
		Error:    errorDetail{Code: "source_unavailable", Message: message},
		Coverage: coverage,
		Sources:  sourceDTOs(states),
	}
}

type readinessResponse struct {
	Status    string             `json:"status"`
	Freshness realtime.Freshness `json:"freshness"`
	Coverage  Coverage           `json:"coverage"`
	Sources   []sourceResponse   `json:"sources"`
}

type routesResponse struct {
	AsOf      *string            `json:"as_of"`
	Freshness realtime.Freshness `json:"freshness"`
	Routes    []string           `json:"routes"`
	Coverage  Coverage           `json:"coverage"`
	Sources   []sourceResponse   `json:"sources"`
}

type trainsResponse struct {
	AsOf      *string            `json:"as_of"`
	Freshness realtime.Freshness `json:"freshness"`
	Trains    []trainResponse    `json:"trains"`
	Coverage  Coverage           `json:"coverage"`
	Sources   []sourceResponse   `json:"sources"`
}

type Coverage string

const (
	CoverageComplete    Coverage = "complete"
	CoveragePartial     Coverage = "partial"
	CoverageUnavailable Coverage = "unavailable"
)

type sourceResponse struct {
	Name            string             `json:"name"`
	Routes          []string           `json:"routes"`
	SourceTimestamp *string            `json:"source_timestamp"`
	Freshness       realtime.Freshness `json:"freshness"`
}

type sourceErrorEnvelope struct {
	Error    errorDetail      `json:"error"`
	Coverage Coverage         `json:"coverage"`
	Sources  []sourceResponse `json:"sources"`
}

type sourceSnapshot struct {
	source   Source
	snapshot realtime.Snapshot
	trains   int
}

// trainResponse is the HTTP representation of a realtime train. It keeps
// transport naming and timestamp formatting outside the domain package.
type trainResponse struct {
	EntityID               string                 `json:"entity_id"`
	RouteID                string                 `json:"route_id"`
	TripID                 string                 `json:"trip_id"`
	ServiceDate            string                 `json:"service_date"`
	StartTime              string                 `json:"start_time"`
	DirectionID            *uint32                `json:"direction_id"`
	Direction              realtime.Direction     `json:"direction"`
	NextStopID             string                 `json:"next_stop_id"`
	NextStopSequence       uint32                 `json:"next_stop_sequence"`
	PredictedAt            *string                `json:"predicted_at"`
	PredictionDelaySeconds *int32                 `json:"prediction_delay_seconds"`
	PositionBasis          realtime.PositionBasis `json:"position_basis"`
	MovementState          realtime.MovementState `json:"movement_state"`
	Freshness              realtime.Freshness     `json:"freshness"`
	SourceTimestamp        *string                `json:"source_timestamp"`
	SourceName             string                 `json:"source_name"`
	ReceivedAt             *string                `json:"received_at"`
	TripUpdateTimestamp    *string                `json:"trip_update_timestamp"`
	TripUpdateFreshness    realtime.Freshness     `json:"trip_update_freshness"`
	VehicleEntityID        string                 `json:"vehicle_entity_id"`
	VehicleTimestamp       *string                `json:"vehicle_timestamp"`
	VehicleFreshness       realtime.Freshness     `json:"vehicle_freshness"`
	VehicleMatch           realtime.VehicleMatch  `json:"vehicle_match"`
}

func trainDTO(train realtime.Train) trainResponse {
	return trainResponse{
		EntityID:               train.EntityID,
		RouteID:                train.RouteID,
		TripID:                 train.TripID,
		ServiceDate:            train.ServiceDate,
		StartTime:              train.StartTime,
		DirectionID:            train.DirectionID,
		Direction:              train.Direction,
		NextStopID:             train.NextStopID,
		NextStopSequence:       train.NextStopSequence,
		PredictedAt:            timestamp(train.PredictedAt),
		PredictionDelaySeconds: train.PredictionDelaySeconds,
		PositionBasis:          train.PositionBasis,
		MovementState:          train.MovementState,
		Freshness:              train.Freshness,
		SourceTimestamp:        timestamp(train.SourceTimestamp),
		SourceName:             train.SourceName,
		ReceivedAt:             timestamp(train.ReceivedAt),
		TripUpdateTimestamp:    timestamp(train.TripUpdateTimestamp),
		TripUpdateFreshness:    train.TripUpdateFreshness,
		VehicleEntityID:        train.VehicleEntityID,
		VehicleTimestamp:       timestamp(train.VehicleTimestamp),
		VehicleFreshness:       train.VehicleFreshness,
		VehicleMatch:           train.VehicleMatch,
	}
}

func (h *Handler) sourcesForRoute(routeID string) []Source {
	var matched []Source
	for _, source := range h.sources {
		for _, configuredRoute := range source.Routes {
			if routeID == configuredRoute {
				matched = append(matched, source)
				break
			}
		}
	}
	if len(matched) > 0 {
		return matched
	}
	return h.sources
}

func (h *Handler) loadSources(sources []Source, now time.Time, includeTrains bool) []sourceSnapshot {
	states := make([]sourceSnapshot, len(sources))
	for index, source := range sources {
		states[index].source = source
		if source.Store == nil {
			states[index].snapshot.Freshness = realtime.FreshnessUnavailable
			continue
		}
		if includeTrains {
			states[index].snapshot = source.Store.Load(now, h.staleAfter)
			continue
		}
		states[index].snapshot.SourceTimestamp, states[index].snapshot.Freshness, states[index].trains = source.Store.Status(now, h.staleAfter)
	}
	return states
}

func summarize(states []sourceSnapshot, now time.Time, staleAfter time.Duration) (time.Time, realtime.Freshness, Coverage) {
	var oldest time.Time
	available := 0
	for _, state := range states {
		if state.snapshot.SourceTimestamp.IsZero() {
			continue
		}
		available++
		if oldest.IsZero() || state.snapshot.SourceTimestamp.Before(oldest) {
			oldest = state.snapshot.SourceTimestamp
		}
	}

	coverage := CoverageUnavailable
	if available == len(states) && available > 0 {
		coverage = CoverageComplete
	} else if available > 0 {
		coverage = CoveragePartial
	}
	return oldest, realtime.FreshnessAt(oldest, now, staleAfter), coverage
}

func readinessFreshness(states []sourceSnapshot) realtime.Freshness {
	result := realtime.FreshnessUnavailable
	for _, state := range states {
		if state.snapshot.Freshness == realtime.FreshnessFresh {
			return realtime.FreshnessFresh
		}
		if state.snapshot.Freshness == realtime.FreshnessStale {
			result = realtime.FreshnessStale
		}
	}
	return result
}

func sourceDTOs(states []sourceSnapshot) []sourceResponse {
	result := make([]sourceResponse, len(states))
	for index, state := range states {
		routes := append([]string(nil), state.source.Routes...)
		sort.Strings(routes)
		result[index] = sourceResponse{
			Name:            state.source.Name,
			Routes:          routes,
			SourceTimestamp: timestamp(state.snapshot.SourceTimestamp),
			Freshness:       state.snapshot.Freshness,
		}
	}
	return result
}

func routes(states []sourceSnapshot) []string {
	seen := make(map[string]struct{})
	for _, state := range states {
		for _, train := range state.snapshot.Trains {
			if train.RouteID != "" {
				seen[train.RouteID] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(seen))
	for routeID := range seen {
		result = append(result, routeID)
	}
	sort.Strings(result)
	return result
}

func timestamp(value time.Time) *string {
	if value.IsZero() {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339)
	return &formatted
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeMetrics(w http.ResponseWriter, states []sourceSnapshot, feeds []telemetry.Feed, now time.Time, staleAfter time.Duration) {
	ready := 0
	if readinessFreshness(states) == realtime.FreshnessFresh {
		ready = 1
	}
	trainCount := 0
	for _, state := range states {
		trainCount += state.trains
	}
	oldest, _, _ := summarize(states, now, staleAfter)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = fmt.Fprintf(w, "# HELP holup_realtime_ready Whether at least one realtime feed is fresh.\n# TYPE holup_realtime_ready gauge\nholup_realtime_ready %d\n# HELP holup_realtime_trains Number of trains in available feed snapshots.\n# TYPE holup_realtime_trains gauge\nholup_realtime_trains %d\n# HELP holup_realtime_snapshot_timestamp_seconds Oldest available feed source timestamp.\n# TYPE holup_realtime_snapshot_timestamp_seconds gauge\nholup_realtime_snapshot_timestamp_seconds %d\n", ready, trainCount, unix(oldest))
	_, _ = fmt.Fprint(w, "# HELP holup_realtime_feed_fresh Whether a feed's last-known-good snapshot is fresh.\n# TYPE holup_realtime_feed_fresh gauge\n# HELP holup_realtime_feed_source_timestamp_seconds Source timestamp of a feed's last-known-good snapshot.\n# TYPE holup_realtime_feed_source_timestamp_seconds gauge\n# HELP holup_realtime_feed_last_success_timestamp_seconds Local time of a feed's last successful refresh.\n# TYPE holup_realtime_feed_last_success_timestamp_seconds gauge\n# HELP holup_realtime_feed_trains Number of trains in a feed's last-known-good snapshot.\n# TYPE holup_realtime_feed_trains gauge\n# HELP holup_realtime_feed_refreshes_total Completed feed refreshes by outcome.\n# TYPE holup_realtime_feed_refreshes_total counter\n# HELP holup_realtime_feed_last_refresh_duration_seconds Duration of the most recent feed refresh.\n# TYPE holup_realtime_feed_last_refresh_duration_seconds gauge\n")
	for _, feed := range feeds {
		fresh := 0
		if realtime.FreshnessAt(feed.SourceTimestamp, now, staleAfter) == realtime.FreshnessFresh {
			fresh = 1
		}
		_, _ = fmt.Fprintf(w, "holup_realtime_feed_fresh{feed=%q} %d\nholup_realtime_feed_source_timestamp_seconds{feed=%q} %d\nholup_realtime_feed_last_success_timestamp_seconds{feed=%q} %d\nholup_realtime_feed_trains{feed=%q} %d\nholup_realtime_feed_refreshes_total{feed=%q,outcome=%q} %d\nholup_realtime_feed_refreshes_total{feed=%q,outcome=%q} %d\nholup_realtime_feed_last_refresh_duration_seconds{feed=%q} %g\n", feed.Name, fresh, feed.Name, unix(feed.SourceTimestamp), feed.Name, unix(feed.LastSuccess), feed.Name, feed.TrainCount, feed.Name, "success", feed.RefreshSuccesses, feed.Name, "error", feed.RefreshErrors, feed.Name, feed.LastRefreshDuration.Seconds())
	}
}

func unix(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.Unix()
}

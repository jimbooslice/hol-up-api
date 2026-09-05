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
	store      *realtime.Store
	metrics    *telemetry.Store
	now        func() time.Time
	staleAfter time.Duration
}

// NewHandler constructs the public HTTP handler.
func NewHandler(store *realtime.Store, metrics *telemetry.Store, now func() time.Time, staleAfter time.Duration) http.Handler {
	return &Handler{store: store, metrics: metrics, now: now, staleAfter: staleAfter}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse("method_not_allowed", "method must be GET"))
		return
	}

	snapshot := h.store.Load(h.now(), h.staleAfter)
	switch r.URL.Path {
	case "/openapi.yaml":
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		_, _ = w.Write(openAPI)
	case "/healthz":
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	case "/readyz":
		status := http.StatusServiceUnavailable
		response := readinessResponse{Status: "not_ready", Freshness: snapshot.Freshness}
		if snapshot.Freshness == realtime.FreshnessFresh {
			status = http.StatusOK
			response.Status = "ready"
		}
		writeJSON(w, status, response)
	case "/api/v1/routes":
		if snapshot.Freshness == realtime.FreshnessUnavailable {
			writeJSON(w, http.StatusServiceUnavailable, errorResponse("source_unavailable", "realtime source is unavailable"))
			return
		}
		writeJSON(w, http.StatusOK, routesResponse{AsOf: timestamp(snapshot.SourceTimestamp), Freshness: snapshot.Freshness, Routes: routes(snapshot.Trains)})
	case "/api/v1/trains":
		routeID := r.URL.Query().Get("route_id")
		if routeID == "" {
			writeJSON(w, http.StatusBadRequest, errorResponse("missing_route_id", "route_id is required"))
			return
		}
		if snapshot.Freshness == realtime.FreshnessUnavailable {
			writeJSON(w, http.StatusServiceUnavailable, errorResponse("source_unavailable", "realtime source is unavailable"))
			return
		}
		trains := make([]trainResponse, 0)
		for _, train := range snapshot.Trains {
			if train.RouteID == routeID {
				trains = append(trains, trainDTO(train))
			}
		}
		writeJSON(w, http.StatusOK, trainsResponse{AsOf: timestamp(snapshot.SourceTimestamp), Freshness: snapshot.Freshness, Trains: trains})
	case "/metrics":
		writeMetrics(w, snapshot, h.metrics.Snapshot(), h.now(), h.staleAfter)
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

type readinessResponse struct {
	Status    string             `json:"status"`
	Freshness realtime.Freshness `json:"freshness"`
}

type routesResponse struct {
	AsOf      *string            `json:"as_of"`
	Freshness realtime.Freshness `json:"freshness"`
	Routes    []string           `json:"routes"`
}

type trainsResponse struct {
	AsOf      *string            `json:"as_of"`
	Freshness realtime.Freshness `json:"freshness"`
	Trains    []trainResponse    `json:"trains"`
}

// trainResponse is the HTTP representation of a realtime train. It keeps
// transport naming and timestamp formatting outside the domain package.
type trainResponse struct {
	EntityID               string                 `json:"entity_id"`
	RouteID                string                 `json:"route_id"`
	TripID                 string                 `json:"trip_id"`
	ServiceDate            string                 `json:"service_date"`
	DirectionID            *uint32                `json:"direction_id"`
	Direction              realtime.Direction     `json:"direction"`
	NextStopID             string                 `json:"next_stop_id"`
	NextStopSequence       uint32                 `json:"next_stop_sequence"`
	PredictedAt            *string                `json:"predicted_at"`
	PredictionDelaySeconds int32                  `json:"prediction_delay_seconds"`
	PositionBasis          realtime.PositionBasis `json:"position_basis"`
	MovementState          realtime.MovementState `json:"movement_state"`
	Freshness              realtime.Freshness     `json:"freshness"`
	SourceTimestamp        *string                `json:"source_timestamp"`
}

func trainDTO(train realtime.Train) trainResponse {
	return trainResponse{
		EntityID:               train.EntityID,
		RouteID:                train.RouteID,
		TripID:                 train.TripID,
		ServiceDate:            train.ServiceDate,
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
	}
}

func routes(trains []realtime.Train) []string {
	seen := make(map[string]struct{})
	for _, train := range trains {
		if train.RouteID != "" {
			seen[train.RouteID] = struct{}{}
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

func writeMetrics(w http.ResponseWriter, snapshot realtime.Snapshot, feeds []telemetry.Feed, now time.Time, staleAfter time.Duration) {
	ready := 0
	if snapshot.Freshness == realtime.FreshnessFresh {
		ready = 1
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = fmt.Fprintf(w, "# HELP holup_realtime_ready Whether realtime state is fresh.\n# TYPE holup_realtime_ready gauge\nholup_realtime_ready %d\n# HELP holup_realtime_trains Number of trains in the latest snapshot.\n# TYPE holup_realtime_trains gauge\nholup_realtime_trains %d\n# HELP holup_realtime_snapshot_timestamp_seconds Source timestamp of the aggregate snapshot.\n# TYPE holup_realtime_snapshot_timestamp_seconds gauge\nholup_realtime_snapshot_timestamp_seconds %d\n", ready, len(snapshot.Trains), unix(snapshot.SourceTimestamp))
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

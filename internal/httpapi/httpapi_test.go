package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hol-up-prototype/internal/realtime"
)

func TestEndpointsUseFreshSnapshot(t *testing.T) {
	now := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	store := realtime.NewStore()
	if err := store.Replace(realtime.Snapshot{SourceTimestamp: now, Trains: []realtime.Train{
		{EntityID: "z", RouteID: "C", TripID: "trip-z"},
		{EntityID: "a", RouteID: "A", TripID: "trip-a"},
		{EntityID: "b", RouteID: "A", TripID: "trip-b"},
	}}); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store, func() time.Time { return now }, time.Minute)

	for _, test := range []struct {
		path string
		want string
	}{
		{"/healthz", `{"status":"ok"}`},
		{"/readyz", `{"status":"ready","freshness":"fresh"}`},
		{"/api/v1/routes", `"routes":["A","C"]`},
		{"/api/v1/trains?route_id=A", `{"as_of":"2026-09-02T12:00:00Z","freshness":"fresh","trains":[`},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))
		if recorder.Code != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200", test.path, recorder.Code)
		}
		if got := recorder.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
			t.Errorf("GET %s content type = %q", test.path, got)
		}
		if body := recorder.Body.String(); !strings.Contains(body, test.want) {
			t.Errorf("GET %s body = %s, want %s", test.path, body, test.want)
		}
	}
}

func TestReadyAndErrors(t *testing.T) {
	now := time.Date(2026, time.September, 2, 12, 1, 0, 0, time.UTC)
	handler := NewHandler(realtime.NewStore(), func() time.Time { return now }, time.Minute)

	for _, test := range []struct {
		method, path string
		wantStatus   int
		wantBody     string
	}{
		{http.MethodGet, "/readyz", http.StatusServiceUnavailable, `{"status":"not_ready","freshness":"unavailable"}`},
		{http.MethodGet, "/api/v1/trains", http.StatusBadRequest, `{"error":{"code":"missing_route_id","message":"route_id is required"}}`},
		{http.MethodGet, "/api/v1/routes", http.StatusServiceUnavailable, `{"error":{"code":"source_unavailable","message":"realtime source is unavailable"}}`},
		{http.MethodGet, "/api/v1/trains?route_id=A", http.StatusServiceUnavailable, `{"error":{"code":"source_unavailable","message":"realtime source is unavailable"}}`},
		{http.MethodPost, "/healthz", http.StatusMethodNotAllowed, `{"error":{"code":"method_not_allowed","message":"method must be GET"}}`},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, nil))
		if recorder.Code != test.wantStatus || strings.TrimSpace(recorder.Body.String()) != test.wantBody {
			t.Errorf("%s %s = (%d, %s), want (%d, %s)", test.method, test.path, recorder.Code, recorder.Body.String(), test.wantStatus, test.wantBody)
		}
	}
}

func TestTrainDTOUsesSnakeCaseAndNullTimestamps(t *testing.T) {
	now := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	store := realtime.NewStore()
	if err := store.Replace(realtime.Snapshot{SourceTimestamp: now, Trains: []realtime.Train{{
		EntityID:               "vehicle-a",
		RouteID:                "A",
		TripID:                 "trip-a",
		ServiceDate:            "20260902",
		Direction:              realtime.DirectionNorthbound,
		NextStopID:             "A01N",
		NextStopSequence:       4,
		PredictionDelaySeconds: 60,
		PositionBasis:          realtime.PositionBasisRealtimePrediction,
		MovementState:          realtime.MovementStateInferredMoving,
	}}}); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	NewHandler(store, func() time.Time { return now }, time.Minute).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/trains?route_id=A", nil))

	want := `{"as_of":"2026-09-02T12:00:00Z","freshness":"fresh","trains":[{"entity_id":"vehicle-a","route_id":"A","trip_id":"trip-a","service_date":"20260902","direction_id":null,"direction":"northbound","next_stop_id":"A01N","next_stop_sequence":4,"predicted_at":null,"prediction_delay_seconds":60,"position_basis":"realtime_prediction","movement_state":"inferred_moving","freshness":"fresh","source_timestamp":"2026-09-02T12:00:00Z"}]}`
	body := strings.TrimSpace(recorder.Body.String())
	if body != want {
		t.Errorf("trains body = %s, want %s", body, want)
	}
	if strings.Contains(body, "EntityID") || strings.Contains(body, "SourceTimestamp") {
		t.Errorf("trains body leaked Go field names: %s", body)
	}
}

func TestStaleSnapshotRemainsAvailable(t *testing.T) {
	now := time.Date(2026, time.September, 2, 12, 2, 0, 0, time.UTC)
	store := realtime.NewStore()
	if err := store.Replace(realtime.Snapshot{SourceTimestamp: now.Add(-2 * time.Minute), Trains: []realtime.Train{{RouteID: "A", TripID: "trip-a"}}}); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(store, func() time.Time { return now }, time.Minute)
	for _, path := range []string{"/api/v1/routes", "/api/v1/trains?route_id=A"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"freshness":"stale"`) {
			t.Errorf("GET %s = (%d, %s), want stale 200", path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestMetrics(t *testing.T) {
	now := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	store := realtime.NewStore()
	if err := store.Replace(realtime.Snapshot{SourceTimestamp: now, Trains: []realtime.Train{{RouteID: "A"}}}); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	NewHandler(store, func() time.Time { return now }, time.Minute).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "holup_realtime_ready 1\n") || !strings.Contains(recorder.Body.String(), "holup_realtime_trains 1\n") {
		t.Fatalf("metrics = (%d, %s)", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/plain; version=0.0.4; charset=utf-8" {
		t.Errorf("content type = %q", got)
	}
}

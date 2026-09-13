package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hol-up-prototype/internal/realtime"
	"hol-up-prototype/internal/telemetry"
)

func TestPartialFeedResponses(t *testing.T) {
	now := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	ace, nqrw := realtime.NewStore(), realtime.NewStore()
	replace(t, ace, realtime.Snapshot{SourceTimestamp: now, Trains: []realtime.Train{
		{NextStopID: "A01N", EntityID: "a", RouteID: "A", TripID: "trip-a"},
		{NextStopID: "A01N", EntityID: "s", RouteID: "S", TripID: "trip-s"},
	}})
	handler := NewHandler(testSources(ace, nqrw), telemetry.New(), func() time.Time { return now }, time.Minute)

	readyRecorder := get(handler, "/readyz")
	var ready readinessResponse
	decode(t, readyRecorder, &ready)
	if readyRecorder.Code != http.StatusOK || ready.Status != "ready" || ready.Freshness != realtime.FreshnessFresh || ready.Coverage != CoveragePartial || len(ready.Sources) != 2 || ready.Sources[1].Freshness != realtime.FreshnessUnavailable || ready.Sources[1].SourceTimestamp != nil || strings.Join(ready.Sources[1].Routes, ",") != "N,Q,R,W" {
		t.Fatalf("partial readiness = %d/%#v", readyRecorder.Code, ready)
	}

	routesRecorder := get(handler, "/api/v1/routes")
	var routeList routesResponse
	decode(t, routesRecorder, &routeList)
	if routesRecorder.Code != http.StatusOK || routeList.Coverage != CoveragePartial || strings.Join(routeList.Routes, ",") != "A,S" || len(routeList.Sources) != 2 {
		t.Fatalf("partial routes = %d/%#v", routesRecorder.Code, routeList)
	}
	emptyACE := realtime.NewStore()
	replace(t, emptyACE, realtime.Snapshot{SourceTimestamp: now})
	emptyRoutesRecorder := get(NewHandler(testSources(emptyACE, realtime.NewStore()), telemetry.New(), func() time.Time { return now }, time.Minute), "/api/v1/routes")
	var emptyRoutes routesResponse
	decode(t, emptyRoutesRecorder, &emptyRoutes)
	if emptyRoutesRecorder.Code != http.StatusOK || emptyRoutes.Coverage != CoveragePartial || len(emptyRoutes.Routes) != 0 {
		t.Fatalf("partial empty routes = %d/%#v", emptyRoutesRecorder.Code, emptyRoutes)
	}

	aRecorder := get(handler, "/api/v1/trains?route_id=A")
	var aTrains trainsResponse
	decode(t, aRecorder, &aTrains)
	if aRecorder.Code != http.StatusOK || aTrains.Coverage != CoverageComplete || len(aTrains.Trains) != 1 || len(aTrains.Sources) != 1 || aTrains.Sources[0].Name != "ace" {
		t.Fatalf("A trains = %d/%#v", aRecorder.Code, aTrains)
	}

	sRecorder := get(handler, "/api/v1/trains?route_id=S")
	var sTrains trainsResponse
	decode(t, sRecorder, &sTrains)
	if sRecorder.Code != http.StatusOK || sTrains.Coverage != CoveragePartial || len(sTrains.Trains) != 1 || len(sTrains.Sources) != 2 {
		t.Fatalf("observed extra-route trains = %d/%#v", sRecorder.Code, sTrains)
	}

	for _, routeID := range []string{"N", "X"} {
		recorder := get(handler, "/api/v1/trains?route_id="+routeID)
		var response sourceErrorEnvelope
		decode(t, recorder, &response)
		if recorder.Code != http.StatusServiceUnavailable || response.Error.Code != "source_unavailable" {
			t.Errorf("unavailable %s trains = %d/%#v", routeID, recorder.Code, response)
		}
		if routeID == "N" && (response.Coverage != CoverageUnavailable || len(response.Sources) != 1 || response.Sources[0].Name != "nqrw") {
			t.Errorf("N source error = %#v", response)
		}
		if routeID == "X" && (response.Coverage != CoveragePartial || len(response.Sources) != 2) {
			t.Errorf("unknown-route source error = %#v", response)
		}
	}
}

func TestLoadedFeedsKeepFreshnessSeparateFromCoverage(t *testing.T) {
	now := time.Date(2026, time.September, 2, 12, 2, 0, 0, time.UTC)
	ace, nqrw := realtime.NewStore(), realtime.NewStore()
	replace(t, ace, realtime.Snapshot{SourceTimestamp: now.Add(-2 * time.Minute), Trains: []realtime.Train{{NextStopID: "A01N", RouteID: "A", TripID: "trip-a"}}})
	replace(t, nqrw, realtime.Snapshot{SourceTimestamp: now})
	handler := NewHandler(testSources(ace, nqrw), telemetry.New(), func() time.Time { return now }, time.Minute)

	readyRecorder := get(handler, "/readyz")
	var ready readinessResponse
	decode(t, readyRecorder, &ready)
	if readyRecorder.Code != http.StatusOK || ready.Freshness != realtime.FreshnessFresh || ready.Coverage != CoverageComplete {
		t.Fatalf("mixed readiness = %d/%#v", readyRecorder.Code, ready)
	}

	routesRecorder := get(handler, "/api/v1/routes")
	var routeList routesResponse
	decode(t, routesRecorder, &routeList)
	if routesRecorder.Code != http.StatusOK || routeList.Freshness != realtime.FreshnessStale || routeList.Coverage != CoverageComplete || value(routeList.AsOf) != "2026-09-02T12:00:00Z" {
		t.Fatalf("aggregate routes = %d/%#v", routesRecorder.Code, routeList)
	}

	aRecorder := get(handler, "/api/v1/trains?route_id=A")
	var aTrains trainsResponse
	decode(t, aRecorder, &aTrains)
	if aRecorder.Code != http.StatusOK || aTrains.Freshness != realtime.FreshnessStale || aTrains.Coverage != CoverageComplete || len(aTrains.Trains) != 1 {
		t.Fatalf("stale A trains = %d/%#v", aRecorder.Code, aTrains)
	}

	nRecorder := get(handler, "/api/v1/trains?route_id=N")
	var nTrains trainsResponse
	decode(t, nRecorder, &nTrains)
	if nRecorder.Code != http.StatusOK || nTrains.Freshness != realtime.FreshnessFresh || nTrains.Coverage != CoverageComplete || len(nTrains.Trains) != 0 {
		t.Fatalf("fresh empty N feed = %d/%#v", nRecorder.Code, nTrains)
	}

	unknownRecorder := get(handler, "/api/v1/trains?route_id=X")
	var unknown trainsResponse
	decode(t, unknownRecorder, &unknown)
	if unknownRecorder.Code != http.StatusOK || unknown.Coverage != CoverageComplete || unknown.Freshness != realtime.FreshnessStale || len(unknown.Trains) != 0 {
		t.Fatalf("fully covered unknown route = %d/%#v", unknownRecorder.Code, unknown)
	}

	staleOnlyRecorder := get(NewHandler(testSources(ace, realtime.NewStore()), telemetry.New(), func() time.Time { return now }, time.Minute), "/readyz")
	var staleOnly readinessResponse
	decode(t, staleOnlyRecorder, &staleOnly)
	if staleOnlyRecorder.Code != http.StatusServiceUnavailable || staleOnly.Freshness != realtime.FreshnessStale || staleOnly.Coverage != CoveragePartial {
		t.Fatalf("stale-only readiness = %d/%#v", staleOnlyRecorder.Code, staleOnly)
	}
}

func TestNoSourcesAndRequestErrors(t *testing.T) {
	now := time.Date(2026, time.September, 2, 12, 1, 0, 0, time.UTC)
	handler := NewHandler(testSources(realtime.NewStore(), realtime.NewStore()), telemetry.New(), func() time.Time { return now }, time.Minute)

	readyRecorder := get(handler, "/readyz")
	var ready readinessResponse
	decode(t, readyRecorder, &ready)
	if readyRecorder.Code != http.StatusServiceUnavailable || ready.Status != "not_ready" || ready.Freshness != realtime.FreshnessUnavailable || ready.Coverage != CoverageUnavailable || len(ready.Sources) != 2 {
		t.Fatalf("empty readiness = %d/%#v", readyRecorder.Code, ready)
	}

	for _, path := range []string{"/api/v1/routes", "/api/v1/trains?route_id=A"} {
		recorder := get(handler, path)
		var response sourceErrorEnvelope
		decode(t, recorder, &response)
		if recorder.Code != http.StatusServiceUnavailable || response.Error.Code != "source_unavailable" || response.Coverage != CoverageUnavailable {
			t.Errorf("GET %s = %d/%#v", path, recorder.Code, response)
		}
	}

	recorder := get(handler, "/api/v1/trains")
	if recorder.Code != http.StatusBadRequest || strings.TrimSpace(recorder.Body.String()) != `{"error":{"code":"missing_route_id","message":"route_id is required"}}` {
		t.Errorf("missing route = %d/%s", recorder.Code, recorder.Body.String())
	}
}

func TestHealthAndOpenAPIBypassRealtimeState(t *testing.T) {
	handler := NewHandler(nil, telemetry.New(), func() time.Time { panic("now called") }, time.Minute)

	health := get(handler, "/healthz")
	if health.Code != http.StatusOK || strings.TrimSpace(health.Body.String()) != `{"status":"ok"}` {
		t.Fatalf("health = %d/%s", health.Code, health.Body.String())
	}

	openAPI := get(handler, "/openapi.yaml")
	if openAPI.Code != http.StatusOK || openAPI.Header().Get("Content-Type") != "application/yaml; charset=utf-8" {
		t.Fatalf("OpenAPI = %d/%s", openAPI.Code, openAPI.Body.String())
	}
	for _, want := range []string{"openapi: 3.1.1", "coverage:", "SourceError:"} {
		if !strings.Contains(openAPI.Body.String(), want) {
			t.Errorf("OpenAPI body missing %q", want)
		}
	}

	method := httptest.NewRecorder()
	handler.ServeHTTP(method, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	if method.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST health status = %d", method.Code)
	}
}

func TestTrainDTOUsesSnakeCaseAndNullTimestamps(t *testing.T) {
	now := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	store := realtime.NewStore()
	replace(t, store, realtime.Snapshot{SourceTimestamp: now, Trains: []realtime.Train{{
		EntityID:               "vehicle-a",
		RouteID:                "A",
		TripID:                 "trip-a",
		ServiceDate:            "20260902",
		Direction:              realtime.DirectionNorthbound,
		NextStopID:             "A01N",
		NextStopSequence:       4,
		PredictionDelaySeconds: new(int32(60)),
		PositionBasis:          realtime.PositionBasisRealtimePrediction,
		MovementState:          realtime.MovementStateInferredMoving,
	}}})
	recorder := get(NewHandler([]Source{{Name: "ace", Routes: []string{"A", "C", "E"}, Store: store}}, telemetry.New(), func() time.Time { return now }, time.Minute), "/api/v1/trains?route_id=A")

	want := `{"as_of":"2026-09-02T12:00:00Z","freshness":"fresh","trains":[{"entity_id":"vehicle-a","route_id":"A","trip_id":"trip-a","service_date":"20260902","start_time":"","direction_id":null,"direction":"northbound","next_stop_id":"A01N","next_stop_sequence":4,"predicted_at":null,"prediction_delay_seconds":60,"position_basis":"realtime_prediction","movement_state":"unknown","freshness":"fresh","source_timestamp":"2026-09-02T12:00:00Z","source_name":"","received_at":null,"trip_update_timestamp":null,"trip_update_freshness":"unavailable","vehicle_entity_id":"","vehicle_timestamp":null,"vehicle_freshness":"unavailable","vehicle_match":"unmatched"}],"coverage":"complete","sources":[{"name":"ace","routes":["A","C","E"],"source_timestamp":"2026-09-02T12:00:00Z","freshness":"fresh"}]}`
	body := strings.TrimSpace(recorder.Body.String())
	if body != want {
		t.Errorf("trains body = %s, want %s", body, want)
	}
	if strings.Contains(body, "EntityID") || strings.Contains(body, "SourceTimestamp") {
		t.Errorf("trains body leaked Go field names: %s", body)
	}
}

func TestMetricsUseAvailableFeedsAndAnyFreshReadiness(t *testing.T) {
	now := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	ace, nqrw := realtime.NewStore(), realtime.NewStore()
	replace(t, ace, realtime.Snapshot{SourceTimestamp: now, Trains: []realtime.Train{{NextStopID: "A01N", RouteID: "A"}}})
	replace(t, nqrw, realtime.Snapshot{SourceTimestamp: now.Add(-2 * time.Minute), Trains: []realtime.Train{{NextStopID: "A01N", RouteID: "N"}, {NextStopID: "A01N", RouteID: "Q"}}})
	metrics := telemetry.New("ace", "nqrw")
	// Deliberately stale telemetry proves aggregate gauges read the source stores.
	metrics.Record("ace", true, 250*time.Millisecond, now, now.Add(-10*time.Minute), 7)
	metrics.Record("nqrw", false, 500*time.Millisecond, now, now.Add(-20*time.Minute), 11)
	recorder := get(NewHandler(testSources(ace, nqrw), metrics, func() time.Time { return now }, time.Minute), "/metrics")
	for _, want := range []string{
		"holup_realtime_ready 1\n",
		"holup_realtime_trains 3\n",
		"holup_realtime_snapshot_timestamp_seconds 1788350280\n",
		"holup_realtime_feed_fresh{feed=\"ace\"} 0\n",
		"holup_realtime_feed_fresh{feed=\"nqrw\"} 0\n",
		"holup_realtime_feed_refreshes_total{feed=\"ace\",outcome=\"success\"} 1\n",
		"holup_realtime_feed_refreshes_total{feed=\"nqrw\",outcome=\"error\"} 1\n",
	} {
		if !strings.Contains(recorder.Body.String(), want) {
			t.Errorf("metrics missing %q: %s", want, recorder.Body.String())
		}
	}
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("metrics = %d/%s", recorder.Code, recorder.Body.String())
	}

	staleACE, staleNQRW := realtime.NewStore(), realtime.NewStore()
	replace(t, staleACE, realtime.Snapshot{SourceTimestamp: now.Add(-2 * time.Minute)})
	replace(t, staleNQRW, realtime.Snapshot{SourceTimestamp: now.Add(-3 * time.Minute)})
	staleMetrics := telemetry.New("ace", "nqrw")
	staleMetrics.Record("ace", true, time.Millisecond, now, now.Add(-2*time.Minute), 0)
	staleMetrics.Record("nqrw", true, time.Millisecond, now, now.Add(-3*time.Minute), 0)
	staleRecorder := get(NewHandler(testSources(staleACE, staleNQRW), staleMetrics, func() time.Time { return now }, time.Minute), "/metrics")
	if !strings.Contains(staleRecorder.Body.String(), "holup_realtime_ready 0\n") {
		t.Fatalf("all-stale metrics = %s", staleRecorder.Body.String())
	}
}

func testSources(ace, nqrw *realtime.Store) []Source {
	return []Source{
		{Name: "ace", Routes: []string{"A", "C", "E"}, Store: ace},
		{Name: "nqrw", Routes: []string{"N", "Q", "R", "W"}, Store: nqrw},
	}
}

func replace(t *testing.T, store *realtime.Store, snapshot realtime.Snapshot) {
	t.Helper()
	if err := store.Replace(snapshot); err != nil {
		t.Fatal(err)
	}
}

func get(handler http.Handler, target string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	return recorder
}

func decode(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
}

func value(pointer *string) string {
	if pointer == nil {
		return ""
	}
	return *pointer
}

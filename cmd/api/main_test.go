package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"hol-up-prototype/internal/gen/gtfs"
	"hol-up-prototype/internal/httpapi"
	"hol-up-prototype/internal/ingest"
	"hol-up-prototype/internal/realtime"
	"hol-up-prototype/internal/telemetry"
)

func TestEnv(t *testing.T) {
	const name = "HOLUP_TEST_VALUE"
	t.Cleanup(func() { _ = os.Unsetenv(name) })
	if got := env(name, "fallback"); got != "fallback" {
		t.Fatalf("env() = %q, want fallback", got)
	}
	if err := os.Setenv(name, "configured"); err != nil {
		t.Fatal(err)
	}
	if got := env(name, "fallback"); got != "configured" {
		t.Fatalf("env() = %q, want configured", got)
	}
}

func TestConfiguredFeedsCoverMTARealtimeSubwayRoutes(t *testing.T) {
	for _, name := range []string{
		"MTA_GTFS_RT_URL",
		"MTA_GTFS_RT_BDFM_URL",
		"MTA_GTFS_RT_G_URL",
		"MTA_GTFS_RT_JZ_URL",
		"MTA_GTFS_RT_L_URL",
		"MTA_GTFS_RT_NQRW_URL",
		"MTA_GTFS_RT_NUMBERED_URL",
		"MTA_GTFS_RT_SI_URL",
	} {
		t.Setenv(name, "")
	}

	expected := map[string]struct {
		routes string
		url    string
	}{
		"ace":      {"A,C,E,H", defaultACEFeedURL},
		"bdfm":     {"B,D,F,FX,M,FS", defaultBDFMFeedURL},
		"g":        {"G", defaultGFeedURL},
		"jz":       {"J,Z", defaultJZFeedURL},
		"l":        {"L", defaultLFeedURL},
		"nqrw":     {"N,Q,R,W", defaultNQRWFeedURL},
		"numbered": {"1,2,3,4,5,6,6X,7,7X,GS", defaultNumberedFeedURL},
		"si":       {"SI,SS", defaultSIFeedURL},
	}
	feeds := configuredFeeds(http.DefaultClient, 30*time.Second, testReceiptTime)
	if len(feeds) != len(expected) {
		t.Fatalf("configured feeds = %d, want %d", len(feeds), len(expected))
	}
	owners := make(map[string]string)
	for _, feed := range feeds {
		want, ok := expected[feed.name]
		if !ok {
			t.Fatalf("unexpected feed %q", feed.name)
		}
		if got := strings.Join(feed.routes, ","); got != want.routes || feed.url != want.url {
			t.Fatalf("feed %q = routes %q, URL %q; want routes %q, URL %q", feed.name, got, feed.url, want.routes, want.url)
		}
		if feed.store == nil || feed.loader == nil {
			t.Fatalf("feed %q is not initialized", feed.name)
		}
		for _, route := range feed.routes {
			if owner, exists := owners[route]; exists {
				t.Fatalf("route %q is owned by both %q and %q", route, owner, feed.name)
			}
			owners[route] = feed.name
		}
	}
	if len(owners) != 30 {
		t.Fatalf("configured route IDs = %d, want 30", len(owners))
	}
}

func TestRefreshPublishesAvailableFeedAtStartup(t *testing.T) {
	base := time.Date(2026, time.September, 2, 17, 0, 0, 0, time.UTC)
	ace := feedServer(t, feedBytes(t, base, "A", "trip-a"))
	defer ace.Close()
	nqrw := feedServer(t, []byte("bad protobuf"))
	defer nqrw.Close()

	metrics := telemetry.New("ace", "nqrw")
	feeds := []feed{
		{name: "ace", routes: []string{"A", "C", "E"}, store: realtime.NewStore()},
		{name: "nqrw", routes: []string{"N", "Q", "R", "W"}, store: realtime.NewStore()},
	}
	feeds[0].loader = ingest.NewLoader(ace.Client(), feeds[0].store, "fixture", ace.URL, "", 30*time.Second, testReceiptTime)
	feeds[1].loader = ingest.NewLoader(nqrw.Client(), feeds[1].store, "fixture", nqrw.URL, "", 30*time.Second, testReceiptTime)

	refresh(context.Background(), slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), metrics, feeds)
	if got := feeds[0].store.Load(base, time.Minute); len(got.Trains) != 1 || got.Trains[0].RouteID != "A" {
		t.Fatalf("ACE snapshot = %#v, want available A train", got)
	}
	if _, freshness, _ := feeds[1].store.Status(base, time.Minute); freshness != realtime.FreshnessUnavailable {
		t.Fatalf("N/Q/R/W freshness = %q, want unavailable", freshness)
	}
	handler := httpapi.NewHandler(feedSources(feeds), metrics, func() time.Time { return base }, time.Minute)
	if recorder := apiRequest(handler, "/api/v1/trains?route_id=A"); recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"coverage":"complete"`) {
		t.Fatalf("partial A API = %d/%s", recorder.Code, recorder.Body.String())
	}
	if recorder := apiRequest(handler, "/api/v1/routes"); recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"coverage":"partial"`) {
		t.Fatalf("partial routes API = %d/%s", recorder.Code, recorder.Body.String())
	}
}

func TestRefreshRetainsLastKnownGoodPerFeed(t *testing.T) {
	base := time.Date(2026, time.September, 2, 17, 0, 0, 0, time.UTC)
	ace := feedServer(t, feedBytes(t, base, "A", "trip-a"), []byte("bad protobuf"))
	defer ace.Close()
	nqrw := feedServer(t, feedBytes(t, base.Add(10*time.Second), "N", "trip-n"), feedBytes(t, base.Add(20*time.Second), "Q", "trip-q"))
	defer nqrw.Close()

	metrics := telemetry.New("ace", "nqrw")
	feeds := []feed{
		{name: "ace", routes: []string{"A", "C", "E"}, store: realtime.NewStore()},
		{name: "nqrw", routes: []string{"N", "Q", "R", "W"}, store: realtime.NewStore()},
	}
	feeds[0].loader = ingest.NewLoader(ace.Client(), feeds[0].store, "fixture", ace.URL, "", 30*time.Second, testReceiptTime)
	feeds[1].loader = ingest.NewLoader(nqrw.Client(), feeds[1].store, "fixture", nqrw.URL, "", 30*time.Second, testReceiptTime)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	refresh(context.Background(), logger, metrics, feeds)
	refresh(context.Background(), logger, metrics, feeds)
	aceSnapshot := feeds[0].store.Load(base.Add(30*time.Second), time.Minute)
	nqrwSnapshot := feeds[1].store.Load(base.Add(30*time.Second), time.Minute)
	if len(aceSnapshot.Trains) != 1 || aceSnapshot.Trains[0].RouteID != "A" || !aceSnapshot.SourceTimestamp.Equal(base) {
		t.Fatalf("ACE snapshot = %#v, want retained A", aceSnapshot)
	}
	if len(nqrwSnapshot.Trains) != 1 || nqrwSnapshot.Trains[0].RouteID != "Q" || !nqrwSnapshot.SourceTimestamp.Equal(base.Add(20*time.Second)) {
		t.Fatalf("N/Q/R/W snapshot = %#v, want refreshed Q", nqrwSnapshot)
	}
	if !strings.Contains(logs.String(), "feed=ace") {
		t.Fatalf("refresh failure log = %q, want feed name", logs.String())
	}
	for _, feed := range metrics.Snapshot() {
		if feed.LastSuccess.IsZero() || feed.RefreshSuccesses == 0 {
			t.Fatalf("metrics for %s = %#v, want successful refresh", feed.Name, feed)
		}
		if feed.Name == "ace" && feed.RefreshErrors != 1 {
			t.Fatalf("ACE metrics = %#v, want one failed refresh", feed)
		}
	}
}

func TestRefreshPublishesFastFeedBeforeSlowFeedCompletes(t *testing.T) {
	base := time.Date(2026, time.September, 2, 17, 0, 0, 0, time.UTC)
	releaseSlow := make(chan struct{})
	slowFeed := feedBytes(t, base, "N", "trip-n")
	slow := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		<-releaseSlow
		_, _ = response.Write(slowFeed)
	}))
	defer slow.Close()
	defer close(releaseSlow)
	fast := feedServer(t, feedBytes(t, base, "A", "trip-a"))
	defer fast.Close()

	metrics := telemetry.New("ace", "nqrw")
	feeds := []feed{
		{name: "ace", routes: []string{"A", "C", "E"}, store: realtime.NewStore()},
		{name: "nqrw", routes: []string{"N", "Q", "R", "W"}, store: realtime.NewStore()},
	}
	feeds[0].loader = ingest.NewLoader(fast.Client(), feeds[0].store, "fixture", fast.URL, "", 30*time.Second, testReceiptTime)
	feeds[1].loader = ingest.NewLoader(slow.Client(), feeds[1].store, "fixture", slow.URL, "", 30*time.Second, testReceiptTime)
	done := make(chan struct{})
	go func() {
		refresh(context.Background(), slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), metrics, feeds)
		close(done)
	}()

	deadline := time.After(time.Second)
	for {
		if _, freshness, _ := feeds[0].store.Status(base, time.Minute); freshness == realtime.FreshnessFresh {
			break
		}
		select {
		case <-deadline:
			t.Fatal("fast feed was not published while slow feed was blocked")
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case <-done:
		t.Fatal("refresh completed while slow feed was blocked")
	default:
	}
	handler := httpapi.NewHandler(feedSources(feeds), metrics, func() time.Time { return base }, time.Minute)
	if recorder := apiRequest(handler, "/api/v1/trains?route_id=A"); recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"coverage":"complete"`) {
		t.Fatalf("fast-feed API status = %d/%s", recorder.Code, recorder.Body.String())
	}
	releaseSlow <- struct{}{}
	<-done
}

func TestRefreshAcceptsRecoveredFeedOlderThanOtherFeed(t *testing.T) {
	base := time.Date(2026, time.September, 2, 17, 0, 0, 0, time.UTC)
	ace := feedServer(t, []byte("bad protobuf"), feedBytes(t, base, "A", "trip-a"))
	defer ace.Close()
	nqrw := feedServer(t, feedBytes(t, base.Add(time.Minute), "N", "trip-n"), feedBytes(t, base.Add(2*time.Minute), "Q", "trip-q"))
	defer nqrw.Close()

	metrics := telemetry.New("ace", "nqrw")
	feeds := []feed{
		{name: "ace", store: realtime.NewStore()},
		{name: "nqrw", store: realtime.NewStore()},
	}
	feeds[0].loader = ingest.NewLoader(ace.Client(), feeds[0].store, "fixture", ace.URL, "", 30*time.Second, testReceiptTime)
	feeds[1].loader = ingest.NewLoader(nqrw.Client(), feeds[1].store, "fixture", nqrw.URL, "", 30*time.Second, testReceiptTime)
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))

	refresh(context.Background(), logger, metrics, feeds)
	refresh(context.Background(), logger, metrics, feeds)
	aceSnapshot := feeds[0].store.Load(base.Add(2*time.Minute), 0)
	nqrwSnapshot := feeds[1].store.Load(base.Add(2*time.Minute), 0)
	if len(aceSnapshot.Trains) != 1 || !aceSnapshot.SourceTimestamp.Equal(base) {
		t.Fatalf("recovered ACE snapshot = %#v, want older cross-feed timestamp accepted", aceSnapshot)
	}
	if len(nqrwSnapshot.Trains) != 1 || !nqrwSnapshot.SourceTimestamp.Equal(base.Add(2*time.Minute)) {
		t.Fatalf("N/Q/R/W snapshot = %#v, want independently refreshed feed", nqrwSnapshot)
	}
}

func feedServer(t *testing.T, responses ...[]byte) *httptest.Server {
	t.Helper()
	index := 0
	return httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write(responses[index])
		index++
	}))
}

func feedBytes(t *testing.T, timestamp time.Time, routeID, tripID string) []byte {
	t.Helper()
	wire, err := proto.Marshal(&gtfs.FeedMessage{
		Header: &gtfs.FeedHeader{GtfsRealtimeVersion: proto.String("1.0"), Timestamp: proto.Uint64(uint64(timestamp.Unix()))},
		Entity: []*gtfs.FeedEntity{{Id: proto.String(tripID), TripUpdate: &gtfs.TripUpdate{
			Trip:           &gtfs.TripDescriptor{TripId: proto.String(tripID), RouteId: proto.String(routeID)},
			StopTimeUpdate: []*gtfs.TripUpdate_StopTimeUpdate{{StopId: proto.String("stop")}},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func feedSources(feeds []feed) []httpapi.Source {
	sources := make([]httpapi.Source, len(feeds))
	for index, feed := range feeds {
		sources[index] = httpapi.Source{Name: feed.name, Routes: feed.routes, Store: feed.store}
	}
	return sources
}

func apiRequest(handler http.Handler, target string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	return recorder
}

func TestRefreshTransitionLogs(t *testing.T) {
	base := time.Date(2026, time.September, 2, 17, 0, 0, 0, time.UTC)
	initial := feedBytes(t, base, "A", "private-trip")
	recoveredAt := base.Add(time.Minute)
	recovered := feedBytes(t, recoveredAt, "A", "recovered-trip")
	responseIndex := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		if responseIndex == 1 || responseIndex == 2 {
			response.Header().Set("Location", "http://outage-user:outage-password@%zz/outage-path?outage-token=secret")
			response.WriteHeader(http.StatusFound)
		} else if responseIndex == 0 {
			_, _ = response.Write(initial)
		} else {
			_, _ = response.Write(recovered)
		}
		responseIndex++
	}))
	defer server.Close()
	feeds := []feed{{name: "ace", store: realtime.NewStore()}}
	feeds[0].loader = ingest.NewLoader(server.Client(), feeds[0].store, "fixture", server.URL, "", 30*time.Second, testReceiptTime)
	metrics := telemetry.New("ace")
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil)).With("service", "hol-up-api")
	refresh(context.Background(), logger, metrics, feeds)
	for range 2 {
		refresh(context.Background(), logger, metrics, feeds)
		retained := feeds[0].store.Load(base, time.Minute)
		if len(retained.Trains) != 1 || retained.Trains[0].TripID != "private-trip" || !retained.SourceTimestamp.Equal(base) {
			t.Fatalf("snapshot during outage = %#v, want original snapshot", retained)
		}
	}
	refresh(context.Background(), logger, metrics, feeds)
	refresh(context.Background(), logger, metrics, feeds)
	recoveredSnapshot := feeds[0].store.Load(recoveredAt, time.Minute)
	if len(recoveredSnapshot.Trains) != 1 || recoveredSnapshot.Trains[0].TripID != "recovered-trip" || !recoveredSnapshot.SourceTimestamp.Equal(recoveredAt) {
		t.Fatalf("snapshot after recovery = %#v, want recovered snapshot", recoveredSnapshot)
	}
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want two transitions: %s", &logs)
	}
	for i, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		wantEvent, wantLevel := "realtime_feed_refresh_failed", "WARN"
		if i == 1 {
			wantEvent, wantLevel = "realtime_feed_refresh_recovered", "INFO"
		}
		wantSourceTimestamp := base
		if i == 1 {
			wantSourceTimestamp = recoveredAt
		}
		for key, want := range map[string]any{"event": wantEvent, "level": wantLevel, "feed": "ace", "service": "hol-up-api", "train_count": float64(1), "source_timestamp": wantSourceTimestamp.Format(time.RFC3339)} {
			if record[key] != want {
				t.Errorf("%s = %v, want %v", key, record[key], want)
			}
		}
		if duration, ok := record["duration_ms"].(float64); !ok || duration < 0 {
			t.Errorf("invalid duration: %v", record)
		}
		if i == 0 && record["snapshot_available"] != true {
			t.Errorf("missing snapshot: %v", record)
		}
		if i == 1 {
			if record["failed_refreshes"] != float64(2) {
				t.Errorf("failure count: %v", record)
			}
			if duration, ok := record["outage_duration_ms"].(float64); !ok || duration < record["duration_ms"].(float64) {
				t.Errorf("invalid outage duration: %v", record)
			}
		}
	}
	if feeds[0].failedRefreshes != 0 || !feeds[0].failureStarted.IsZero() {
		t.Fatal("outage not cleared")
	}
	if got := metrics.Snapshot()[0]; got.RefreshErrors != 2 || got.RefreshSuccesses != 3 {
		t.Fatalf("metrics: %+v", got)
	}
	if strings.Contains(logs.String(), "private-trip") {
		t.Fatal("trip leaked")
	}
	for _, marker := range []string{"outage-user", "outage-password", "outage-path", "outage-token", "secret"} {
		if strings.Contains(logs.String(), marker) {
			t.Errorf("outage log contains %q: %s", marker, &logs)
		}
	}
}

func TestRefreshInitialFailureAndCancellation(t *testing.T) {
	server := feedServer(t, []byte("bad"))
	defer server.Close()
	feeds := []feed{{name: "ace", store: realtime.NewStore()}}
	feeds[0].loader = ingest.NewLoader(server.Client(), feeds[0].store, "fixture", server.URL, "", 30*time.Second, testReceiptTime)
	metrics := telemetry.New("ace")
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	refresh(context.Background(), logger, metrics, feeds)
	var record map[string]any
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["snapshot_available"] != false || record["train_count"] != float64(0) {
		t.Fatalf("false snapshot claim: %v", record)
	}
	if _, exists := record["source_timestamp"]; exists {
		t.Fatal("timestamp without snapshot")
	}
	beforeLogs, beforeMetrics := logs.String(), metrics.Snapshot()
	beforeCount, beforeStarted := feeds[0].failedRefreshes, feeds[0].failureStarted
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requested := make(chan struct{})
	blocked := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(requested)
		<-request.Context().Done()
	}))
	defer blocked.Close()
	feeds[0].loader = ingest.NewLoader(blocked.Client(), feeds[0].store, "fixture", blocked.URL, "", 30*time.Second, testReceiptTime)
	done := make(chan struct{})
	go func() {
		refresh(ctx, logger, metrics, feeds)
		close(done)
	}()
	<-requested
	cancel()
	<-done
	if logs.String() != beforeLogs || !reflect.DeepEqual(metrics.Snapshot(), beforeMetrics) || feeds[0].failedRefreshes != beforeCount || feeds[0].failureStarted != beforeStarted {
		t.Fatal("cancellation changed observability state")
	}
}

func TestRefreshSanitizesURLError(t *testing.T) {
	feeds := []feed{{name: "ace", store: realtime.NewStore()}}
	client := &http.Client{Transport: failingTransport{}}
	feeds[0].loader = ingest.NewLoader(client, feeds[0].store, "fixture", "https://user:password@example.test/feed?token=secret", "", 30*time.Second, testReceiptTime)
	var logs bytes.Buffer
	refresh(context.Background(), slog.New(slog.NewJSONHandler(&logs, nil)), telemetry.New("ace"), feeds)
	for _, secret := range []string{"user", "password", "example.test", "token", "secret", "/feed"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("log contains %q: %s", secret, &logs)
		}
	}
	var record map[string]any
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["error"] != "request failed" {
		t.Fatalf("error = %q, want request failed", record["error"])
	}
}

func TestRefreshSanitizesMalformedRedirect(t *testing.T) {
	const location = "http://leak-user:leak-password@%zz/leak-path?leak-token=secret"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Location", location)
		response.WriteHeader(http.StatusFound)
	}))
	defer server.Close()

	feeds := []feed{{name: "ace", store: realtime.NewStore()}}
	feeds[0].loader = ingest.NewLoader(server.Client(), feeds[0].store, "fixture", server.URL, "", 30*time.Second, testReceiptTime)
	var logs bytes.Buffer
	refresh(context.Background(), slog.New(slog.NewJSONHandler(&logs, nil)), telemetry.New("ace"), feeds)

	var record map[string]any
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["error"] != "request failed" {
		t.Errorf("error = %q, want request failed", record["error"])
	}
	if record["snapshot_available"] != false || record["train_count"] != float64(0) {
		t.Errorf("initial failure claimed a snapshot: %v", record)
	}
	if _, exists := record["source_timestamp"]; exists {
		t.Errorf("initial failure logged source_timestamp: %v", record)
	}
	for _, marker := range []string{"leak-user", "leak-password", "leak-path", "leak-token", "secret"} {
		if strings.Contains(logs.String(), marker) {
			t.Errorf("serialized log contains %q: %s", marker, &logs)
		}
		for field, value := range record {
			if text, ok := value.(string); ok && strings.Contains(text, marker) {
				t.Errorf("decoded field %q contains %q: %q", field, marker, text)
			}
		}
	}
}

func TestRefreshLogsHTTPStatusWithoutResponseDetails(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			marker := fmt.Sprintf("response-secret-%d", status)
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("X-Diagnostic", marker)
				response.WriteHeader(status)
				_, _ = response.Write([]byte(marker))
			}))
			defer server.Close()

			feeds := []feed{{name: "ace", store: realtime.NewStore()}}
			feeds[0].loader = ingest.NewLoader(server.Client(), feeds[0].store, "fixture", server.URL, "", 30*time.Second, testReceiptTime)
			var logs bytes.Buffer
			refresh(context.Background(), slog.New(slog.NewJSONHandler(&logs, nil)), telemetry.New("ace"), feeds)

			var record map[string]any
			if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
				t.Fatal(err)
			}
			if want := fmt.Sprintf("upstream HTTP status %d", status); record["error"] != want {
				t.Errorf("error = %q, want %q", record["error"], want)
			}
			if strings.Contains(logs.String(), marker) {
				t.Errorf("response details leaked: %s", &logs)
			}
		})
	}
}

func TestLogErrorSafeSummaries(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"canceled", fmt.Errorf("outer-secret: %w", context.Canceled), "operation canceled"},
		{"deadline", fmt.Errorf("outer-secret: %w", context.DeadlineExceeded), "operation timed out"},
		{"network timeout", &url.Error{Op: "timeout-op-secret", URL: "https://timeout-url-secret.test", Err: timeoutTestError("timeout-detail-secret")}, "operation timed out"},
		{"HTTP status", fmt.Errorf("outer-secret: %w", ingest.HTTPStatusError(http.StatusTooManyRequests)), "upstream HTTP status 429"},
		{"feed too large", fmt.Errorf("outer-secret: %w", ingest.ErrFeedTooLarge), ingest.ErrFeedTooLarge.Error()},
		{"differential feed", fmt.Errorf("outer-secret: %w", ingest.ErrDifferentialFeed), ingest.ErrDifferentialFeed.Error()},
		{"missing timestamp", fmt.Errorf("outer-secret: %w", realtime.ErrMissingSourceTimestamp), realtime.ErrMissingSourceTimestamp.Error()},
		{"out of order", fmt.Errorf("outer-secret: %w", realtime.ErrOutOfOrderSnapshot), realtime.ErrOutOfOrderSnapshot.Error()},
		{"future timestamp", fmt.Errorf("outer-secret: %w", &ingest.FutureTimestampError{}), "GTFS-Realtime source timestamp exceeds allowed future skew"},
		{"invalid future skew configuration", fmt.Errorf("outer-secret: %w", errInvalidMaxFutureSkew), errInvalidMaxFutureSkew.Error()},
		{"certificate", fmt.Errorf("outer-secret: %w", &tls.CertificateVerificationError{Err: errors.New("certificate-secret")}), "TLS failure"},
		{"TLS record", fmt.Errorf("outer-secret: %w", tls.RecordHeaderError{Msg: "record-secret"}), "TLS failure"},
		{"DNS", fmt.Errorf("outer-secret: %w", &net.DNSError{Err: "dns-detail-secret", Name: "dns-name-secret", Server: "dns-server-secret"}), "DNS resolution failed"},
		{"network operation", fmt.Errorf("outer-secret: %w", &net.OpError{Op: "op-secret", Net: "net-secret", Source: sensitiveAddr("source-secret"), Addr: sensitiveAddr("address-secret"), Err: errors.New("network-detail-secret")}), "network operation failed"},
		{"nested URL", fmt.Errorf("outer-secret: %w", &url.Error{Op: "outer-op-secret", URL: "https://outer-url-secret.test", Err: &url.Error{Op: "inner-op-secret", URL: "https://inner-url-secret.test", Err: errors.New("url-detail-secret")}}), "request failed"},
		{"plain", errors.New("plain-secret"), "operation failed"},
		{"wrapped", fmt.Errorf("outer-secret: %w", errors.New("wrapped-secret")), "operation failed"},
		{"joined cancellation precedence", errors.Join(errors.New("join-secret"), ingest.HTTPStatusError(http.StatusUnauthorized), context.Canceled), "operation canceled"},
		{"joined status precedence", errors.Join(errors.New("join-secret"), &net.DNSError{Name: "dns-secret"}, ingest.HTTPStatusError(http.StatusServiceUnavailable)), "upstream HTTP status 503"},
		{"joined sentinel precedence", errors.Join(&url.Error{Op: "url-secret", URL: "https://url-secret.test", Err: errors.New("detail-secret")}, ingest.ErrDifferentialFeed), ingest.ErrDifferentialFeed.Error()},
		{"joined TLS precedence", errors.Join(&url.Error{Op: "url-secret", URL: "https://url-secret.test", Err: errors.New("detail-secret")}, tls.RecordHeaderError{Msg: "record-secret"}), "TLS failure"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := logError(test.err); got != test.want {
				t.Errorf("logError() = %q, want %q", got, test.want)
			}
		})
	}
}

type timeoutTestError string

func (e timeoutTestError) Error() string { return string(e) }
func (timeoutTestError) Timeout() bool   { return true }
func (timeoutTestError) Temporary() bool { return true }

type sensitiveAddr string

func (sensitiveAddr) Network() string  { return "network-secret" }
func (a sensitiveAddr) String() string { return string(a) }

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network unavailable")
}

func testReceiptTime() time.Time {
	return time.Date(2026, time.September, 2, 17, 2, 0, 0, time.UTC)
}

func TestConfiguredMaxFutureSkew(t *testing.T) {
	for _, test := range []struct {
		name    string
		value   string
		want    time.Duration
		invalid bool
	}{
		{"unset", "", 30 * time.Second, false},
		{"empty", "", 30 * time.Second, false},
		{"custom", "1m250ms", time.Minute + 250*time.Millisecond, false},
		{"zero", "0", 0, false},
		{"malformed", "secret-invalid-value", 0, true},
		{"negative", "-1ns", 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("MTA_GTFS_RT_MAX_FUTURE_SKEW", test.value)
			if test.name == "unset" {
				if err := os.Unsetenv("MTA_GTFS_RT_MAX_FUTURE_SKEW"); err != nil {
					t.Fatal(err)
				}
			}
			got, err := configuredMaxFutureSkew()
			if test.invalid {
				if !errors.Is(err, errInvalidMaxFutureSkew) || logError(err) != errInvalidMaxFutureSkew.Error() {
					t.Fatalf("configuration error = %v", err)
				}
				var logs bytes.Buffer
				if err := run(slog.New(slog.NewJSONHandler(&logs, nil))); !errors.Is(err, errInvalidMaxFutureSkew) {
					t.Fatalf("run() = %v, want configuration error before startup", err)
				}
				if logs.Len() != 0 {
					t.Fatalf("invalid configuration started API: %s", &logs)
				}
			} else if err != nil || got != test.want {
				t.Fatalf("configuredMaxFutureSkew() = %v, %v; want %v", got, err, test.want)
			}
		})
	}
}

func TestConfiguredFeedsShareFutureTolerance(t *testing.T) {
	base := testReceiptTime()
	future := feedBytes(t, base.Add(6*time.Second), "A", "rejected")
	boundary := feedBytes(t, base.Add(5*time.Second), "A", "accepted")
	responses := make([][]byte, 0, 16)
	for range 8 {
		responses = append(responses, future, boundary)
	}
	server := feedServer(t, responses...)
	defer server.Close()
	for _, name := range []string{"MTA_GTFS_RT_URL", "MTA_GTFS_RT_BDFM_URL", "MTA_GTFS_RT_G_URL", "MTA_GTFS_RT_JZ_URL", "MTA_GTFS_RT_L_URL", "MTA_GTFS_RT_NQRW_URL", "MTA_GTFS_RT_NUMBERED_URL", "MTA_GTFS_RT_SI_URL"} {
		t.Setenv(name, server.URL)
	}
	feeds := configuredFeeds(server.Client(), 5*time.Second, func() time.Time { return base })
	if len(feeds) != 8 {
		t.Fatalf("feeds = %d, want 8", len(feeds))
	}
	for _, source := range feeds {
		var futureError *ingest.FutureTimestampError
		if err := source.loader.Load(t.Context()); !errors.As(err, &futureError) || futureError.MaxFutureSkew != 5*time.Second || !futureError.ReceivedAt.Equal(base) {
			t.Fatalf("%s did not use configured tolerance and clock: %v", source.name, err)
		}
		if err := source.loader.Load(t.Context()); err != nil {
			t.Fatalf("%s rejected boundary: %v", source.name, err)
		}
	}
}

func TestRefreshFutureTimestampRecoveryAndWarnings(t *testing.T) {
	for _, priorFailure := range []bool{false, true} {
		t.Run(fmt.Sprintf("prior_failure_%t", priorFailure), func(t *testing.T) {
			base := testReceiptTime()
			now := base
			futureTime := base.Add(time.Hour)
			future := feedBytes(t, futureTime, "A", "private-rejected-trip")
			recoveredAt := base.Add(70 * time.Second)
			responses := [][]byte{feedBytes(t, base, "A", "original")}
			if priorFailure {
				responses = append(responses, []byte("bad protobuf"))
			}
			responses = append(responses, future, future, feedBytes(t, recoveredAt, "A", "recovered"), future)
			server := feedServer(t, responses...)
			defer server.Close()
			feeds := []feed{{name: "ace", routes: []string{"A"}, store: realtime.NewStore()}}
			feeds[0].loader = ingest.NewLoader(server.Client(), feeds[0].store, "fixture", server.URL, "", 30*time.Second, func() time.Time { return now })
			metrics := telemetry.New("ace")
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			refresh(t.Context(), logger, metrics, feeds)
			original := feeds[0].store.Load(base, staleAfter)
			before := metrics.Snapshot()[0]
			failures := uint64(0)
			if priorFailure {
				refresh(t.Context(), logger, metrics, feeds)
				failures++
			}
			started := feeds[0].failureStarted
			for i := range 2 {
				if i == 1 {
					now = recoveredAt
				}
				refresh(t.Context(), logger, metrics, feeds)
				failures++
				if got := feeds[0].store.Load(base, staleAfter); !reflect.DeepEqual(got, original) {
					t.Fatalf("rejection changed retained snapshot: %+v", got)
				}
				got := metrics.Snapshot()[0]
				if got.RefreshErrors != failures || got.RefreshSuccesses != 1 || got.LastSuccess != before.LastSuccess || got.SourceTimestamp != before.SourceTimestamp || got.TrainCount != before.TrainCount {
					t.Fatalf("rejection metrics = %+v, previously %+v", got, before)
				}
				if priorFailure && feeds[0].failureStarted != started {
					t.Fatal("future rejection restarted existing outage")
				}
			}
			handler := httpapi.NewHandler(feedSources(feeds), metrics, func() time.Time { return now }, staleAfter)
			if got := feeds[0].store.Load(now, staleAfter); got.Freshness != realtime.FreshnessStale {
				t.Fatalf("retained freshness = %s", got.Freshness)
			}
			if got := apiRequest(handler, "/readyz"); got.Code != http.StatusServiceUnavailable {
				t.Fatalf("stale readiness = %d", got.Code)
			}
			if got := apiRequest(handler, "/api/v1/trains?route_id=A"); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"freshness":"stale"`) {
				t.Fatalf("stale trains = %d/%s", got.Code, got.Body.String())
			}
			refresh(t.Context(), logger, metrics, feeds)
			got := feeds[0].store.Load(now, staleAfter)
			if !got.SourceTimestamp.Equal(recoveredAt) || len(got.Trains) != 1 || got.Trains[0].TripID != "recovered" {
				t.Fatalf("recovery = %+v", got)
			}
			if feeds[0].failedRefreshes != 0 || feeds[0].futureTimestampWarned || !feeds[0].failureStarted.IsZero() {
				t.Fatal("recovery did not reset outage")
			}
			if got := apiRequest(handler, "/readyz"); got.Code != http.StatusOK {
				t.Fatalf("recovered readiness = %d", got.Code)
			}
			refresh(t.Context(), logger, metrics, feeds)
			lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
			wantLines := 3
			if priorFailure {
				wantLines++
			}
			if len(lines) != wantLines {
				t.Fatalf("warnings not suppressed/reset: %s", &logs)
			}
			futureWarnings := 0
			for i, line := range lines {
				var record map[string]any
				if err := json.Unmarshal([]byte(line), &record); err != nil {
					t.Fatal(err)
				}
				if record["event"] == "realtime_feed_refresh_recovered" {
					if record["level"] != "INFO" || record["failed_refreshes"] != float64(failures) {
						t.Errorf("recovery log = %v", record)
					}
					continue
				}
				if priorFailure && i == 0 {
					continue
				}
				futureWarnings++
				retainedAt, receivedAt := base, base
				if futureWarnings == 2 {
					retainedAt, receivedAt = recoveredAt, recoveredAt
				}
				for key, want := range map[string]any{
					"event": "realtime_feed_refresh_failed", "level": "WARN", "feed": "ace",
					"error": "GTFS-Realtime source timestamp exceeds allowed future skew", "reason": "future_source_timestamp",
					"rejected_source_timestamp": float64(futureTime.Unix()), "received_at": receivedAt.Format(time.RFC3339),
					"max_future_skew_seconds": float64(30), "source_timestamp": retainedAt.Format(time.RFC3339),
					"snapshot_available": true, "train_count": float64(1),
				} {
					if record[key] != want {
						t.Errorf("%s = %v, want %v", key, record[key], want)
					}
				}
			}
			if futureWarnings != 2 || strings.Contains(logs.String(), "private-rejected-trip") {
				t.Fatalf("unsafe or missing warning: %s", &logs)
			}
			if got := metrics.Snapshot()[0]; got.RefreshErrors != failures+1 || got.RefreshSuccesses != 2 || !got.SourceTimestamp.Equal(recoveredAt) {
				t.Fatalf("final metrics = %+v", got)
			}
		})
	}
}

func TestRefreshFutureOnlyStartupAndHealthySibling(t *testing.T) {
	for _, healthySibling := range []bool{false, true} {
		t.Run(fmt.Sprintf("healthy_sibling_%t", healthySibling), func(t *testing.T) {
			base := testReceiptTime()
			future := feedBytes(t, base.Add(time.Hour), "A", "rejected")
			server := feedServer(t, future, future)
			defer server.Close()
			feeds := []feed{{name: "ace", routes: []string{"A"}, store: realtime.NewStore()}}
			feeds[0].loader = ingest.NewLoader(server.Client(), feeds[0].store, "fixture", server.URL, "", 30*time.Second, func() time.Time { return base })
			if healthySibling {
				valid := feedBytes(t, base, "N", "healthy")
				sibling := feedServer(t, valid, valid)
				defer sibling.Close()
				store := realtime.NewStore()
				feeds = append(feeds, feed{name: "nqrw", routes: []string{"N"}, store: store, loader: ingest.NewLoader(sibling.Client(), store, "fixture", sibling.URL, "", 30*time.Second, func() time.Time { return base })})
			}
			metrics := telemetry.New("ace")
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			refresh(t.Context(), logger, metrics, feeds)
			refresh(t.Context(), logger, metrics, feeds)
			var record map[string]any
			if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
				t.Fatalf("want one warning: %v: %s", err, &logs)
			}
			if record["snapshot_available"] != false || record["reason"] != "future_source_timestamp" {
				t.Fatalf("initial warning = %v", record)
			}
			if _, exists := record["source_timestamp"]; exists {
				t.Fatal("initial rejection logged retained timestamp")
			}
			if _, freshness, count := feeds[0].store.Status(base, staleAfter); freshness != realtime.FreshnessUnavailable || count != 0 {
				t.Fatal("initial future snapshot published")
			}
			if got := metrics.Snapshot()[0]; got.RefreshErrors != 2 || got.RefreshSuccesses != 0 || !got.LastSuccess.IsZero() || !got.SourceTimestamp.IsZero() || got.TrainCount != 0 {
				t.Fatalf("startup metrics = %+v", got)
			}
			now := base
			handler := httpapi.NewHandler(feedSources(feeds), metrics, func() time.Time { return now }, staleAfter)
			wantStatus := http.StatusServiceUnavailable
			if healthySibling {
				wantStatus = http.StatusOK
			}
			if got := apiRequest(handler, "/readyz"); got.Code != wantStatus {
				t.Fatalf("readiness = %d/%s", got.Code, got.Body.String())
			}
			if healthySibling {
				if got := apiRequest(handler, "/api/v1/trains?route_id=N"); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"trip_id":"healthy"`) {
					t.Fatalf("sibling = %d/%s", got.Code, got.Body.String())
				}
			}
			now = base.Add(staleAfter + time.Second)
			if got := apiRequest(handler, "/readyz"); got.Code != http.StatusServiceUnavailable {
				t.Fatalf("aged readiness = %d", got.Code)
			}
		})
	}
}

func TestRefreshSuppressesFutureRejectionOnCancellation(t *testing.T) {
	base := testReceiptTime()
	server := feedServer(t, feedBytes(t, base.Add(time.Hour), "A", "rejected"))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	feeds := []feed{{name: "ace", store: realtime.NewStore()}}
	feeds[0].loader = ingest.NewLoader(server.Client(), feeds[0].store, "fixture", server.URL, "", 30*time.Second, func() time.Time {
		cancel()
		return base
	})
	metrics := telemetry.New("ace")
	before := metrics.Snapshot()
	var logs bytes.Buffer
	refresh(ctx, slog.New(slog.NewJSONHandler(&logs, nil)), metrics, feeds)
	if logs.Len() != 0 || !reflect.DeepEqual(metrics.Snapshot(), before) || feeds[0].failedRefreshes != 0 || feeds[0].futureTimestampWarned || !feeds[0].failureStarted.IsZero() {
		t.Fatal("canceled rejection changed observability state")
	}
}

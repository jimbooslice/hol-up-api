package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
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
	feeds := configuredFeeds(http.DefaultClient)
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
	feeds[0].loader = ingest.NewLoader(ace.Client(), feeds[0].store, ace.URL, "")
	feeds[1].loader = ingest.NewLoader(nqrw.Client(), feeds[1].store, nqrw.URL, "")

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
	feeds[0].loader = ingest.NewLoader(ace.Client(), feeds[0].store, ace.URL, "")
	feeds[1].loader = ingest.NewLoader(nqrw.Client(), feeds[1].store, nqrw.URL, "")
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
	feeds[0].loader = ingest.NewLoader(fast.Client(), feeds[0].store, fast.URL, "")
	feeds[1].loader = ingest.NewLoader(slow.Client(), feeds[1].store, slow.URL, "")
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
	feeds[0].loader = ingest.NewLoader(ace.Client(), feeds[0].store, ace.URL, "")
	feeds[1].loader = ingest.NewLoader(nqrw.Client(), feeds[1].store, nqrw.URL, "")
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

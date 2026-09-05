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

func TestRefreshPublishesHealthyFeedWithOtherFeedsLastKnownGood(t *testing.T) {
	base := time.Date(2026, time.September, 2, 17, 0, 0, 0, time.UTC)
	ace := feedServer(t, feedBytes(t, base, "A", "trip-a"), []byte("bad protobuf"))
	defer ace.Close()
	nqrw := feedServer(t, feedBytes(t, base.Add(10*time.Second), "N", "trip-n"), feedBytes(t, base.Add(20*time.Second), "Q", "trip-q"))
	defer nqrw.Close()

	aggregate := realtime.NewStore()
	metrics := telemetry.New("ace", "nqrw")
	feeds := []feed{
		{name: "ace", store: realtime.NewStore()},
		{name: "nqrw", store: realtime.NewStore()},
	}
	feeds[0].loader = ingest.NewLoader(ace.Client(), feeds[0].store, ace.URL, "")
	feeds[1].loader = ingest.NewLoader(nqrw.Client(), feeds[1].store, nqrw.URL, "")
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	refresh(context.Background(), logger, aggregate, metrics, feeds)
	refresh(context.Background(), logger, aggregate, metrics, feeds)
	got := aggregate.Load(base.Add(30*time.Second), time.Minute)
	if !got.SourceTimestamp.Equal(base) || len(got.Trains) != 2 {
		t.Fatalf("aggregate = %#v, want retained A and refreshed Q", got)
	}
	if got.Trains[0].RouteID != "A" || got.Trains[1].RouteID != "Q" {
		t.Fatalf("aggregate routes = %q/%q, want A/Q", got.Trains[0].RouteID, got.Trains[1].RouteID)
	}
	if !got.Trains[0].SourceTimestamp.Equal(base) || !got.Trains[1].SourceTimestamp.Equal(base.Add(20*time.Second)) {
		t.Fatalf("source timestamps = %s/%s", got.Trains[0].SourceTimestamp, got.Trains[1].SourceTimestamp)
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

package integration

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"hol-up-prototype/internal/ingest"
	"hol-up-prototype/internal/realtime"
	"hol-up-prototype/internal/reconcile"
	"hol-up-prototype/internal/schedule"
	"hol-up-prototype/internal/scheduleingest"
)

type fixtureTransport map[string][]byte

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(f[r.URL.Path]))}, nil
}
func TestOfflineALineIngestionThroughReconciliation(t *testing.T) {
	archive, err := os.ReadFile("../test/fixtures/reconcile/a-line.zip")
	if err != nil {
		t.Fatal(err)
	}
	wire, err := os.ReadFile("../test/fixtures/reconcile/a-line.pb")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 13, 4, 9, 0, 0, time.UTC)
	now := func() time.Time { return at }
	schedules := &schedule.Store{}
	realtimeStore := realtime.NewStore()
	results := &reconcile.Store{}
	client := &http.Client{Transport: fixtureTransport{"/schedule": archive, "/realtime": wire}}
	compute := func() {
		results.Publish(reconcile.Reconcile(schedules.Load(), []reconcile.Source{{Name: "ace", Routes: []string{"A"}, Snapshot: realtimeStore.Load(at, time.Minute)}}, at, time.Minute, 24*time.Hour))
	}
	loader := scheduleingest.NewLoader(client, schedules, nil, scheduleingest.Config{SupplementedURL: "https://fixture/schedule"}, slog.New(slog.NewTextHandler(io.Discard, nil)), now)
	loader.OnPublication = func(string) { compute() }
	loader.Refresh(t.Context())
	if results.Load() == nil || !results.Load().Metadata().ScheduleEligible {
		t.Fatal("schedule ingestion/publication failed")
	}
	if err := ingest.NewLoader(client, realtimeStore, "ace", "https://fixture/realtime", "", 30*time.Second, now).Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	compute()
	bundle := results.Load()
	matches := bundle.Matches()
	instance := reconcile.Instance{TripID: "BSP26GEN-A055-Sunday-00_000950_A..S74R", ServiceDate: "20260913"}
	if len(matches) != 1 || matches[0].Outcome != reconcile.Matched || matches[0].Instance != instance || matches[0].Stops[0].StopID != "A02S" || matches[0].Train.Stops[0].Arrival.Time.Unix() != at.Unix()+30 {
		t.Fatalf("%+v", matches)
	}
	if matches[0].Train.Active() || bundle.Fallback(instance, at).Reason != "active_realtime_counterpart" {
		t.Fatal("compact fields required for active counterpart")
	}
	rules := bundle.ReplacementRules()
	if len(rules) != 1 || !rules[0].Valid || rules[0].Period.Start != nil || rules[0].Reference.Source.Name != "ace" || rules[0].Reference.Source.SourceTimestamp != at || rules[0].Reference.Source.ReceivedAt != at {
		t.Fatalf("%+v", rules)
	}
	// The same complete feed with no trip updates establishes absence.
	snapshot := realtimeStore.Load(at, time.Minute)
	snapshot.Trains = nil
	empty := reconcile.Reconcile(schedules.Load(), []reconcile.Source{{Name: "ace", Routes: []string{"A"}, Snapshot: snapshot}}, at, time.Minute, 24*time.Hour)
	if !empty.Fallback(instance, at).Suppressed || empty.Fallback(instance, at.Add(time.Minute+time.Nanosecond)).Suppressed {
		t.Fatal("replacement authority did not expire")
	}
}

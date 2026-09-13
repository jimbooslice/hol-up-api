package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"hol-up-prototype/internal/ingest"
	"hol-up-prototype/internal/realtime"
	"hol-up-prototype/internal/reconcile"
	"hol-up-prototype/internal/schedule"
	"hol-up-prototype/internal/scheduleingest"
	"hol-up-prototype/internal/telemetry"
)

func coordinatorFixture(t *testing.T) (*reconciliationCoordinator, *bytes.Buffer) {
	t.Helper()
	archive, err := os.ReadFile("../../test/fixtures/reconcile/a-line.zip")
	if err != nil {
		t.Fatal(err)
	}
	dataset, err := scheduleingest.Decode(archive)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 13, 4, 9, 0, 0, time.UTC)
	v, err := schedule.NewVersion(dataset, schedule.Metadata{Source: schedule.Regular, ParserSchema: "test", ArchiveSHA256: strings.Repeat("a", 64), ValidatedAt: at, PublishedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	c := &reconciliationCoordinator{schedules: &schedule.Store{}, feeds: []feed{{name: "ace", routes: []string{"A"}, store: realtime.NewStore()}}, now: func() time.Time { return at }, logger: slog.New(slog.NewJSONHandler(logs, nil)), maxScheduleAge: 24 * time.Hour}
	c.schedules.Publish(v)
	if err := c.feeds[0].store.Replace(realtime.Snapshot{SourceTimestamp: at, Complete: true, Trains: []realtime.Train{{TripID: "000950_A..S74R", RouteID: "A", ServiceDate: "20260913"}}}); err != nil {
		t.Fatal(err)
	}
	return c, logs
}
func await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("synchronization timed out")
	}
}
func TestCoordinatorCapturesInputsAfterLock(t *testing.T) {
	c, _ := coordinatorFixture(t)
	at := c.now()
	entered := make(chan struct{})
	done := make(chan struct{})
	c.now = func() time.Time { close(entered); return at.Add(time.Second) }
	c.mu.Lock()
	go func() { c.run("blocked"); close(done) }()
	// Publication while the run is waiting must be captured by that run.
	if err := c.feeds[0].store.Replace(realtime.Snapshot{SourceTimestamp: at.Add(time.Second), Complete: true}); err != nil {
		t.Fatal(err)
	}
	meta := c.schedules.Load().Metadata()
	meta.ValidatedAt = at.Add(time.Second)
	meta.PublishedAt = meta.ValidatedAt
	v, err := schedule.NewVersion(c.schedules.Load().Dataset(), meta)
	if err != nil {
		t.Fatal(err)
	}
	c.schedules.Publish(v)
	select {
	case <-entered:
		t.Fatal("captured clock before lock")
	default:
	}
	c.mu.Unlock()
	await(t, done)
	b := c.results.Load()
	if b.Schedule() != v || b.Sources()[0].SourceTimestamp != at.Add(time.Second) || len(b.Matches()) != 0 || b.Metadata().EvaluatedAt != at.Add(time.Second) {
		t.Fatal("captured inputs before lock")
	}
}
func TestSchedulePublicationDuringRunAndAtomicReaders(t *testing.T) {
	c, _ := coordinatorFixture(t)
	at := c.now()
	old := c.schedules.Load()
	firstEntered, firstRelease := make(chan struct{}), make(chan struct{})
	secondEntered, secondRelease := make(chan struct{}), make(chan struct{})
	calls := 0
	c.now = func() time.Time {
		calls++
		if calls == 1 {
			close(firstEntered)
			<-firstRelease
		} else {
			close(secondEntered)
			<-secondRelease
		}
		return at
	}
	firstDone, secondDone := make(chan struct{}), make(chan struct{})
	go func() { c.run("first"); close(firstDone) }()
	await(t, firstEntered)
	meta := old.Metadata()
	meta.ArchiveSHA256 = strings.Repeat("b", 64)
	next, err := schedule.NewVersion(old.Dataset(), meta)
	if err != nil {
		t.Fatal(err)
	}
	c.schedules.Publish(next)
	go func() { c.run("schedule:new"); close(secondDone) }()
	close(firstRelease)
	await(t, firstDone)
	await(t, secondEntered)
	first := c.results.Load()
	if first.Schedule() != old || first.Metadata().Schedule != old.Metadata() {
		t.Fatal("mixed captured schedule")
	}
	// Readers can keep the old immutable bundle while a new one is published.
	var readers sync.WaitGroup
	for range 8 {
		readers.Go(func() {
			for range 1000 {
				b := c.results.Load()
				if b.Metadata().Schedule != b.Schedule().Metadata() {
					t.Error("torn publication")
				}
				_ = b.Matches()
				_ = b.ReplacementRules()
			}
		})
	}
	close(secondRelease)
	await(t, secondDone)
	readers.Wait()
	if c.results.Load().Schedule() != next || first.Schedule() != old {
		t.Fatal("new run lost or old bundle mutated")
	}
}
func TestConcurrentReconciliationRuns(t *testing.T) {
	c, logs := coordinatorFixture(t)
	var runs sync.WaitGroup
	for range 8 {
		runs.Go(func() { c.run("concurrent") })
	}
	runs.Wait()
	if strings.Count(logs.String(), `"event":"reconciliation_completed"`) != 8 || c.results.Load().Matches()[0].Outcome != reconcile.Matched {
		t.Fatal("lost publication")
	}
}
func TestReconciliationLogLevels(t *testing.T) {
	for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		c, logs := coordinatorFixture(t)
		c.logger = slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: level}))
		c.run("test")
		output := logs.String()
		if strings.Contains(output, `"event":"reconciliation_trip"`) != (level <= slog.LevelDebug) || strings.Contains(output, `"event":"reconciliation_completed"`) != (level <= slog.LevelInfo) {
			t.Fatalf("level %s: %s", level, output)
		}
		if strings.Contains(output, "https://") {
			t.Fatal("upstream URL logged")
		}
		if level <= slog.LevelInfo {
			for _, field := range []string{"trigger", "schedule", "sources", "outcomes", "duration_ms"} {
				if !strings.Contains(output, `"`+field+`"`) {
					t.Fatalf("missing %s", field)
				}
			}
		}
	}
	for _, value := range []string{"", "DEBUG", "debug", "iNfO", "WARN", "error"} {
		t.Setenv("LOG_LEVEL", value)
		if _, err := configuredLogLevel(); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []string{"TRACE", " info", "secret=hidden"} {
		t.Setenv("LOG_LEVEL", value)
		if _, err := configuredLogLevel(); err != errInvalidLogLevel || strings.Contains(logError(err), value) {
			t.Fatalf("unsafe config: %v", err)
		}
	}
}
func TestInvalidLogLevelExitsBeforeServingOrPolling(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestAPIScheduleStartupProcess$")
	cmd.Env = append(os.Environ(), "HOLUP_SCHEDULE_TEST_PROCESS=1", "LOG_LEVEL=secret-config-value")
	output, err := cmd.CombinedOutput()
	if err == nil || strings.Contains(string(output), "secret-config-value") || strings.Contains(string(output), "api_starting") || !strings.Contains(string(output), errInvalidLogLevel.Error()) {
		t.Fatalf("startup=%v/%s", err, output)
	}
	t.Setenv("LOG_LEVEL", "invalid")
	if err := run(slog.New(slog.NewTextHandler(io.Discard, nil))); err != errInvalidLogLevel {
		t.Fatal(err)
	}
}
func TestSuccessfulRefreshTriggersReconciliation(t *testing.T) {
	c, _ := coordinatorFixture(t)
	wire, err := os.ReadFile("../../test/fixtures/reconcile/a-line.pb")
	if err != nil {
		t.Fatal(err)
	}
	valid := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if valid {
			_, _ = w.Write(wire)
		} else {
			w.WriteHeader(503)
		}
	}))
	defer server.Close()
	c.feeds[0].loader = ingest.NewLoader(server.Client(), c.feeds[0].store, "ace", server.URL, "", 30*time.Second, c.now)
	calls := 0
	c.feeds[0].onPublication = func(trigger string) { calls++; c.run(trigger) }
	metrics := telemetry.New("ace")
	refresh(context.Background(), c.logger, metrics, c.feeds)
	first := c.results.Load()
	if calls != 1 || first == nil || first.Matches()[0].Outcome != reconcile.Matched {
		t.Fatal("publication did not reconcile")
	}
	valid = false
	refresh(context.Background(), c.logger, metrics, c.feeds)
	if calls != 1 || c.results.Load() != first {
		t.Fatal("failure republished reconciliation")
	}
}

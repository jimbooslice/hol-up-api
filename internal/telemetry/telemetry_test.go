package telemetry

import (
	"testing"
	"time"
)

func TestRecordRetainsLastSuccessAfterFailure(t *testing.T) {
	store := New("ace")
	source := time.Date(2026, time.September, 4, 12, 0, 0, 0, time.UTC)
	completed := source.Add(time.Second)
	store.Record("ace", true, 200*time.Millisecond, completed, source, 12)
	store.Record("ace", false, 300*time.Millisecond, completed.Add(time.Minute), source, 12)

	feeds := store.Snapshot()
	if len(feeds) != 1 {
		t.Fatalf("feeds = %#v, want one", feeds)
	}
	got := feeds[0]
	if !got.LastSuccess.Equal(completed) || got.RefreshSuccesses != 1 || got.RefreshErrors != 1 || got.LastRefreshDuration != 300*time.Millisecond || got.TrainCount != 12 || !got.SourceTimestamp.Equal(source) {
		t.Fatalf("feed = %#v", got)
	}
}

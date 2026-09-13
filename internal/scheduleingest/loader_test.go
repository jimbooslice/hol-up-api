package scheduleingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hol-up-prototype/internal/schedule"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testLoader(t *testing.T, cache Cache) (*Loader, *schedule.Store, *bytes.Buffer, *time.Time, map[string]int, *bool, *bool) {
	t.Helper()
	now := time.Date(2026, 9, 12, 16, 0, 0, 0, time.UTC)
	wire := zipFixture(t, fixtureFiles())
	supplementedOK, regularOK := true, true
	calls := map[string]int{}
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls[r.URL.Path]++
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > FetchTimeout {
			t.Error("missing fetch deadline")
		}
		if r.Method != http.MethodGet {
			t.Error("wrong method")
		}
		if (r.URL.Path == "/supplemented" && !supplementedOK) || (r.URL.Path == "/regular" && !regularOK) {
			return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("secret upstream response"))}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(wire))}, nil
	})}
	store := &schedule.Store{}
	logs := &bytes.Buffer{}
	l := NewLoader(client, store, cache, Config{SupplementedURL: "https://fixture/supplemented?secret=hidden", RegularURL: "https://fixture/regular", MaxAge: DefaultMaxAge}, slog.New(slog.NewJSONHandler(logs, nil)), func() time.Time { return now })
	return l, store, logs, &now, calls, &supplementedOK, &regularOK
}

func TestSupplementedRetentionFallbackRecoveryAndFreshness(t *testing.T) {
	l, store, logs, now, calls, suppOK, regularOK := testLoader(t, nil)
	l.Refresh(t.Context())
	first := store.Load()
	if first == nil || first.Metadata().Source != schedule.Supplemented || calls["/regular"] != 0 {
		t.Fatal("supplemented was not preferred")
	}
	*now = now.Add(time.Hour)
	l.Refresh(t.Context())
	second := store.Load()
	if second == first || second.Metadata().Version != first.Metadata().Version || !second.Metadata().ValidatedAt.Equal(*now) || first.Metadata().ValidatedAt.Equal(*now) {
		t.Fatal("identical archive did not renew metadata immutably")
	}
	*suppOK = false
	for i := 0; i < 2; i++ {
		*now = now.Add(time.Hour)
		l.Refresh(t.Context())
	}
	if store.Load() != second || calls["/regular"] != 0 {
		t.Fatal("failed refresh replaced eligible supplemented data")
	}
	*now = second.Metadata().ValidatedAt.Add(DefaultMaxAge)
	l.Refresh(t.Context())
	if calls["/regular"] != 0 {
		t.Fatal("exact max-age boundary expired")
	}
	*now = now.Add(time.Nanosecond)
	l.Refresh(t.Context())
	regular := store.Load()
	if regular.Metadata().Source != schedule.Regular || calls["/regular"] != 1 || regular.Metadata().Version == first.Metadata().Version || regular.Metadata().PlanningCoverage != (schedule.Coverage{}) {
		t.Fatal("regular fallback failed")
	}
	*regularOK = false
	l.Refresh(t.Context())
	if store.Load() != regular {
		t.Fatal("failed regular refresh lost retained data")
	}
	*suppOK = true
	l.Refresh(t.Context())
	if store.Load().Metadata().Source != schedule.Supplemented || calls["/regular"] != 2 {
		t.Fatal("supplemented recovery failed")
	}
	if strings.Count(logs.String(), `"event":"schedule_refresh_failed","source":"supplemented"`) != 1 || !strings.Contains(logs.String(), `"event":"schedule_refresh_recovered","source":"supplemented"`) || !strings.Contains(logs.String(), `"reason":"supplemented_expired"`) {
		t.Fatalf("missing transition logs: %s", logs)
	}
	if strings.Contains(logs.String(), "secret") || strings.Contains(logs.String(), "hidden") || strings.Contains(logs.String(), "https://") {
		t.Fatal("unsafe logging")
	}
	decoder := json.NewDecoder(bytes.NewReader(logs.Bytes()))
	for decoder.More() {
		var entry map[string]any
		if err := decoder.Decode(&entry); err != nil {
			t.Fatal(err)
		}
		if entry["event"] == "schedule_published" {
			for _, name := range []string{"source", "version", "published_at", "validated_at", "coverage", "planning_coverage", "counts", "duration_ms", "selection_reason"} {
				if _, ok := entry[name]; !ok {
					t.Errorf("missing publication %s", name)
				}
			}
		}
	}
}

func TestFallbackWhenSupplementedUnavailableOrOutsideCoverage(t *testing.T) {
	l, store, logs, _, calls, suppOK, _ := testLoader(t, nil)
	*suppOK = false
	l.Refresh(t.Context())
	if store.Load() == nil || store.Load().Metadata().Source != schedule.Regular || calls["/regular"] != 1 || !strings.Contains(logs.String(), "no_supplemented_schedule") {
		t.Fatal("initial fallback failed")
	}
	files := fixtureFiles()
	files["feed_info.txt"] = strings.ReplaceAll(files["feed_info.txt"], "20261231", "20260911")
	old := zipFixture(t, files)
	valid := zipFixture(t, fixtureFiles())
	l.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		wire := valid
		if r.URL.Path == "/supplemented" {
			wire = old
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(wire))}, nil
	})}
	l.Refresh(t.Context())
	if store.Load().Metadata().Source != schedule.Regular {
		t.Fatal("published out-of-coverage schedule")
	}
}

func TestFileCacheRestartExpiryCorruptionAndAtomicSave(t *testing.T) {
	cache := FileCache{Dir: t.TempDir()}
	l, store, _, now, calls, suppOK, _ := testLoader(t, cache)
	l.Restore(t.Context())
	if store.Load() != nil {
		t.Fatal("missing cache published")
	}
	l.Refresh(t.Context())
	first := store.Load()
	entry, err := cache.Load()
	if err != nil {
		t.Fatal(err)
	}
	if entry.Metadata != first.Metadata() || entry.Schema != cacheSchema || archiveHash(entry.Archive) != first.Metadata().ArchiveSHA256 {
		t.Fatal("cache envelope mismatch")
	}
	*now = now.Add(time.Hour)
	restart := NewLoader(l.client, &schedule.Store{}, cache, l.config, l.logger, l.now)
	restart.Restore(t.Context())
	restored := restart.store.Load()
	if restored == nil || restored.Metadata().Version != first.Metadata().Version || !restored.Metadata().ValidatedAt.Equal(first.Metadata().ValidatedAt) || !restored.Metadata().PublishedAt.Equal(*now) || len(restored.Dataset().RouteTrips("A")) != 1 {
		t.Fatal("restart failed to rebuild consistent indexes or renewed freshness")
	}
	*suppOK = false
	restart.Refresh(t.Context())
	if restart.store.Load() != restored || calls["/regular"] != 0 {
		t.Fatal("retained cache lost preference")
	}
	*now = now.Add(DefaultMaxAge)
	expired := NewLoader(l.client, &schedule.Store{}, cache, l.config, l.logger, l.now)
	expired.Restore(t.Context())
	if expired.store.Load() == nil || expired.store.Load().Eligible(*now, DefaultMaxAge) {
		t.Fatal("cache read renewed age or discarded expired data")
	}
	expired.Refresh(t.Context())
	if expired.store.Load().Metadata().Source != schedule.Regular {
		t.Fatal("expired restart did not fallback")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*CacheEntry)
	}{
		{"archive hash", func(e *CacheEntry) { e.Archive[0] ^= 1 }},
		{"schema", func(e *CacheEntry) { e.Schema++ }},
		{"parser schema", func(e *CacheEntry) { e.Metadata.ParserSchema = "future" }},
		{"version", func(e *CacheEntry) { e.Metadata.Version = "forged" }},
		{"coverage", func(e *CacheEntry) { e.Metadata.Coverage.End = "20991231" }},
		{"horizon", func(e *CacheEntry) { e.Metadata.PlanningCoverage.End = "20991231" }},
		{"future timestamp", func(e *CacheEntry) { e.Metadata.ValidatedAt = now.Add(time.Hour) }},
		{"revalidate", func(e *CacheEntry) {
			e.Archive = []byte("not a zip")
			e.Metadata.ArchiveSHA256 = archiveHash(e.Archive)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := entry
			bad.Archive = bytes.Clone(entry.Archive)
			tc.mutate(&bad)
			if err := cache.Save(bad); err != nil {
				t.Fatal(err)
			}
			loader := NewLoader(l.client, &schedule.Store{}, cache, l.config, l.logger, l.now)
			loader.Restore(t.Context())
			if loader.store.Load() != nil {
				t.Fatal("accepted corrupt cache")
			}
		})
	}
	for _, wire := range []string{"{", `{} {}`, `{"unrecognized":true}`} {
		if err := os.WriteFile(filepath.Join(cache.Dir, "active.json"), []byte(wire), 0600); err != nil {
			t.Fatal(err)
		}
		loader := NewLoader(l.client, &schedule.Store{}, cache, l.config, l.logger, l.now)
		loader.Restore(t.Context())
		if loader.store.Load() != nil {
			t.Fatal("accepted corrupt JSON")
		}
	}
	if err := cache.Save(entry); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache.Dir, ".active-interrupted.tmp"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := cache.Load()
	if err != nil || got.Metadata != entry.Metadata {
		t.Fatalf("temporary write affected active cache: %v", err)
	}
	if err := cache.Save(entry); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(cache.Dir, ".active-*.tmp"))
	if err != nil || len(files) != 1 {
		t.Fatalf("save leaked temporary files: %v %v", files, err)
	}
	f, err := os.OpenFile(filepath.Join(cache.Dir, "active.json"), os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxCacheBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err := cache.Load(); !errors.Is(err, ErrLimit) {
		t.Fatalf("unbounded cache: %v", err)
	}
}

type failedCache struct{}

func (failedCache) Load() (CacheEntry, error) {
	return CacheEntry{}, errors.New("sensitive cache path")
}
func (failedCache) Save(CacheEntry) error { return errors.New("sensitive cache path") }

func TestCacheFailureDoesNotBlockPublication(t *testing.T) {
	l, store, logs, _, _, _, _ := testLoader(t, failedCache{})
	l.Restore(t.Context())
	l.Refresh(t.Context())
	if store.Load() == nil || !strings.Contains(logs.String(), "schedule_cache_write_failed") || !strings.Contains(logs.String(), "schedule_cache_rejected") || strings.Contains(logs.String(), "sensitive") {
		t.Fatalf("cache failure: %s", logs)
	}
}

type repeatedBytes struct{}

func (repeatedBytes) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestHTTPActualBodyLimitRetainsPublishedSchedule(t *testing.T) {
	l, store, logs, _, _, _, _ := testLoader(t, nil)
	l.Refresh(t.Context())
	first := store.Load()
	l.client = &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, ContentLength: -1, Body: io.NopCloser(io.LimitReader(repeatedBytes{}, MaxArchiveBytes+1))}, nil
	})}
	l.Refresh(t.Context())
	if store.Load() != first || !strings.Contains(logs.String(), ErrLimit.Error()) {
		t.Fatal("oversized streaming response replaced published data")
	}
}

func TestHTTPFailuresAndCanceledPoller(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response *http.Response
		err      error
		want     string
	}{
		{"status", &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader("secret"))}, nil, "upstream HTTP status 403"},
		{"declared limit", &http.Response{StatusCode: 200, ContentLength: MaxArchiveBytes + 1, Body: io.NopCloser(strings.NewReader(""))}, nil, ErrLimit.Error()},
		{"bad ZIP", &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("secret"))}, nil, ErrInvalid.Error()},
		{"network", nil, errors.New("https://user:secret@upstream/secret"), "schedule operation failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, store, logs, _, _, _, _ := testLoader(t, nil)
			l.client = &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) { return tc.response, tc.err })}
			l.Refresh(t.Context())
			if store.Load() != nil || !strings.Contains(logs.String(), tc.want) || strings.Contains(logs.String(), "secret") {
				t.Fatalf("failure handling: %s", logs)
			}
		})
	}
	l, store, logs, _, _, _, _ := testLoader(t, nil)
	started := make(chan struct{})
	l.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { l.Run(ctx); close(done) }()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not stop poller")
	}
	if store.Load() != nil || logs.Len() != 0 {
		t.Fatalf("cancellation published or warned: %s", logs)
	}
}

func TestEveryPublicationNotifiesAfterStoreIncludingRestoreAndRenewal(t *testing.T) {
	cache := FileCache{Dir: t.TempDir()}
	loader, store, _, now, _, suppOK, _ := testLoader(t, cache)
	var versions []*schedule.Version
	var reasons []string
	loader.OnPublication = func(reason string) {
		if store.Load() == nil {
			t.Fatal("callback before publication")
		}
		versions = append(versions, store.Load())
		reasons = append(reasons, reason)
	}
	loader.Refresh(t.Context())
	*now = now.Add(time.Hour)
	loader.Refresh(t.Context())
	if len(versions) != 2 || versions[0] == versions[1] || versions[0].Metadata().Version != versions[1].Metadata().Version || !versions[1].Metadata().ValidatedAt.Equal(*now) {
		t.Fatal("unchanged-content renewal callback missing")
	}
	*suppOK = false
	loader.Refresh(t.Context())
	if len(versions) != 2 {
		t.Fatal("failed refresh notified")
	}
	restored := &schedule.Store{}
	loader.store = restored
	loader.OnPublication = func(reason string) {
		if restored.Load() == nil {
			t.Fatal("restore callback before publication")
		}
		reasons = append(reasons, reason)
	}
	loader.Restore(t.Context())
	if len(reasons) != 3 || reasons[2] != "cache_restored" || restored.Load().Metadata().ValidatedAt != versions[1].Metadata().ValidatedAt {
		t.Fatalf("restore callback: %v", reasons)
	}
}

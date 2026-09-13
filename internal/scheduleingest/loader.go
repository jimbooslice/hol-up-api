package scheduleingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"hol-up-prototype/internal/schedule"
)

const (
	SupplementedURL = "https://rrgtfsfeeds.s3.amazonaws.com/gtfs_supplemented.zip"
	RegularURL      = "https://rrgtfsfeeds.s3.amazonaws.com/gtfs_subway.zip"
	PollInterval    = time.Hour
	FetchTimeout    = time.Minute
	DefaultMaxAge   = 24 * time.Hour
	DefaultCacheDir = "/tmp/hol-up/schedule"
)

var ErrCoverage = errors.New("schedule does not cover current service date")

type HTTPStatusError int

func (e HTTPStatusError) Error() string { return fmt.Sprintf("upstream HTTP status %d", e) }

type Config struct {
	SupplementedURL, RegularURL string
	MaxAge                      time.Duration
}
type failure struct {
	started time.Time
	count   int
}

// Loader serializes cache restoration and refreshes. Network I/O never holds a
// store/reader lock; the store's only operation is atomic publication.
type Loader struct {
	// OnPublication runs after every publication. Configure before Run/Refresh/Restore.
	OnPublication func(string)
	client        *http.Client
	store         *schedule.Store
	cache         Cache
	config        Config
	logger        *slog.Logger
	now           func() time.Time
	mu            sync.Mutex
	failures      map[schedule.Source]failure
}

func NewLoader(client *http.Client, store *schedule.Store, cache Cache, config Config, logger *slog.Logger, now func() time.Time) *Loader {
	if client == nil {
		client = &http.Client{Timeout: FetchTimeout}
	}
	if config.SupplementedURL == "" {
		config.SupplementedURL = SupplementedURL
	}
	if config.RegularURL == "" {
		config.RegularURL = RegularURL
	}
	if config.MaxAge <= 0 {
		config.MaxAge = DefaultMaxAge
	}
	return &Loader{client: client, store: store, cache: cache, config: config, logger: logger, now: now, failures: map[schedule.Source]failure{}}
}

func (l *Loader) Run(ctx context.Context) {
	l.Restore(ctx)
	l.Refresh(ctx)
	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.Refresh(ctx)
		}
	}
}

// Restore revalidates the archive and derived metadata without renewing age.
// An expired cache can be retained for inspection, but Eligible remains false.
func (l *Loader) Restore(ctx context.Context) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cache == nil || ctx.Err() != nil {
		return
	}
	started := l.now()
	entry, err := l.cache.Load()
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var v *schedule.Version
	if err == nil {
		m := entry.Metadata
		if entry.Schema != cacheSchema || m.ParserSchema != ParserSchema || m.ArchiveSHA256 != archiveHash(entry.Archive) || m.ValidatedAt.After(l.now()) || m.PublishedAt.After(l.now()) {
			err = ErrCache
		} else {
			var dataset *schedule.Dataset
			dataset, err = Decode(entry.Archive)
			if err == nil {
				v, err = schedule.NewVersion(dataset, m)
				if err == nil && v.Metadata() != m {
					err = ErrCache
				}
			}
		}
	}
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		l.logger.Warn("Schedule cache rejected", "event", "schedule_cache_rejected", "error", safeError(err))
		return
	}
	m := v.Metadata()
	m.PublishedAt = l.now()
	v, err = schedule.NewVersion(v.Dataset(), m)
	if err != nil {
		return
	}
	reason := "cache_restored"
	if !v.Eligible(l.now(), l.config.MaxAge) {
		reason = "cache_ineligible"
	}
	l.store.Publish(v)
	if l.OnPublication != nil {
		l.OnPublication(reason)
	}
	l.logPublication(v, started, reason)
}

// Refresh always retries supplemented first; regular is fetched only when no
// eligible supplemented version remains, including after an upstream failure.
func (l *Loader) Refresh(ctx context.Context) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if ctx.Err() != nil {
		return
	}
	started := l.now()
	err := l.fetchAndPublish(ctx, schedule.Supplemented, l.config.SupplementedURL, "supplemented_validated", started)
	if ctx.Err() != nil {
		return
	}
	l.recordOutcome(schedule.Supplemented, err, started)
	if err == nil {
		return
	}
	current := l.store.Load()
	if current != nil && current.Metadata().Source == schedule.Supplemented && current.Eligible(l.now(), l.config.MaxAge) {
		l.logger.Info("Retaining supplemented schedule", "event", "schedule_retained", "source", schedule.Supplemented, "version", current.Metadata().Version, "reason", "supplemented_refresh_failed")
		return
	}
	reason := "no_supplemented_schedule"
	if current != nil && current.Metadata().Source == schedule.Supplemented {
		reason = "supplemented_expired"
		if !current.Dataset().Covers(l.now()) {
			reason = "supplemented_outside_coverage"
		} else if l.now().Before(current.Metadata().ValidatedAt) {
			reason = "supplemented_validation_in_future"
		}
	}
	l.logger.Info("Trying regular schedule fallback", "event", "schedule_fallback", "reason", reason)
	started = l.now()
	err = l.fetchAndPublish(ctx, schedule.Regular, l.config.RegularURL, reason, started)
	if ctx.Err() != nil {
		return
	}
	l.recordOutcome(schedule.Regular, err, started)
}

func (l *Loader) fetchAndPublish(ctx context.Context, source schedule.Source, address, reason string, started time.Time) error {
	fetchCtx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/zip, application/octet-stream")
	response, err := l.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return HTTPStatusError(response.StatusCode)
	}
	if response.ContentLength > MaxArchiveBytes {
		return ErrLimit
	}
	archive, err := io.ReadAll(io.LimitReader(response.Body, MaxArchiveBytes+1))
	if err != nil {
		return err
	}
	if int64(len(archive)) > MaxArchiveBytes {
		return ErrLimit
	}
	dataset, err := Decode(archive)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	now := l.now()
	v, err := schedule.NewVersion(dataset, schedule.Metadata{Source: source, ParserSchema: ParserSchema, ArchiveSHA256: archiveHash(archive), ValidatedAt: now, PublishedAt: now})
	if err != nil {
		return err
	}
	if !v.Eligible(now, l.config.MaxAge) {
		return ErrCoverage
	}
	l.store.Publish(v)
	if l.OnPublication != nil {
		l.OnPublication(reason)
	}
	l.logPublication(v, started, reason)
	if l.cache != nil {
		if err := l.cache.Save(CacheEntry{Schema: cacheSchema, Metadata: v.Metadata(), Archive: archive}); err != nil {
			l.logger.Warn("Schedule cache write failed", "event", "schedule_cache_write_failed", "error", "schedule cache write failed", "version", v.Metadata().Version)
		}
	}
	return nil
}

func archiveHash(archive []byte) string {
	sum := sha256.Sum256(archive)
	return hex.EncodeToString(sum[:])
}

func (l *Loader) logPublication(v *schedule.Version, started time.Time, reason string) {
	m := v.Metadata()
	l.logger.Info("Schedule published", "event", "schedule_published", "source", m.Source, "version", m.Version, "published_at", m.PublishedAt, "validated_at", m.ValidatedAt, "coverage", m.Coverage, "planning_coverage", m.PlanningCoverage, "counts", v.Dataset().Counts(), "duration_ms", l.now().Sub(started).Milliseconds(), "selection_reason", reason)
}

func (l *Loader) recordOutcome(source schedule.Source, err error, started time.Time) {
	f := l.failures[source]
	if err != nil {
		f.count++
		if f.count == 1 {
			f.started = started
			l.logger.Warn("Schedule refresh failed", "event", "schedule_refresh_failed", "source", source, "error", safeError(err), "duration_ms", l.now().Sub(started).Milliseconds(), "snapshot_available", l.store.Load() != nil)
		}
		l.failures[source] = f
	} else if f.count > 0 {
		l.logger.Info("Schedule refresh recovered", "event", "schedule_refresh_recovered", "source", source, "failed_refreshes", f.count, "outage_duration_ms", l.now().Sub(f.started).Milliseconds())
		delete(l.failures, source)
	}
}

// Never log upstream URLs, CSV values, response bodies, or filesystem paths.
func safeError(err error) string {
	if errors.Is(err, context.Canceled) {
		return "operation canceled"
	}
	var network net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &network) && network.Timeout()) {
		return "operation timed out"
	}
	var status HTTPStatusError
	if errors.As(err, &status) {
		return status.Error()
	}
	for _, known := range []error{ErrLimit, ErrInvalid, ErrCoverage, ErrCache} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	return "schedule operation failed"
}

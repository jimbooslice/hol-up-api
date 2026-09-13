package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"hol-up-prototype/internal/httpapi"
	"hol-up-prototype/internal/ingest"
	"hol-up-prototype/internal/realtime"
	"hol-up-prototype/internal/telemetry"
)

const (
	defaultAddress         = ":8080"
	defaultNumberedFeedURL = "https://api-endpoint.mta.info/Dataservice/mtagtfsfeeds/nyct%2Fgtfs"
	defaultACEFeedURL      = defaultNumberedFeedURL + "-ace"
	defaultBDFMFeedURL     = defaultNumberedFeedURL + "-bdfm"
	defaultGFeedURL        = defaultNumberedFeedURL + "-g"
	defaultJZFeedURL       = defaultNumberedFeedURL + "-jz"
	defaultLFeedURL        = defaultNumberedFeedURL + "-l"
	defaultNQRWFeedURL     = defaultNumberedFeedURL + "-nqrw"
	defaultSIFeedURL       = defaultNumberedFeedURL + "-si"
	pollInterval           = 30 * time.Second
	staleAfter             = time.Minute
)

var errInvalidMaxFutureSkew = errors.New("MTA_GTFS_RT_MAX_FUTURE_SKEW must be a non-negative Go duration")

type feed struct {
	name                  string
	routes                []string
	url                   string
	loader                *ingest.Loader
	store                 *realtime.Store
	failureStarted        time.Time
	failedRefreshes       uint64
	futureTimestampWarned bool
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "hol-up-api")
	if err := run(logger); err != nil {
		logger.Error("API stopped", "event", "api_stopped", "error", logError(err))
		os.Exit(1)
	}
	logger.Info("API stopped", "event", "api_stopped")
}

func run(logger *slog.Logger) error {
	maxFutureSkew, err := configuredMaxFutureSkew()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := &http.Client{Timeout: 10 * time.Second}
	feeds := configuredFeeds(client, maxFutureSkew, time.Now)
	sources := make([]httpapi.Source, len(feeds))
	names := make([]string, len(feeds))
	for index, feed := range feeds {
		sources[index] = httpapi.Source{Name: feed.name, Routes: feed.routes, Store: feed.store}
		names[index] = feed.name
	}
	metrics := telemetry.New(names...)

	server := &http.Server{
		Addr:              env("ADDR", defaultAddress),
		Handler:           httpapi.NewHandler(sources, metrics, time.Now, staleAfter),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       time.Minute,
	}
	logger.Info("API starting", "event", "api_starting", "address", server.Addr, "feed_count", len(feeds), "poll_interval_seconds", pollInterval.Seconds(), "stale_after_seconds", staleAfter.Seconds(), "max_future_skew_seconds", maxFutureSkew.Seconds())
	go poll(ctx, logger, metrics, feeds)
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		logger.Info("API shutdown started", "event", "api_shutdown_started", "timeout_seconds", 10)
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownContext)
	}
}

func configuredMaxFutureSkew() (time.Duration, error) {
	duration, err := time.ParseDuration(env("MTA_GTFS_RT_MAX_FUTURE_SKEW", "30s"))
	if err != nil || duration < 0 {
		return 0, errInvalidMaxFutureSkew
	}
	return duration, nil
}

func configuredFeeds(client *http.Client, maxFutureSkew time.Duration, now func() time.Time) []feed {
	feeds := []feed{
		{name: "ace", routes: []string{"A", "C", "E", "H"}, url: env("MTA_GTFS_RT_URL", defaultACEFeedURL)},
		{name: "bdfm", routes: []string{"B", "D", "F", "FX", "M", "FS"}, url: env("MTA_GTFS_RT_BDFM_URL", defaultBDFMFeedURL)},
		{name: "g", routes: []string{"G"}, url: env("MTA_GTFS_RT_G_URL", defaultGFeedURL)},
		{name: "jz", routes: []string{"J", "Z"}, url: env("MTA_GTFS_RT_JZ_URL", defaultJZFeedURL)},
		{name: "l", routes: []string{"L"}, url: env("MTA_GTFS_RT_L_URL", defaultLFeedURL)},
		{name: "nqrw", routes: []string{"N", "Q", "R", "W"}, url: env("MTA_GTFS_RT_NQRW_URL", defaultNQRWFeedURL)},
		{name: "numbered", routes: []string{"1", "2", "3", "4", "5", "6", "6X", "7", "7X", "GS"}, url: env("MTA_GTFS_RT_NUMBERED_URL", defaultNumberedFeedURL)},
		{name: "si", routes: []string{"SI", "SS"}, url: env("MTA_GTFS_RT_SI_URL", defaultSIFeedURL)},
	}
	for index := range feeds {
		feeds[index].store = realtime.NewStore()
		feeds[index].loader = ingest.NewLoader(client, feeds[index].store, feeds[index].name, feeds[index].url, "", maxFutureSkew, now)
	}
	return feeds
}

func poll(ctx context.Context, logger *slog.Logger, metrics *telemetry.Store, feeds []feed) {
	refresh(ctx, logger, metrics, feeds)

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh(ctx, logger, metrics, feeds)
		}
	}
}

func refresh(ctx context.Context, logger *slog.Logger, metrics *telemetry.Store, feeds []feed) {
	var wait sync.WaitGroup
	for index := range feeds {
		source := &feeds[index]
		wait.Add(1)
		go func() {
			defer wait.Done()
			started := time.Now()
			err := source.loader.Load(ctx)
			if ctx.Err() != nil {
				return
			}
			completed := time.Now()
			duration := completed.Sub(started)
			sourceTimestamp, _, trainCount := source.store.Status(time.Time{}, 0)
			metrics.Record(source.name, err == nil, duration, completed, sourceTimestamp, trainCount)
			if err != nil {
				source.failedRefreshes++
				if source.failedRefreshes == 1 {
					source.failureStarted = started
				}
				var futureError *ingest.FutureTimestampError
				isFuture := errors.As(err, &futureError)
				if source.failedRefreshes == 1 || (isFuture && !source.futureTimestampWarned) {
					fields := []any{"event", "realtime_feed_refresh_failed", "feed", source.name, "error", logError(err), "duration_ms", duration.Milliseconds(), "snapshot_available", !sourceTimestamp.IsZero(), "train_count", trainCount}
					if !sourceTimestamp.IsZero() {
						fields = append(fields, "source_timestamp", sourceTimestamp)
					}
					if isFuture {
						fields = append(fields, "reason", "future_source_timestamp", "rejected_source_timestamp", futureError.SourceTimestamp.Unix(), "received_at", futureError.ReceivedAt, "max_future_skew_seconds", futureError.MaxFutureSkew.Seconds())
						source.futureTimestampWarned = true
					}
					logger.Warn("Realtime feed refresh failed", fields...)
				}
			} else if source.failedRefreshes > 0 {
				logger.Info("Realtime feed refresh recovered", "event", "realtime_feed_refresh_recovered", "feed", source.name, "failed_refreshes", source.failedRefreshes, "outage_duration_ms", completed.Sub(source.failureStarted).Milliseconds(), "duration_ms", duration.Milliseconds(), "source_timestamp", sourceTimestamp, "train_count", trainCount)
				source.failedRefreshes = 0
				source.failureStarted = time.Time{}
				source.futureTimestampWarned = false
			}
		}()
	}
	wait.Wait()
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// Return only application-owned summaries at the logging boundary.
func logError(err error) string {
	if errors.Is(err, context.Canceled) {
		return "operation canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "operation timed out"
	}
	if errors.Is(err, errInvalidMaxFutureSkew) {
		return errInvalidMaxFutureSkew.Error()
	}
	var futureError *ingest.FutureTimestampError
	if errors.As(err, &futureError) {
		return futureError.Error()
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return "operation timed out"
	}

	var statusError ingest.HTTPStatusError
	if errors.As(err, &statusError) {
		return fmt.Sprintf("upstream HTTP status %d", statusError)
	}
	if errors.Is(err, ingest.ErrFeedTooLarge) {
		return ingest.ErrFeedTooLarge.Error()
	}
	if errors.Is(err, ingest.ErrDifferentialFeed) {
		return ingest.ErrDifferentialFeed.Error()
	}
	if errors.Is(err, realtime.ErrMissingSourceTimestamp) {
		return realtime.ErrMissingSourceTimestamp.Error()
	}
	if errors.Is(err, realtime.ErrOutOfOrderSnapshot) {
		return realtime.ErrOutOfOrderSnapshot.Error()
	}

	var certificateError *tls.CertificateVerificationError
	var recordHeaderError tls.RecordHeaderError
	if errors.As(err, &certificateError) || errors.As(err, &recordHeaderError) {
		return "TLS failure"
	}
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		return "DNS resolution failed"
	}
	var operationError *net.OpError
	if errors.As(err, &operationError) {
		return "network operation failed"
	}
	var requestError *url.Error
	if errors.As(err, &requestError) {
		return "request failed"
	}
	return "operation failed"
}

package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
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

type feed struct {
	name   string
	routes []string
	url    string
	loader *ingest.Loader
	store  *realtime.Store
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("API stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := &http.Client{Timeout: 10 * time.Second}
	feeds := configuredFeeds(client)
	sources := make([]httpapi.Source, len(feeds))
	names := make([]string, len(feeds))
	for index, feed := range feeds {
		sources[index] = httpapi.Source{Name: feed.name, Routes: feed.routes, Store: feed.store}
		names[index] = feed.name
	}
	metrics := telemetry.New(names...)
	go poll(ctx, logger, metrics, feeds)

	server := &http.Server{
		Addr:              env("ADDR", defaultAddress),
		Handler:           httpapi.NewHandler(sources, metrics, time.Now, staleAfter),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       time.Minute,
	}
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("API listening", "address", server.Addr)
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownContext)
	}
}

func configuredFeeds(client *http.Client) []feed {
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
		feeds[index].loader = ingest.NewLoader(client, feeds[index].store, feeds[index].url, "")
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
	for _, source := range feeds {
		wait.Add(1)
		go func() {
			defer wait.Done()
			started := time.Now()
			err := source.loader.Load(ctx)
			sourceTimestamp, _, trainCount := source.store.Status(time.Time{}, 0)
			metrics.Record(source.name, err == nil, time.Since(started), time.Now(), sourceTimestamp, trainCount)
			if err != nil && !errors.Is(err, context.Canceled) {
				logger.Warn("realtime feed refresh failed; retaining last known good snapshot", "feed", source.name, "error", err)
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

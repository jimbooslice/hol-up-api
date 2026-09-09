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
	defaultAddress     = ":8080"
	defaultACEFeedURL  = "https://api-endpoint.mta.info/Dataservice/mtagtfsfeeds/nyct%2Fgtfs-ace"
	defaultNQRWFeedURL = "https://api-endpoint.mta.info/Dataservice/mtagtfsfeeds/nyct%2Fgtfs-nqrw"
	pollInterval       = 30 * time.Second
	staleAfter         = time.Minute
)

type feed struct {
	name   string
	routes []string
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
	feeds := []feed{
		{name: "ace", routes: []string{"A", "C", "E"}, store: realtime.NewStore()},
		{name: "nqrw", routes: []string{"N", "Q", "R", "W"}, store: realtime.NewStore()},
	}
	feeds[0].loader = ingest.NewLoader(client, feeds[0].store, env("MTA_GTFS_RT_URL", defaultACEFeedURL), "")
	feeds[1].loader = ingest.NewLoader(client, feeds[1].store, env("MTA_GTFS_RT_NQRW_URL", defaultNQRWFeedURL), "")
	metrics := telemetry.New(feeds[0].name, feeds[1].name)
	go poll(ctx, logger, metrics, feeds)

	sources := make([]httpapi.Source, len(feeds))
	for index, feed := range feeds {
		sources[index] = httpapi.Source{Name: feed.name, Routes: feed.routes, Store: feed.store}
	}

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

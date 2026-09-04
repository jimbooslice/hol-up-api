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

	store := realtime.NewStore()
	client := &http.Client{Timeout: 10 * time.Second}
	feeds := []feed{
		{name: "ace", store: realtime.NewStore()},
		{name: "nqrw", store: realtime.NewStore()},
	}
	feeds[0].loader = ingest.NewLoader(client, feeds[0].store, env("MTA_GTFS_RT_URL", defaultACEFeedURL), "")
	feeds[1].loader = ingest.NewLoader(client, feeds[1].store, env("MTA_GTFS_RT_NQRW_URL", defaultNQRWFeedURL), "")
	go poll(ctx, logger, store, feeds)

	server := &http.Server{
		Addr:              env("ADDR", defaultAddress),
		Handler:           httpapi.NewHandler(store, time.Now, staleAfter),
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

func poll(ctx context.Context, logger *slog.Logger, store *realtime.Store, feeds []feed) {
	refresh(ctx, logger, store, feeds)

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh(ctx, logger, store, feeds)
		}
	}
}

func refresh(ctx context.Context, logger *slog.Logger, aggregate *realtime.Store, feeds []feed) {
	var wait sync.WaitGroup
	for _, source := range feeds {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := source.loader.Load(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Warn("realtime feed refresh failed; retaining last known good snapshot", "feed", source.name, "error", err)
			}
		}()
	}
	wait.Wait()

	snapshots := make([]realtime.Snapshot, len(feeds))
	for index, source := range feeds {
		snapshots[index] = source.store.Load(time.Time{}, 0)
	}
	if snapshot, ok := realtime.Combine(snapshots...); ok {
		if err := aggregate.Replace(snapshot); err != nil {
			logger.Warn("aggregate realtime snapshot publication failed", "error", err)
		}
	}
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

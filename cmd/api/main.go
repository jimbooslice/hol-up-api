package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"hol-up-prototype/internal/httpapi"
	"hol-up-prototype/internal/ingest"
	"hol-up-prototype/internal/realtime"
)

const (
	defaultAddress = ":8080"
	defaultFeedURL = "https://api-endpoint.mta.info/Dataservice/mtagtfsfeeds/nyct%2Fgtfs-ace"
	pollInterval   = 30 * time.Second
	staleAfter     = time.Minute
)

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
	loader := ingest.NewLoader(&http.Client{Timeout: 10 * time.Second}, store, env("MTA_GTFS_RT_URL", defaultFeedURL), "A")
	go poll(ctx, logger, loader)

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

func poll(ctx context.Context, logger *slog.Logger, loader *ingest.Loader) {
	load := func() {
		if err := loader.Load(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Warn("realtime feed refresh failed; retaining last known good snapshot", "error", err)
		}
	}
	load()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			load()
		}
	}
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

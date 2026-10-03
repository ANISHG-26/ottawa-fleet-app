package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/database"
	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/telemetry"
	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/worker"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	logger, logFile, err := telemetry.NewLogger("assignment-worker")
	if err != nil {
		slog.Error("logger setup failed", "error", err)
		os.Exit(1)
	}
	defer logFile.Close()
	slog.SetDefault(logger)
	telemetryRuntime, err := telemetry.Init(ctx, "assignment-worker")
	if err != nil {
		logger.Error("telemetry setup failed", "error", err)
		os.Exit(1)
	}
	defer func() {
		shutdown, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if err := telemetryRuntime.Shutdown(shutdown); err != nil {
			logger.Warn("telemetry shutdown failed", "error", err)
		}
	}()
	openCtx, openCancel := context.WithTimeout(ctx, 5*time.Second)
	defer openCancel()
	db, err := database.Open(openCtx, os.Getenv("DATABASE_URL"))
	if err != nil {
		logger.Error("database connection failed", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	instance, err := worker.NewOwner()
	if err != nil {
		logger.Error("worker identity generation failed", "error", err)
		os.Exit(1)
	}
	processor := &worker.Worker{Store: worker.NewPostgresStore(db), Fleet: worker.NewHTTPFleet(env("FLEET_API_URL", "http://localhost:8080"), telemetry.NewHTTPTransport("assignment-worker", nil)), Owner: instance, Logger: logger, PauseAfterReservation: os.Getenv("PAUSE_AFTER_RESERVATION") == "1"}
	addr := env("HTTP_ADDR", ":8082")
	server := &http.Server{Addr: addr, Handler: telemetry.HTTPMiddleware("assignment-worker")(processor.Handler(func(ctx context.Context) error {
		var compatible bool
		if err := db.PingContext(ctx); err != nil {
			return err
		}
		if _, err := database.DatasetEpoch(ctx, db); err != nil {
			return err
		}
		if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM app_schema_migrations WHERE name='ride/001_ride_jobs.sql')`).Scan(&compatible); err != nil {
			return err
		}
		if !compatible {
			return fmt.Errorf("ride schema migration is missing")
		}
		return nil
	})), ReadHeaderTimeout: 5 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()
	logger.Info("assignment worker listening", "addr", addr, "worker_id", instance)
	runErr := make(chan error, 1)
	go func() { runErr <- processor.Run(ctx) }()
	select {
	case err = <-serveErr:
		if err != http.ErrServerClosed {
			logger.Error("http server stopped", "error", err)
			cancel()
		}
	case err = <-runErr:
		if err != nil {
			logger.Error("worker stopped", "error", err)
		}
		cancel()
	case <-ctx.Done():
	}
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdown); err != nil {
		logger.Error("http shutdown failed", "error", err)
	}
	select {
	case <-runErr:
	case <-shutdown.Done():
		logger.Warn("worker shutdown reached 10 second limit")
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

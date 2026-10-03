package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/database"
	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/fleet"
	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/httpapi"
	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/telemetry"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	logger, logFile, err := telemetry.NewLogger("fleet-api")
	if err != nil {
		slog.Error("logger setup failed", "error", err)
		os.Exit(1)
	}
	defer logFile.Close()
	slog.SetDefault(logger)
	telemetryRuntime, err := telemetry.Init(ctx, "fleet-api")
	if err != nil {
		slog.Error("telemetry setup failed", "error", err)
		os.Exit(1)
	}
	defer func() {
		shutdown, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if err := telemetryRuntime.Shutdown(shutdown); err != nil {
			slog.Warn("telemetry shutdown failed", "error", err)
		}
	}()
	db, err := database.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		slog.Error("database connection failed", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	latency := boundedEnvInt("FLEET_FAULT_LATENCY_MS", 0, 1000)
	errorPercent := boundedEnvInt("FLEET_FAULT_ERROR_PERCENT", 0, 50)
	api := fleet.NewHandler(db, time.Now, fleet.FaultConfig{Latency: time.Duration(latency) * time.Millisecond, ErrorPercent: errorPercent, ControlEnabled: os.Getenv("FLEET_FAULT_CONTROL") == "1"})
	if os.Getenv("FLEET_SYNTHETIC_OBSERVATIONS") == "1" {
		interval := boundedEnvInt("FLEET_OBSERVATION_INTERVAL_SECONDS", 10, 60)
		fleet.StartSyntheticObservationFeed(ctx, db, time.Now, time.Duration(interval)*time.Second, slog.Default())
		slog.Info("synthetic observation feed enabled", "interval_seconds", interval)
	}
	server := &http.Server{Addr: addr, Handler: telemetry.HTTPMiddleware("fleet-api")(httpapi.RequestID(api)), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			slog.Error("graceful shutdown failed", "error", err)
		}
	}()
	slog.Info("fleet API starting", "addr", addr, "fault_latency_ms", latency, "fault_error_percent", errorPercent)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("fleet API stopped", "error", err)
		os.Exit(1)
	}
}

func boundedEnvInt(name string, fallback, minMax int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return fallback
	}
	if n > minMax {
		return minMax
	}
	return n
}

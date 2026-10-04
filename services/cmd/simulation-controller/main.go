package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/database"
	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/simulation"
	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/telemetry"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	logger, logFile, err := telemetry.NewLogger("simulation-controller")
	if err != nil {
		slog.Error("logger setup failed", "error", err)
		os.Exit(1)
	}
	defer logFile.Close()
	slog.SetDefault(logger)
	telemetryRuntime, err := telemetry.Init(ctx, "simulation-controller")
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
	controller := simulation.NewHandler(db, simulation.Config{Enabled: os.Getenv("SIMULATION_ENABLED") == "1", FleetURL: env("FLEET_API_URL", "http://localhost:8080"), RideURL: env("RIDE_API_URL", "http://localhost:8081")})
	server := &http.Server{Addr: env("HTTP_ADDR", ":8083"), Handler: telemetry.HTTPMiddleware("simulation-controller")(controller.Handler()), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	workerErr := make(chan error, 1)
	go func() { workerErr <- controller.RunWorker(ctx) }()
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()
	logger.Info("simulation controller listening", "addr", server.Addr)
	select {
	case err = <-serveErr:
		if err != http.ErrServerClosed {
			logger.Error("controller stopped", "error", err)
			cancel()
		}
	case err = <-workerErr:
		logger.Error("controller dispatcher stopped", "error", err)
		cancel()
	case <-ctx.Done():
	}
	shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if err := server.Shutdown(shutdown); err != nil {
		logger.Warn("http shutdown failed", "error", err)
	}
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

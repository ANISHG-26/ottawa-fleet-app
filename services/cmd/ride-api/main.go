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
	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/ride"
	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/telemetry"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	logger, logFile, err := telemetry.NewLogger("ride-api")
	if err != nil {
		slog.Error("logger setup failed", "error", err)
		os.Exit(1)
	}
	defer logFile.Close()
	slog.SetDefault(logger)
	telemetryRuntime, err := telemetry.Init(ctx, "ride-api")
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
	addr := env("HTTP_ADDR", ":8081")
	api := &ride.API{Store: ride.NewStore(db), DB: db}
	server := newHTTPServer(addr, telemetry.HTTPMiddleware("ride-api")(api.Handler()), 10*time.Second, 10*time.Second)
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()
	logger.Info("ride api listening", "addr", addr)
	select {
	case err = <-serveErr:
		if err != http.ErrServerClosed {
			logger.Error("http server stopped", "error", err)
			os.Exit(1)
		}
	case <-ctx.Done():
	}
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdown); err != nil {
		logger.Error("http shutdown failed", "error", err)
	}
}

func newHTTPServer(addr string, handler http.Handler, readTimeout, writeTimeout time.Duration) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       60 * time.Second,
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

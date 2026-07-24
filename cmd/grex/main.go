// grex is an OpAMP control plane for OpenTelemetry Collector fleets.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dennisme/grex/internal/config"
	"github.com/dennisme/grex/internal/server"
)

const shutdownGrace = 10 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "grex:", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "config.yaml", "path to the grex config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	logger := newLogger(cfg.Log)
	slog.SetDefault(logger)

	srv := server.New(cfg, logger)
	if err := srv.Start(); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case <-ctx.Done():
		logger.Info("shutting down", "reason", "signal")
	case err := <-srv.Fatal():
		logger.Error("listener failed", "error", err)
		shutdownErr := shutdown(srv)
		if shutdownErr != nil {
			logger.Error("shutdown failed", "error", shutdownErr)
		}
		return err
	}
	return shutdown(srv)
}

func shutdown(srv *server.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	return srv.Shutdown(ctx)
}

func newLogger(cfg config.Log) *slog.Logger {
	var level slog.Level
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if cfg.Format == "json" {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		handler = slog.NewTextHandler(os.Stderr, opts)
	}
	return slog.New(handler)
}

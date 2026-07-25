// grex is an OpAMP control plane for OpenTelemetry Collector fleets.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"net/http"

	"github.com/dennisme/grex/internal/api"
	"github.com/dennisme/grex/internal/buildinfo"
	"github.com/dennisme/grex/internal/config"
	"github.com/dennisme/grex/internal/fleet"
	"github.com/dennisme/grex/internal/metrics"
	"github.com/dennisme/grex/internal/opamp"
	"github.com/dennisme/grex/internal/server"
	"github.com/dennisme/grex/internal/ui"
)

const (
	shutdownGrace = 10 * time.Second
	// drainDelay gives an orchestrator's readiness probe time to observe
	// /readyz turning unready before listeners actually close, so new
	// traffic stops arriving before in-flight connections are cut.
	drainDelay = 5 * time.Second
)

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

	serverMetrics := metrics.NewRegistry()
	fleetMetrics := prometheus.NewRegistry()
	metrics.NewInfoGauge(serverMetrics, "grex_build_info", "Build information.", prometheus.Labels{
		"version":    buildinfo.Version,
		"commit":     buildinfo.Commit,
		"go_version": runtime.Version(),
	})
	metrics.NewInfoGauge(serverMetrics, "grex_config_info", "Non-secret configuration values in effect.", prometheus.Labels{
		"log_level":               cfg.Log.Level,
		"log_format":              cfg.Log.Format,
		"tls_enabled":             strconv.FormatBool(cfg.TLS.CertFile != ""),
		"mtls_enabled":            strconv.FormatBool(cfg.TLS.ClientCAFile != ""),
		"heartbeat_interval":      cfg.Fleet.HeartbeatInterval.String(),
		"stale_missed_heartbeats": strconv.Itoa(cfg.Fleet.StaleMissedHeartbeats),
		"per_agent_series_limit":  strconv.Itoa(cfg.Metrics.PerAgentSeriesLimit),
	})
	events := metrics.NewEvents(serverMetrics, fleetMetrics)
	registry := fleet.New(fleet.Config{
		HeartbeatInterval:     cfg.Fleet.HeartbeatInterval,
		StaleMissedHeartbeats: cfg.Fleet.StaleMissedHeartbeats,
		RequiredAttributes:    cfg.Fleet.RequiredAttributes,
	}, logger, events)
	fleetMetrics.MustRegister(metrics.NewFleetCollector(registry, cfg.Metrics.PerAgentSeriesLimit))
	handler, connCtx, err := opamp.New(logger, registry, events).Attach()
	if err != nil {
		return err
	}

	startedAt := time.Now()
	httpMetrics := metrics.NewHTTPMetrics(serverMetrics)
	uiMux := http.NewServeMux()
	api.New(registry, startedAt).Mount(uiMux, httpMetrics.Instrument)
	uiHandler, err := ui.New(registry, ui.Config{PollInterval: cfg.UI.PollInterval}, startedAt)
	if err != nil {
		return fmt.Errorf("ui: %w", err)
	}
	uiHandler.Mount(uiMux)

	srv := server.New(cfg, logger,
		server.OpAMP{Handler: handler, ConnContext: connCtx},
		server.UI{Handler: uiMux},
		serverMetrics, fleetMetrics)
	if err := srv.Start(); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go registry.Run(ctx)

	select {
	case <-ctx.Done():
		logger.Info("shutting down", "reason", "signal")
		srv.BeginDraining()
		logger.Info("draining", "delay", drainDelay)
		time.Sleep(drainDelay)
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

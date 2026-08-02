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
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/riverqueue/river"

	"net/http"

	"riverqueue.com/riverui"

	"github.com/dennisme/grex/internal/api"
	"github.com/dennisme/grex/internal/buildinfo"
	"github.com/dennisme/grex/internal/config"
	"github.com/dennisme/grex/internal/fleet"
	"github.com/dennisme/grex/internal/metrics"
	"github.com/dennisme/grex/internal/opamp"
	"github.com/dennisme/grex/internal/persistence"
	"github.com/dennisme/grex/internal/server"
	"github.com/dennisme/grex/internal/ui"
)

// mountRiverUI wraps River's own job/queue UI (riverqueue.com/riverui) at
// /riverui, reusing the same River client the purge job already runs
// (persistence.NewPurgeClient) — no second River client. No riverui-
// specific auth added: it mounts on uiMux, the same mux server.New wraps
// once with mTLS + SPIFFE role mapping when ui_tls.client_ca_file is
// configured (see docs/admin/authentication.md), so this route inherits
// that access control for free. That's a different axis from the
// browser-login OIDC flow that's still unbuilt (issue #11) — this isn't
// blocked on that, and doesn't need riverui's own
// RIVER_BASIC_AUTH_USER/PASS mechanism, which would be a second,
// inconsistent auth story to rip out later. Caller must still call
// Start(ctx) on the returned handler before it's fully functional (caching
// and background query support), same two-step shape as riverui's own
// example.
func mountRiverUI(uiMux *http.ServeMux, purgeClient *river.Client[pgx.Tx], logger *slog.Logger) (*riverui.Handler, error) {
	handler, err := riverui.NewHandler(&riverui.HandlerOpts{
		Logger:    logger,
		Prefix:    "/riverui",
		Endpoints: riverui.NewEndpoints(purgeClient, nil),
	})
	if err != nil {
		return nil, fmt.Errorf("river ui: %w", err)
	}
	uiMux.Handle("/riverui/", handler)
	return handler, nil
}

const shutdownGrace = 10 * time.Second

// persistenceFlushInterval is how often dirty agents are saved to the
// database, when one is configured. Separate from Sweep's ticker: liveness
// and durability are different concerns.
const persistenceFlushInterval = 5 * time.Second

// sessionSnapshotInterval is how often every registered agent's session
// state (agent_session) is wholesale-written, independent of dirty
// tracking — see persistence.SessionSnapshotter. agent_session's row is
// small (no JSONB), so this can run at the same cadence as the dirty flush
// without the write-amplification cost a wholesale agents-table rewrite
// would have at fleet scale.
const sessionSnapshotInterval = 5 * time.Second

// drainDelay gives an orchestrator's readiness probe time to observe
// /readyz turning unready before listeners actually close, so new
// traffic stops arriving before in-flight connections are cut.
// Overridable in tests to avoid sleeping the full production window.
var drainDelay = 5 * time.Second

// exitFunc is os.Exit in production; tests may override to avoid process exit.
var exitFunc = os.Exit

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "grex:", err)
		exitFunc(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("grex", flag.ContinueOnError)
	configPath := fs.String("config", "config.yaml", "path to the grex config file")
	if err := fs.Parse(args); err != nil {
		return err
	}

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
		"opamp_tls_enabled":       strconv.FormatBool(cfg.OpAMPTLS.CertFile != ""),
		"opamp_mtls_enabled":      strconv.FormatBool(cfg.OpAMPTLS.ClientCAFile != ""),
		"ui_tls_enabled":          strconv.FormatBool(cfg.UITLS.CertFile != ""),
		"ui_mtls_enabled":         strconv.FormatBool(cfg.UITLS.ClientCAFile != ""),
		"telemetry_tls_enabled":   strconv.FormatBool(cfg.TelemetryTLS.CertFile != ""),
		"telemetry_mtls_enabled":  strconv.FormatBool(cfg.TelemetryTLS.ClientCAFile != ""),
		"auth_default_role":       cfg.Auth.DefaultRole,
		"heartbeat_interval":      cfg.Fleet.HeartbeatInterval.String(),
		"stale_missed_heartbeats": strconv.Itoa(cfg.Fleet.StaleMissedHeartbeats),
		"per_agent_series_limit":  strconv.Itoa(cfg.Metrics.PerAgentSeriesLimit),
	})
	events := metrics.NewEvents(serverMetrics, fleetMetrics)
	registryEvents := fleet.Events(events)

	// Persistence is entirely opt-in: unset database.host means grex behaves
	// exactly as it does without a database configured at all.
	var dbPool *pgxpool.Pool
	var dirtyTracker *persistence.DirtyTracker
	var store persistence.StateStore
	var connStore persistence.ConnectionStore
	var jobQueue persistence.JobQueue
	var maxConcurrentWrites int
	var purgeClient *river.Client[pgx.Tx]
	var replicaID, replicaLabel string
	if cfg.Database.Host != "" {
		dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
			cfg.Database.Host, cfg.Database.Port, cfg.Database.User, cfg.Database.Password,
			cfg.Database.DBName, cfg.Database.SSLMode)
		pool, err := pgxpool.New(context.Background(), dsn)
		if err != nil {
			return fmt.Errorf("database: %w", err)
		}
		dbPool = pool
		defer dbPool.Close()
		pgStore := persistence.NewPostgresStore(pool)
		store = pgStore
		connStore = pgStore
		jobQueue = pgStore
		dirtyTracker = persistence.NewDirtyTracker()
		registryEvents = fleet.MultiEvents(events, dirtyTracker)
		fleetMetrics.MustRegister(persistence.NewPoolCollector(pool))
		// Bounding concurrent writes at the pool's own configured size means
		// grex never asks Postgres for more connections than pgxpool already
		// knows it has, self-consistent by construction (see
		// persistence.PoolCollector's doc comment on why this defaults to the
		// grex process's own CPU count, not the database's actual capacity).
		maxConcurrentWrites = int(pool.Config().MaxConns)

		// replicaID identifies this process for agent_connections (see
		// docs/spec/design.md's Dispatch routing section) and doubles as
		// River's own Config.ID. A random UUID generated once here, in
		// memory only — not the pod name or hostname: those churn on
		// restart and aren't guaranteed unique across clusters that could
		// share one Postgres. replicaLabel is the pod name/hostname anyway,
		// but only as a human-readable debug label, never for routing or
		// uniqueness.
		replicaID = uuid.NewString()
		replicaLabel, _ = os.Hostname()

		// Constructed here (not deferred to the goroutine-launching block
		// below) so mountRiverUI can mount its handler on uiMux before
		// srv.Start() begins serving it — registering a new route on a mux
		// already being served concurrently is a race. Only Start(ctx),
		// which needs the shutdown context, waits until after srv.Start().
		purgeClient, err = persistence.NewPurgeClient(pool, cfg.Fleet.SoftDeleteDuration, events, logger, replicaID)
		if err != nil {
			return fmt.Errorf("purge client: %w", err)
		}
	}

	registry := fleet.New(fleet.Config{
		HeartbeatInterval:     cfg.Fleet.HeartbeatInterval,
		StaleMissedHeartbeats: cfg.Fleet.StaleMissedHeartbeats,
		RequiredAttributes:    cfg.Fleet.RequiredAttributes,
		ShardCount:            cfg.Fleet.ShardCount,
	}, logger, registryEvents)
	fleetMetrics.MustRegister(metrics.NewFleetCollector(registry, cfg.Metrics.PerAgentSeriesLimit))
	opampHandler := opamp.New(logger, registry, events)
	handler, connCtx, err := opampHandler.Attach()
	if err != nil {
		return err
	}

	startedAt := time.Now()
	httpMetrics := metrics.NewHTTPMetrics(serverMetrics)
	uiMux := http.NewServeMux()
	api.New(registry, startedAt, store, events, jobQueue).Mount(uiMux, httpMetrics.Instrument)
	uiHandler, err := ui.New(registry, ui.Config{PollInterval: cfg.UI.PollInterval}, startedAt, store, events)
	if err != nil {
		return fmt.Errorf("ui: %w", err)
	}
	uiHandler.Mount(uiMux)

	var riverUIHandler *riverui.Handler
	if dbPool != nil {
		riverUIHandler, err = mountRiverUI(uiMux, purgeClient, logger)
		if err != nil {
			return err
		}
	}

	srv := server.New(cfg, logger,
		server.OpAMP{Handler: handler, ConnContext: connCtx},
		server.UI{Handler: uiMux},
		events,
		serverMetrics, fleetMetrics)
	if err := srv.Start(); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// persistCtx outlives ctx through the drain window below: Registry.Run/
	// Flusher/SessionSnapshotter must keep working while agents still
	// connected finish sending messages during BeginDraining's drain delay,
	// not die the instant the signal arrives. Cancelled explicitly, only
	// after that drain window ends, so their final flush (see Flusher.Run/
	// SessionSnapshotter.Run) actually has something worth catching.
	persistCtx, cancelPersist := context.WithCancel(context.Background())
	defer cancelPersist()
	var persistWG sync.WaitGroup

	persistWG.Add(1)
	go func() { defer persistWG.Done(); registry.Run(persistCtx) }()
	if dbPool != nil {
		flusher := persistence.NewFlusher(registry, dirtyTracker, store, persistenceFlushInterval, logger, maxConcurrentWrites, events, connStore, replicaID, replicaLabel)
		persistWG.Add(1)
		go func() { defer persistWG.Done(); flusher.Run(persistCtx) }()

		snapshotter := persistence.NewSessionSnapshotter(registry, store, sessionSnapshotInterval, logger, maxConcurrentWrites, events)
		persistWG.Add(1)
		go func() { defer persistWG.Done(); snapshotter.Run(persistCtx) }()

		if err := purgeClient.Start(ctx); err != nil {
			return fmt.Errorf("start purge client: %w", err)
		}
		if err := riverUIHandler.Start(ctx); err != nil {
			return fmt.Errorf("start river ui: %w", err)
		}
		defer func() {
			stopCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
			defer cancel()
			if err := purgeClient.Stop(stopCtx); err != nil {
				logger.Error("purge client stop failed", "error", err)
			}
		}()
	}

	select {
	case <-ctx.Done():
		logger.Info("shutting down", "reason", "signal")
		// Refuse new agent connections immediately — a rejected client's
		// own exponential-backoff-with-jitter reconnect (opamp-go) lands it
		// on a different, still-ready replica behind the load balancer.
		// Existing connections are untouched; BeginDraining flips /readyz
		// for the same reason at the orchestrator level.
		opampHandler.Drain()
		srv.BeginDraining()
		logger.Info("draining", "delay", drainDelay)
		time.Sleep(drainDelay)
	case err := <-srv.Fatal():
		logger.Error("listener failed", "error", err)
		cancelPersist()
		persistWG.Wait()
		shutdownErr := shutdown(srv)
		if shutdownErr != nil {
			logger.Error("shutdown failed", "error", shutdownErr)
		}
		return err
	}
	// Drain window over: let Registry.Run/Flusher/SessionSnapshotter do
	// their final flush now, and wait for it to actually finish (each is
	// independently bounded by its own interval-as-timeout, same budget
	// class as shutdownGrace) before closing listeners.
	cancelPersist()
	persistWG.Wait()
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

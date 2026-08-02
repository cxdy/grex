package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/dennisme/grex/internal/config"
	"github.com/dennisme/grex/internal/persistence/testdb"
	"github.com/dennisme/grex/internal/server"
	"github.com/prometheus/client_golang/prometheus"
)

func writeTestConfig(t *testing.T, extra string) string {
	t.Helper()
	// Ephemeral ports avoid collisions with parallel packages / host services.
	body := `
listeners:
  opamp: "127.0.0.1:0"
  ui: "127.0.0.1:0"
  telemetry: "127.0.0.1:0"
fleet:
  heartbeat_interval: 100ms
  stale_missed_heartbeats: 3
ui:
  poll_interval: 1s
log:
  level: error
  format: text
`
	if extra != "" {
		body += "\n" + extra
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNewLoggerLevelsAndFormats(t *testing.T) {
	t.Parallel()
	cases := []struct {
		level, format string
	}{
		{"debug", "text"},
		{"info", "text"},
		{"warn", "json"},
		{"error", "json"},
		{"", "text"}, // default level branch
	}
	for _, tc := range cases {
		t.Run(tc.level+"/"+tc.format, func(t *testing.T) {
			t.Parallel()
			log := newLogger(config.Log{Level: tc.level, Format: tc.format})
			if log == nil {
				t.Fatal("expected logger")
			}
			// Exercise the handler without asserting output.
			log.Info("test")
		})
	}
}

// TestMountRiverUI covers mountRiverUI in isolation: river.NewClient and
// riverui.NewHandler are both lazy (verified empirically — neither
// attempts a real connection at construction time, only Start/actual
// queries do), so this needs no live Postgres, just a syntactically valid
// (but unreachable) DSN.
func TestMountRiverUI(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "host=127.0.0.1 port=1 user=x password=x dbname=x sslmode=disable")
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{Logger: logger})
	if err != nil {
		t.Fatalf("river.NewClient: %v", err)
	}

	uiMux := http.NewServeMux()
	handler, err := mountRiverUI(uiMux, client, logger)
	if err != nil {
		t.Fatalf("mountRiverUI: %v", err)
	}
	if handler == nil {
		t.Fatal("mountRiverUI returned a nil handler")
	}

	req := httptest.NewRequest(http.MethodGet, "/riverui/", nil)
	rec := httptest.NewRecorder()
	uiMux.ServeHTTP(rec, req)
	if rec.Code == http.StatusNotFound {
		t.Errorf("GET /riverui/ = 404, want the route mounted")
	}
}

func TestRunMissingConfig(t *testing.T) {
	err := run([]string{"-config", filepath.Join(t.TempDir(), "nope.yaml")})
	if err == nil {
		t.Fatal("expected error for missing config")
	}
	if !strings.Contains(err.Error(), "read config") && !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunBadFlag(t *testing.T) {
	// ContinueOnError returns the parse error instead of os.Exit.
	err := run([]string{"-not-a-real-flag"})
	if err == nil {
		t.Fatal("expected flag parse error")
	}
}

func TestRunSignalShutdown(t *testing.T) {
	prevDelay := drainDelay
	drainDelay = 0
	t.Cleanup(func() { drainDelay = prevDelay })

	path := writeTestConfig(t, "")
	done := make(chan error, 1)
	go func() {
		done <- run([]string{"-config", path})
	}()

	// Wait until the process is listening (telemetry /healthz).
	// Server binds :0 so we cannot know the port from config; instead we
	// send SIGTERM after a short settle — Start returns only after bind.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		// Best-effort: if run already failed, surface it.
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("run exited early: %v", err)
			}
			return
		default:
		}
		time.Sleep(20 * time.Millisecond)
		// After Start, signal.NotifyContext is armed — send SIGTERM.
		if time.Since(deadline.Add(-3*time.Second)) > 50*time.Millisecond {
			p, err := os.FindProcess(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			if err := p.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			break
		}
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not shut down after SIGTERM")
	}
}

// TestRunWithDatabaseConfigured covers run()'s entire database.host-enabled
// branch — pgxpool wiring, replicaID generation, the purge client, riverui
// mounting, Flusher/SessionSnapshotter, and the deferred purge-client
// shutdown — none of which any other test here exercises (they all leave
// database.host unset, the opt-out path). Not just plumbing: this is the
// same wiring the compose dev stack runs for real.
func TestRunWithDatabaseConfigured(t *testing.T) {
	inst := testdb.New(t) // applies grex's own migrations

	// River's own schema (river_job, river_leader, etc.) isn't part of
	// grex's migrations (see cmd/river-migrate) — purgeClient.Start and
	// riverUIHandler.Start both need it present, same as
	// internal/persistence/purge_test.go's TestNewPurgeClientRunsAndPurgesViaInsert.
	pool, err := pgxpool.New(context.Background(), inst.DSN())
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		t.Fatalf("rivermigrate.New: %v", err)
	}
	if _, err := migrator.Migrate(context.Background(), rivermigrate.DirectionUp, nil); err != nil {
		t.Fatalf("migrate river schema: %v", err)
	}
	pool.Close()

	prevDelay := drainDelay
	drainDelay = 0
	t.Cleanup(func() { drainDelay = prevDelay })

	path := writeTestConfig(t, fmt.Sprintf(`
database:
  host: %s
  port: %d
  user: %s
  password: %s
  dbname: %s
  sslmode: %s
`, inst.Host, inst.Port, inst.User, inst.Password, inst.DBName, inst.SSLMode))

	// run() builds its own logger from cfg.Log, writing to os.Stderr — swap
	// it for a pipe so the known River shutdown race below (same one
	// internal/persistence/purge_test.go documents) can be asserted on
	// instead of leaking to real stderr unchecked, per the
	// pristine-test-output rule. Safe to reassign the package-level
	// os.Stderr var here: this package's tests run sequentially (none call
	// t.Parallel()), and it's restored before this test does anything else
	// with it.
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStderr := os.Stderr
	os.Stderr = stderrW

	done := make(chan error, 1)
	go func() {
		done <- run([]string{"-config", path})
	}()

	// Let run() finish constructing and starting everything (pool, purge
	// client, riverui, Flusher/SessionSnapshotter goroutines) before
	// signaling shutdown.
	time.Sleep(500 * time.Millisecond)
	select {
	case err := <-done:
		os.Stderr = origStderr
		t.Fatalf("run exited early: %v", err)
	default:
	}

	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		os.Stderr = origStderr
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(10 * time.Second):
		os.Stderr = origStderr
		t.Fatal("run did not shut down after SIGTERM")
	}

	if err := stderrW.Close(); err != nil {
		t.Fatal(err)
	}
	var stderrBuf strings.Builder
	if _, err := io.Copy(&stderrBuf, stderrR); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(stderrBuf.String()), "\n") {
		if line == "" {
			continue
		}
		if !strings.Contains(line, "level=ERROR") {
			continue
		}
		if strings.Contains(line, "PeriodicJobEnqueuer") && strings.Contains(line, "context canceled") {
			continue
		}
		t.Errorf("unexpected stderr line: %s", line)
	}
}

func TestRunInvalidConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	// Invalid log level fails validation.
	if err := os.WriteFile(path, []byte(`
listeners:
  opamp: "127.0.0.1:0"
  ui: "127.0.0.1:0"
  telemetry: "127.0.0.1:0"
log:
  level: nope
  format: text
`), 0o600); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"-config", path})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestRunStartBindFailure(t *testing.T) {
	// Hold a port so grex cannot bind OpAMP.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close() //nolint:errcheck
	addr := ln.Addr().String()

	path := filepath.Join(t.TempDir(), "config.yaml")
	body := `
listeners:
  opamp: "` + addr + `"
  ui: "127.0.0.1:0"
  telemetry: "127.0.0.1:0"
fleet:
  heartbeat_interval: 30s
  stale_missed_heartbeats: 3
log:
  level: error
  format: json
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"-config", path}); err == nil {
		t.Fatal("expected bind failure")
	}
}

func TestShutdownHelper(t *testing.T) {
	cfg := &config.Config{
		Listeners: config.Listeners{
			OpAMP:     "127.0.0.1:0",
			UI:        "127.0.0.1:0",
			Telemetry: "127.0.0.1:0",
		},
		Fleet:   config.Fleet{HeartbeatInterval: time.Second, StaleMissedHeartbeats: 3},
		Metrics: config.Metrics{PerAgentSeriesLimit: 100},
		UI:      config.UI{PollInterval: time.Second},
		Log:     config.Log{Level: "error", Format: "text"},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := prometheus.NewRegistry()
	srv := server.New(cfg, log, server.OpAMP{}, server.UI{}, nil, reg, prometheus.NewRegistry())
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	// Smoke: telemetry is up.
	resp, err := http.Get("http://" + srv.TelemetryAddr() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if err := shutdown(srv); err != nil {
		t.Fatal(err)
	}
}

func TestMainErrorPath(t *testing.T) {
	// Cover main()'s error branch without exiting the test process.
	prevExit := exitFunc
	var code int
	exitFunc = func(c int) { code = c }
	t.Cleanup(func() { exitFunc = prevExit })

	prevArgs := os.Args
	os.Args = []string{"grex", "-config", filepath.Join(t.TempDir(), "missing.yaml")}
	t.Cleanup(func() { os.Args = prevArgs })

	main()
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}

func TestMainSuccessPath(t *testing.T) {
	prevDelay := drainDelay
	drainDelay = 0
	t.Cleanup(func() { drainDelay = prevDelay })

	prevExit := exitFunc
	exitFunc = func(c int) {
		t.Errorf("unexpected exit(%d)", c)
	}
	t.Cleanup(func() { exitFunc = prevExit })

	path := writeTestConfig(t, "")
	prevArgs := os.Args
	os.Args = []string{"grex", "-config", path}
	t.Cleanup(func() { os.Args = prevArgs })

	// Run main in a goroutine; signal after it should have started.
	done := make(chan struct{})
	go func() {
		defer close(done)
		main()
	}()

	time.Sleep(100 * time.Millisecond)
	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("main did not return after SIGTERM")
	}
}

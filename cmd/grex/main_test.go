package main

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dennisme/grex/internal/config"
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
	srv := server.New(cfg, log, server.OpAMP{}, server.UI{}, reg, prometheus.NewRegistry())
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

package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dennisme/grex/internal/config"
)

func testConfig() *config.Config {
	return &config.Config{
		Listeners: config.Listeners{
			OpAMP:     "127.0.0.1:0",
			UI:        "127.0.0.1:0",
			Telemetry: "127.0.0.1:0",
		},
		Fleet: config.Fleet{HeartbeatInterval: 30 * time.Second, StaleMissedHeartbeats: 3},
		Log:   config.Log{Level: "info", Format: "text"},
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func startServer(t *testing.T) *Server {
	t.Helper()
	s := New(testConfig(), testLogger(), OpAMP{})
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	return s
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec // test URL built from loopback listener
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(body)
}

func TestTelemetryEndpoints(t *testing.T) {
	s := startServer(t)
	base := "http://" + s.TelemetryAddr()

	if code, body := get(t, base+"/healthz"); code != http.StatusOK || !strings.Contains(body, "ok") {
		t.Errorf("/healthz = %d %q", code, body)
	}
	if code, _ := get(t, base+"/readyz"); code != http.StatusOK {
		t.Errorf("/readyz = %d", code)
	}
	code, body := get(t, base+"/metrics")
	if code != http.StatusOK {
		t.Errorf("/metrics = %d", code)
	}
	if !strings.Contains(body, "go_goroutines") {
		t.Error("/metrics missing go runtime collectors")
	}
}

func TestOpAMPAndUIAreStubs(t *testing.T) {
	s := startServer(t)
	for name, addr := range map[string]string{
		"opamp": s.OpAMPAddr(),
		"ui":    s.UIAddr(),
	} {
		if code, _ := get(t, "http://"+addr+"/"); code != http.StatusNotImplemented {
			t.Errorf("%s listener = %d, want %d", name, code, http.StatusNotImplemented)
		}
	}
}

func TestOpAMPHandlerMounted(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	s := New(testConfig(), testLogger(), OpAMP{Handler: handler})
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})

	if code, _ := get(t, "http://"+s.OpAMPAddr()+"/v1/opamp"); code != http.StatusOK {
		t.Errorf("/v1/opamp = %d, want 200 from mounted handler", code)
	}
	if code, _ := get(t, "http://"+s.OpAMPAddr()+"/other"); code != http.StatusNotImplemented {
		t.Errorf("/other = %d, want 501", code)
	}
}

func TestShutdownClosesListeners(t *testing.T) {
	s := New(testConfig(), testLogger(), OpAMP{})
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	addr := s.TelemetryAddr()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if _, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		t.Error("telemetry listener still accepting after Shutdown")
	}
}

func TestStartFailsOnUnbindableAddress(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lis.Close() }()

	cfg := testConfig()
	cfg.Listeners.OpAMP = lis.Addr().String()
	s := New(cfg, testLogger(), OpAMP{})
	if err := s.Start(); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
		t.Fatal("Start succeeded on an already-bound port")
	}
}

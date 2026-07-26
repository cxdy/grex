package server

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/dennisme/grex/internal/config"
	"github.com/dennisme/grex/internal/metrics"
	"github.com/dennisme/grex/internal/testcert"
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
	s := New(testConfig(), testLogger(), OpAMP{}, UI{}, nil, metrics.NewRegistry(), prometheus.NewRegistry())
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

// /readyz must flip to unready as soon as a graceful drain begins, before
// listeners close, so an orchestrator's readiness probe can stop routing new
// traffic during the drain window. /healthz stays a pure liveness signal and
// does not flip: the process is still alive and not deadlocked.
func TestReadyzReflectsDraining(t *testing.T) {
	s := startServer(t)
	base := "http://" + s.TelemetryAddr()

	if code, _ := get(t, base+"/readyz"); code != http.StatusOK {
		t.Fatalf("/readyz before draining = %d, want 200", code)
	}

	s.BeginDraining()

	if code, _ := get(t, base+"/readyz"); code != http.StatusServiceUnavailable {
		t.Errorf("/readyz while draining = %d, want 503", code)
	}
	if code, _ := get(t, base+"/healthz"); code != http.StatusOK {
		t.Errorf("/healthz while draining = %d, want 200 (liveness unaffected)", code)
	}
}

func TestPprofDisabledByDefault(t *testing.T) {
	s := startServer(t)
	base := "http://" + s.TelemetryAddr()

	if code, _ := get(t, base+"/debug/pprof/"); code != http.StatusNotFound {
		t.Errorf("/debug/pprof/ with pprof disabled = %d, want 404", code)
	}
}

func TestPprofEnabled(t *testing.T) {
	cfg := testConfig()
	cfg.Debug.PprofEnabled = true
	s := New(cfg, testLogger(), OpAMP{}, UI{}, nil, metrics.NewRegistry(), prometheus.NewRegistry())
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})
	base := "http://" + s.TelemetryAddr()

	if code, body := get(t, base+"/debug/pprof/"); code != http.StatusOK || !strings.Contains(body, "profile") {
		t.Errorf("/debug/pprof/ = %d %q, want 200 with profile index", code, body)
	}
	if code, _ := get(t, base+"/debug/pprof/cmdline"); code != http.StatusOK {
		t.Errorf("/debug/pprof/cmdline = %d, want 200", code)
	}
	if code, _ := get(t, base+"/debug/pprof/goroutine"); code != http.StatusOK {
		t.Errorf("/debug/pprof/goroutine = %d, want 200", code)
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

func TestMetricsEndpointsSeparated(t *testing.T) {
	fleetReg := prometheus.NewRegistry()
	fleetGauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "grex_test_fleet_series", Help: "test"})
	fleetReg.MustRegister(fleetGauge)
	fleetGauge.Set(1)

	s := New(testConfig(), testLogger(), OpAMP{}, UI{}, nil, metrics.NewRegistry(), fleetReg)
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})
	base := "http://" + s.TelemetryAddr()

	_, serverBody := get(t, base+"/metrics")
	if !strings.Contains(serverBody, "go_goroutines") {
		t.Error("/metrics missing runtime series")
	}
	if strings.Contains(serverBody, "grex_test_fleet_series") {
		t.Error("/metrics leaks fleet series")
	}

	_, fleetBody := get(t, base+"/metrics/fleet")
	if !strings.Contains(fleetBody, "grex_test_fleet_series") {
		t.Error("/metrics/fleet missing fleet series")
	}
	if strings.Contains(fleetBody, "go_goroutines") {
		t.Error("/metrics/fleet leaks runtime series")
	}
}

func TestOpAMPHandlerMounted(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	s := New(testConfig(), testLogger(), OpAMP{Handler: handler}, UI{}, nil, metrics.NewRegistry(), prometheus.NewRegistry())
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

func TestUIHandlerMounted(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/agents", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	s := New(testConfig(), testLogger(), OpAMP{}, UI{Handler: mux}, nil, metrics.NewRegistry(), prometheus.NewRegistry())
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})

	if code, _ := get(t, "http://"+s.UIAddr()+"/api/agents"); code != http.StatusOK {
		t.Errorf("/api/agents = %d, want 200 from mounted handler", code)
	}
}

func TestShutdownClosesListeners(t *testing.T) {
	s := New(testConfig(), testLogger(), OpAMP{}, UI{}, nil, metrics.NewRegistry(), prometheus.NewRegistry())
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	addr := s.TelemetryAddr()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	// TCP teardown at the OS level isn't necessarily atomic with Shutdown
	// returning, so poll briefly rather than asserting on a single Dial.
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			t.Error("telemetry listener still accepting after Shutdown")
			return
		}
		time.Sleep(20 * time.Millisecond)
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
	s := New(cfg, testLogger(), OpAMP{}, UI{}, nil, metrics.NewRegistry(), prometheus.NewRegistry())
	if err := s.Start(); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
		t.Fatal("Start succeeded on an already-bound port")
	}
}

func TestBoundAddrBeforeStartAndFatalChannel(t *testing.T) {
	s := New(testConfig(), testLogger(), OpAMP{}, UI{}, nil, metrics.NewRegistry(), prometheus.NewRegistry())
	if s.OpAMPAddr() != "" || s.UIAddr() != "" || s.TelemetryAddr() != "" {
		t.Fatal("bound addresses should be empty before Start")
	}
	// Fatal is a non-nil receive-only channel.
	ch := s.Fatal()
	if ch == nil {
		t.Fatal("Fatal channel nil")
	}
	select {
	case <-ch:
		t.Fatal("unexpected fatal before Start")
	default:
	}
}

func TestShutdownBeforeStartIsNoop(t *testing.T) {
	s := New(testConfig(), testLogger(), OpAMP{}, UI{}, nil, metrics.NewRegistry(), prometheus.NewRegistry())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown before Start: %v", err)
	}
}

func TestOpAMPTLSConfigErrors(t *testing.T) {
	// Missing keypair files.
	_, err := listenerTLSConfig(config.TLS{CertFile: "/no/such/cert.pem", KeyFile: "/no/such/key.pem"}, tls.RequireAndVerifyClientCert)
	if err == nil {
		t.Fatal("expected keypair load error")
	}

	// Valid keypair + missing client CA file.
	certs := testcert.Gen(t)
	_, err = listenerTLSConfig(config.TLS{
		CertFile:     certs.ServerCertFile,
		KeyFile:      certs.ServerKeyFile,
		ClientCAFile: "/no/such/ca.pem",
	}, tls.RequireAndVerifyClientCert)
	if err == nil {
		t.Fatal("expected client CA read error")
	}

	// Valid keypair + empty/non-PEM client CA file.
	badCA := t.TempDir() + "/empty-ca.pem"
	if err := os.WriteFile(badCA, []byte("not a cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = listenerTLSConfig(config.TLS{
		CertFile:     certs.ServerCertFile,
		KeyFile:      certs.ServerKeyFile,
		ClientCAFile: badCA,
	}, tls.RequireAndVerifyClientCert)
	if err == nil {
		t.Fatal("expected empty CA bundle error")
	}

	// Happy path: no TLS.
	cfg, err := listenerTLSConfig(config.TLS{}, tls.RequireAndVerifyClientCert)
	if err != nil || cfg != nil {
		t.Fatalf("empty TLS: cfg=%v err=%v", cfg, err)
	}
}

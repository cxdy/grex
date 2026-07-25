package server

import (
	"context"
	"crypto/tls"
	"net/http"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/dennisme/grex/internal/metrics"
	"github.com/dennisme/grex/internal/testcert"
)

func startTLSServer(t *testing.T, certs testcert.Certs, mtls bool) *Server {
	t.Helper()
	cfg := testConfig()
	cfg.TLS.CertFile = certs.ServerCertFile
	cfg.TLS.KeyFile = certs.ServerKeyFile
	if mtls {
		cfg.TLS.ClientCAFile = certs.CAFile
	}
	s := New(cfg, testLogger(), OpAMP{}, metrics.NewRegistry(), prometheus.NewRegistry())
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

func tlsGet(t *testing.T, addr string, tlsCfg *tls.Config) (int, error) {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}}
	defer client.CloseIdleConnections()
	resp, err := client.Get("https://" + addr + "/")
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}

func TestOpAMPListenerServesTLS(t *testing.T) {
	certs := testcert.Gen(t)
	s := startTLSServer(t, certs, false)

	code, err := tlsGet(t, s.OpAMPAddr(), &tls.Config{RootCAs: certs.CAPool, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("TLS GET: %v", err)
	}
	if code != http.StatusNotImplemented {
		t.Errorf("status = %d, want %d", code, http.StatusNotImplemented)
	}
}

func TestOpAMPListenerRequiresClientCert(t *testing.T) {
	certs := testcert.Gen(t)
	s := startTLSServer(t, certs, true)

	code, err := tlsGet(t, s.OpAMPAddr(), &tls.Config{
		RootCAs:      certs.CAPool,
		Certificates: []tls.Certificate{certs.ClientTLSCert},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("mTLS GET with client cert: %v", err)
	}
	if code != http.StatusNotImplemented {
		t.Errorf("status = %d, want %d", code, http.StatusNotImplemented)
	}

	if _, err := tlsGet(t, s.OpAMPAddr(), &tls.Config{RootCAs: certs.CAPool, MinVersion: tls.VersionTLS12}); err == nil {
		t.Error("GET without client cert succeeded, want handshake rejection")
	}
}

func TestUIAndTelemetryStayPlainHTTP(t *testing.T) {
	certs := testcert.Gen(t)
	s := startTLSServer(t, certs, true)

	if code, _ := get(t, "http://"+s.TelemetryAddr()+"/healthz"); code != http.StatusOK {
		t.Errorf("telemetry /healthz over plain HTTP = %d, want 200", code)
	}
	if code, _ := get(t, "http://"+s.UIAddr()+"/"); code != http.StatusNotImplemented {
		t.Errorf("ui over plain HTTP = %d, want 501", code)
	}
}

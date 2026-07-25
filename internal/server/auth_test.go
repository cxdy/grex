package server

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/dennisme/grex/internal/metrics"
	"github.com/dennisme/grex/internal/spiffe"
	"github.com/dennisme/grex/internal/testcert"
)

type fakeAuthMetrics struct {
	mu      sync.Mutex
	denied  []string
	allowed []string
}

func (f *fakeAuthMetrics) AuthDenied(reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.denied = append(f.denied, reason)
}

func (f *fakeAuthMetrics) AuthAllowed(role string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.allowed = append(f.allowed, role)
}

func mustSpiffeURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// startAuthServer configures mTLS with a role-mapping table on both the UI
// and telemetry listeners. The UI handler responds 200 "ui-ok" so tests can
// tell an authorized request from the 501 stub.
func startAuthServer(t *testing.T, certs testcert.Certs, rules []spiffe.RoleRule, defaultRole string, m AuthMetrics) *Server {
	t.Helper()
	cfg := testConfig()
	cfg.UITLS.CertFile = certs.ServerCertFile
	cfg.UITLS.KeyFile = certs.ServerKeyFile
	cfg.UITLS.ClientCAFile = certs.CAFile
	cfg.TelemetryTLS.CertFile = certs.ServerCertFile
	cfg.TelemetryTLS.KeyFile = certs.ServerKeyFile
	cfg.TelemetryTLS.ClientCAFile = certs.CAFile
	cfg.Auth.RoleMapping = rules
	cfg.Auth.DefaultRole = defaultRole

	ui := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ui-ok"))
	})

	s := New(cfg, testLogger(), OpAMP{}, UI{Handler: ui}, m, metrics.NewRegistry(), prometheus.NewRegistry())
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

func authGet(t *testing.T, addr, path string, tlsCfg *tls.Config) (int, string, error) {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}}
	defer client.CloseIdleConnections()
	resp, err := client.Get("https://" + addr + path)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body := make([]byte, 64)
	n, _ := resp.Body.Read(body)
	return resp.StatusCode, string(body[:n]), nil
}

func TestUIAllowsRoleMappedCert(t *testing.T) {
	certs := testcert.Gen(t)
	viewer := certs.IssueClient(t, "alice", []*url.URL{mustSpiffeURL(t, "spiffe://grex-api.internal/user/alice")})
	m := &fakeAuthMetrics{}
	s := startAuthServer(t, certs, []spiffe.RoleRule{
		{Match: "exact", SpiffeID: "spiffe://grex-api.internal/user/alice", Role: "viewer"},
	}, "none", m)

	code, body, err := authGet(t, s.UIAddr(), "/", &tls.Config{
		RootCAs:      certs.CAPool,
		Certificates: []tls.Certificate{viewer},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if code != http.StatusOK || !strings.Contains(body, "ui-ok") {
		t.Errorf("status=%d body=%q, want 200 ui-ok", code, body)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.allowed) != 1 || m.allowed[0] != "viewer" {
		t.Errorf("allowed = %v, want [viewer]", m.allowed)
	}
}

func TestUIDeniesCertWithNoRoleMapping(t *testing.T) {
	certs := testcert.Gen(t)
	unmapped := certs.IssueClient(t, "mallory", []*url.URL{mustSpiffeURL(t, "spiffe://grex-api.internal/user/mallory")})
	m := &fakeAuthMetrics{}
	s := startAuthServer(t, certs, []spiffe.RoleRule{
		{Match: "exact", SpiffeID: "spiffe://grex-api.internal/user/alice", Role: "viewer"},
	}, "none", m)

	code, _, err := authGet(t, s.UIAddr(), "/", &tls.Config{
		RootCAs:      certs.CAPool,
		Certificates: []tls.Certificate{unmapped},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", code)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.denied) != 1 || m.denied[0] != "no_role" {
		t.Errorf("denied = %v, want [no_role]", m.denied)
	}
}

func TestUIDeniesRequestWithNoCert(t *testing.T) {
	certs := testcert.Gen(t)
	m := &fakeAuthMetrics{}
	s := startAuthServer(t, certs, nil, "none", m)

	code, _, err := authGet(t, s.UIAddr(), "/", &tls.Config{RootCAs: certs.CAPool, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", code)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.denied) != 1 || m.denied[0] != "no_cert" {
		t.Errorf("denied = %v, want [no_cert]", m.denied)
	}
}

func TestTelemetryMetricsRequireRoleButHealthProbesStayOpen(t *testing.T) {
	certs := testcert.Gen(t)
	prom := certs.IssueClient(t, "prometheus", []*url.URL{mustSpiffeURL(t, "spiffe://grex-api.internal/service/prometheus")})
	m := &fakeAuthMetrics{}
	s := startAuthServer(t, certs, []spiffe.RoleRule{
		{Match: "exact", SpiffeID: "spiffe://grex-api.internal/service/prometheus", Role: "viewer"},
	}, "none", m)

	authedTLS := &tls.Config{RootCAs: certs.CAPool, Certificates: []tls.Certificate{prom}, MinVersion: tls.VersionTLS12}
	noCertTLS := &tls.Config{RootCAs: certs.CAPool, MinVersion: tls.VersionTLS12}

	if code, _, err := authGet(t, s.TelemetryAddr(), "/metrics", authedTLS); err != nil || code != http.StatusOK {
		t.Errorf("/metrics with role cert: code=%d err=%v, want 200", code, err)
	}
	if code, _, err := authGet(t, s.TelemetryAddr(), "/metrics", noCertTLS); err != nil || code != http.StatusForbidden {
		t.Errorf("/metrics without cert: code=%d err=%v, want 403", code, err)
	}
	if code, _, err := authGet(t, s.TelemetryAddr(), "/healthz", noCertTLS); err != nil || code != http.StatusOK {
		t.Errorf("/healthz without cert: code=%d err=%v, want 200", code, err)
	}
	if code, _, err := authGet(t, s.TelemetryAddr(), "/readyz", noCertTLS); err != nil || code != http.StatusOK {
		t.Errorf("/readyz without cert: code=%d err=%v, want 200", code, err)
	}
}

func TestUIAndTelemetryDefaultRoleGrantsAccess(t *testing.T) {
	certs := testcert.Gen(t)
	anyClient := certs.IssueClient(t, "someone", []*url.URL{mustSpiffeURL(t, "spiffe://grex-api.internal/user/someone")})
	m := &fakeAuthMetrics{}
	s := startAuthServer(t, certs, nil, "viewer", m)

	code, _, err := authGet(t, s.UIAddr(), "/", &tls.Config{
		RootCAs:      certs.CAPool,
		Certificates: []tls.Certificate{anyClient},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if code != http.StatusOK {
		t.Errorf("status = %d, want 200 via default_role=viewer", code)
	}
}

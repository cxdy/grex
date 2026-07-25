package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/dennisme/grex/internal/spiffe"
)

func certWithURIs(uris ...*url.URL) *x509.Certificate {
	return &x509.Certificate{URIs: uris}
}

func mustParseURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", s, err)
	}
	return u
}

func TestIdentityAbsentWhenNotAttached(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if _, ok := Identity(r); ok {
		t.Fatal("Identity: got ok=true on request with no attached identity")
	}
}

func TestIdentityPresentAfterAttach(t *testing.T) {
	want := RequestIdentity{ID: spiffe.ID{TrustDomain: "grex-api.internal", Path: "/user/alice"}, Role: "viewer"}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r = r.WithContext(context.WithValue(r.Context(), requestIdentityKey{}, want))

	got, ok := Identity(r)
	if !ok {
		t.Fatal("Identity: got ok=false, want true")
	}
	if got != want {
		t.Errorf("Identity = %+v, want %+v", got, want)
	}
}

func TestRequireSPIFFERoleNilMetricsAllowsAndDenies(t *testing.T) {
	rules := []spiffe.RoleRule{
		{Match: "exact", SpiffeID: "spiffe://grex-api.internal/user/alice", Role: "viewer"},
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := Identity(r)
		if !ok || id.Role != "viewer" {
			t.Errorf("handler saw identity %+v ok=%v, want role=viewer", id, ok)
		}
		w.WriteHeader(http.StatusOK)
	})
	h := requireSPIFFERole(rules, "none", nil, testLogger(), next)

	allowed := certWithURIs(mustSpiffeURL(t, "spiffe://grex-api.internal/user/alice"))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{allowed}}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("allowed cert: status=%d, want 200", w.Code)
	}

	denied := certWithURIs(mustSpiffeURL(t, "spiffe://grex-api.internal/user/mallory"))
	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{denied}}
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)
	if w2.Code != http.StatusForbidden {
		t.Fatalf("unmapped cert: status=%d, want 403", w2.Code)
	}
}

func TestSpiffeDenyReasonCoversAllSentinelErrors(t *testing.T) {
	m := &fakeAuthMetrics{}
	h := requireSPIFFERole(nil, "none", m, testLogger(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	cases := []struct {
		name string
		cert *x509.Certificate
		want string
	}{
		{"no_uri_san", certWithURIs(), "no_uri_san"},
		{"multiple_uri_sans", certWithURIs(mustSpiffeURL(t, "spiffe://a/b"), mustSpiffeURL(t, "spiffe://c/d")), "multiple_uri_sans"},
		{"bad_scheme", certWithURIs(mustParseURL(t, "https://grex-api.internal/user/alice")), "bad_scheme"},
		{"malformed", certWithURIs(mustParseURL(t, "spiffe:///no-host")), "malformed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{tc.cert}}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status=%d, want 403", w.Code)
			}
			m.mu.Lock()
			last := m.denied[len(m.denied)-1]
			m.mu.Unlock()
			if last != tc.want {
				t.Errorf("denied reason = %q, want %q", last, tc.want)
			}
		})
	}
}

package spiffe

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net/url"
	"testing"
	"time"
)

func selfSignedCert(t *testing.T, uris []*url.URL) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		URIs:         uris,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestFromCertValid(t *testing.T) {
	cert := selfSignedCert(t, []*url.URL{mustURL(t, "spiffe://grex.internal/user/alice")})
	id, err := FromCert(cert)
	if err != nil {
		t.Fatalf("FromCert: %v", err)
	}
	if id.TrustDomain != "grex.internal" {
		t.Errorf("TrustDomain = %q", id.TrustDomain)
	}
	if id.Path != "/user/alice" {
		t.Errorf("Path = %q", id.Path)
	}
	if id.String() != "spiffe://grex.internal/user/alice" {
		t.Errorf("String() = %q", id.String())
	}
}

func TestFromCertNoURISAN(t *testing.T) {
	cert := selfSignedCert(t, nil)
	_, err := FromCert(cert)
	if !errors.Is(err, ErrNoURISAN) {
		t.Errorf("err = %v, want ErrNoURISAN", err)
	}
}

func TestFromCertMultipleURISANs(t *testing.T) {
	cert := selfSignedCert(t, []*url.URL{
		mustURL(t, "spiffe://grex.internal/user/alice"),
		mustURL(t, "spiffe://grex.internal/service/ci"),
	})
	_, err := FromCert(cert)
	if !errors.Is(err, ErrMultipleURISANs) {
		t.Errorf("err = %v, want ErrMultipleURISANs", err)
	}
}

func TestFromCertWrongScheme(t *testing.T) {
	cert := selfSignedCert(t, []*url.URL{mustURL(t, "https://grex.internal/user/alice")})
	_, err := FromCert(cert)
	if !errors.Is(err, ErrWrongScheme) {
		t.Errorf("err = %v, want ErrWrongScheme", err)
	}
}

func TestFromCertMalformed(t *testing.T) {
	// A spiffe:// URI with no host (trust domain) is malformed.
	cert := selfSignedCert(t, []*url.URL{mustURL(t, "spiffe:///user/alice")})
	_, err := FromCert(cert)
	if !errors.Is(err, ErrMalformed) {
		t.Errorf("err = %v, want ErrMalformed", err)
	}
}

func TestFromCertEmptyPath(t *testing.T) {
	cert := selfSignedCert(t, []*url.URL{mustURL(t, "spiffe://grex.internal")})
	_, err := FromCert(cert)
	if !errors.Is(err, ErrMalformed) {
		t.Errorf("err = %v, want ErrMalformed for empty path", err)
	}
}

func TestResolveRoleExactMatch(t *testing.T) {
	mappings := []RoleRule{
		{Match: "exact", SpiffeID: "spiffe://grex.internal/user/alice", Role: "admin"},
		{Match: "prefix", SpiffeID: "spiffe://grex.internal/user/", Role: "viewer"},
	}
	id := ID{TrustDomain: "grex.internal", Path: "/user/alice"}
	role := ResolveRole(id, mappings, "none")
	if role != "admin" {
		t.Errorf("role = %q, want admin (exact beats prefix)", role)
	}
}

func TestResolveRolePrefixMatch(t *testing.T) {
	mappings := []RoleRule{
		{Match: "prefix", SpiffeID: "spiffe://grex.internal/service/", Role: "viewer"},
	}
	id := ID{TrustDomain: "grex.internal", Path: "/service/prometheus"}
	role := ResolveRole(id, mappings, "none")
	if role != "viewer" {
		t.Errorf("role = %q, want viewer", role)
	}
}

func TestResolveRoleDefault(t *testing.T) {
	id := ID{TrustDomain: "grex.internal", Path: "/user/bob"}
	role := ResolveRole(id, nil, "none")
	if role != "none" {
		t.Errorf("role = %q, want default none", role)
	}
}

func TestResolveRoleFirstMatchWinsAtSameSpecificity(t *testing.T) {
	mappings := []RoleRule{
		{Match: "prefix", SpiffeID: "spiffe://grex.internal/user/", Role: "viewer"},
		{Match: "prefix", SpiffeID: "spiffe://grex.internal/user/", Role: "admin"},
	}
	id := ID{TrustDomain: "grex.internal", Path: "/user/alice"}
	role := ResolveRole(id, mappings, "none")
	if role != "viewer" {
		t.Errorf("role = %q, want viewer (first prefix match)", role)
	}
}

func TestResolveRoleNoMatchFallsToDefault(t *testing.T) {
	mappings := []RoleRule{
		{Match: "exact", SpiffeID: "spiffe://grex.internal/user/alice", Role: "admin"},
	}
	id := ID{TrustDomain: "grex.internal", Path: "/user/bob"}
	role := ResolveRole(id, mappings, "none")
	if role != "none" {
		t.Errorf("role = %q, want none", role)
	}
}

// Package spiffe resolves a caller's identity and role from a SPIFFE ID
// carried as a URI SAN on their mTLS client certificate. Two namespaces are
// used across grex: spiffe://<trust-domain>/user/<name> for humans and
// spiffe://<trust-domain>/service/<name> for automation (Prometheus, CI,
// scripts).
package spiffe

import (
	"crypto/x509"
	"errors"
	"fmt"
)

var (
	// ErrNoURISAN is returned when the certificate carries no URI SAN.
	ErrNoURISAN = errors.New("spiffe: certificate has no URI SAN")
	// ErrMultipleURISANs is returned when the certificate carries more than
	// one URI SAN; grex requires exactly one so identity is unambiguous.
	ErrMultipleURISANs = errors.New("spiffe: certificate has more than one URI SAN")
	// ErrWrongScheme is returned when the URI SAN is not a spiffe:// URI.
	ErrWrongScheme = errors.New("spiffe: URI SAN is not a spiffe:// URI")
	// ErrMalformed is returned when the spiffe:// URI has no trust domain or
	// no path.
	ErrMalformed = errors.New("spiffe: malformed SPIFFE ID")
)

// ID is a parsed SPIFFE ID: spiffe://<TrustDomain><Path>.
type ID struct {
	TrustDomain string
	Path        string
}

// String renders the ID back to its spiffe:// URI form.
func (id ID) String() string {
	return "spiffe://" + id.TrustDomain + id.Path
}

// FromCert extracts and validates the SPIFFE ID from a client certificate's
// URI SANs. The certificate must carry exactly one URI SAN, and it must be a
// well-formed spiffe:// URI with both a trust domain and a path.
func FromCert(cert *x509.Certificate) (ID, error) {
	switch len(cert.URIs) {
	case 0:
		return ID{}, ErrNoURISAN
	case 1:
		// fall through
	default:
		return ID{}, ErrMultipleURISANs
	}
	u := cert.URIs[0]
	if u.Scheme != "spiffe" {
		return ID{}, fmt.Errorf("%w: %q", ErrWrongScheme, u.Scheme)
	}
	if u.Host == "" || u.Path == "" {
		return ID{}, fmt.Errorf("%w: %q", ErrMalformed, u.String())
	}
	return ID{TrustDomain: u.Host, Path: u.Path}, nil
}

// RoleRule maps one SPIFFE ID (exact or prefix match) to a role. Tagged for
// YAML so internal/config can decode it directly without a duplicate type.
type RoleRule struct {
	// Match is "exact" or "prefix".
	Match string `yaml:"match"`
	// SpiffeID is the full spiffe:// URI to match against (exact), or the
	// spiffe:// URI prefix to match against (prefix).
	SpiffeID string `yaml:"spiffe_id"`
	Role     string `yaml:"role"`
}

// ResolveRole returns the role for id per the ordered rule list: exact
// matches win over prefix matches regardless of order, and the first match
// wins among rules of the same specificity. defaultRole is returned when
// nothing matches.
func ResolveRole(id ID, rules []RoleRule, defaultRole string) string {
	full := id.String()
	for _, r := range rules {
		if r.Match == "exact" && r.SpiffeID == full {
			return r.Role
		}
	}
	for _, r := range rules {
		if r.Match == "prefix" && len(r.SpiffeID) <= len(full) && full[:len(r.SpiffeID)] == r.SpiffeID {
			return r.Role
		}
	}
	return defaultRole
}

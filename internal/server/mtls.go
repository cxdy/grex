package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/dennisme/grex/internal/spiffe"
)

// AuthMetrics records mTLS authorization outcomes on the UI and telemetry
// listeners.
type AuthMetrics interface {
	AuthDenied(reason string)
	AuthAllowed(role string)
}

type noopAuthMetrics struct{}

func (noopAuthMetrics) AuthDenied(string)  {}
func (noopAuthMetrics) AuthAllowed(string) {}

// RequestIdentity is the caller identity resolved from an mTLS client
// certificate, attached to the request context by requireSPIFFERole.
type RequestIdentity struct {
	ID   spiffe.ID
	Role string
}

type requestIdentityKey struct{}

// Identity returns the caller identity attached by requireSPIFFERole, if the
// listener has mTLS authorization enabled.
func Identity(r *http.Request) (RequestIdentity, bool) {
	id, ok := r.Context().Value(requestIdentityKey{}).(RequestIdentity)
	return id, ok
}

// requireSPIFFERole wraps next with SPIFFE-identity authorization: it
// extracts the peer certificate's SPIFFE ID, resolves a role from rules, and
// denies with 403 when there is no certificate, the SPIFFE ID is invalid, or
// the resolved role is empty/"none". On success it attaches a
// RequestIdentity to the request context before calling next.
func requireSPIFFERole(rules []spiffe.RoleRule, defaultRole string, m AuthMetrics, logger *slog.Logger, next http.Handler) http.Handler {
	if m == nil {
		m = noopAuthMetrics{}
	}
	deny := func(w http.ResponseWriter, r *http.Request, reason string) {
		m.AuthDenied(reason)
		logger.Warn("mTLS request denied", "reason", reason, "path", r.URL.Path)
		http.Error(w, "forbidden", http.StatusForbidden)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			deny(w, r, "no_cert")
			return
		}
		id, err := spiffe.FromCert(r.TLS.PeerCertificates[0])
		if err != nil {
			deny(w, r, spiffeDenyReason(err))
			return
		}
		role := spiffe.ResolveRole(id, rules, defaultRole)
		if role == "" || role == "none" {
			deny(w, r, "no_role")
			return
		}
		m.AuthAllowed(role)
		ctx := context.WithValue(r.Context(), requestIdentityKey{}, RequestIdentity{ID: id, Role: role})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func spiffeDenyReason(err error) string {
	switch {
	case errors.Is(err, spiffe.ErrNoURISAN):
		return "no_uri_san"
	case errors.Is(err, spiffe.ErrMultipleURISANs):
		return "multiple_uri_sans"
	case errors.Is(err, spiffe.ErrWrongScheme):
		return "bad_scheme"
	case errors.Is(err, spiffe.ErrMalformed):
		return "malformed"
	default:
		return "invalid"
	}
}

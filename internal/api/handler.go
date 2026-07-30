// Package api serves the JSON read API over fleet state.
package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"runtime"
	"slices"
	"strconv"
	"time"

	"github.com/dennisme/grex/internal/buildinfo"
	"github.com/dennisme/grex/internal/fleet"
	"github.com/dennisme/grex/internal/persistence"
)

const (
	defaultLimit = 100
	maxLimit     = 1000
)

// Metrics is the subset of metrics.Events this package records against.
type Metrics interface {
	// ListStoreFallbackFailed counts one GET /api/agents request whose
	// database merge failed. This package always calls it with surface
	// "api".
	ListStoreFallbackFailed(surface string)
}

type noopMetrics struct{}

func (noopMetrics) ListStoreFallbackFailed(string) {}

// Handler serves the read API: list/get agents and server status.
type Handler struct {
	registry  *fleet.Registry
	startedAt time.Time
	store     persistence.StateStore
	metrics   Metrics
}

// New builds a Handler over the given registry. startedAt is used for uptime
// in GET /api/status; pass time.Now() at process start. store is optional
// (nil when database.host is unset, the same opt-in pattern persistence's
// Flusher and purge job already use): when set, GET /api/agents/{id} falls
// back to it for an agent fleet.Registry doesn't hold locally — an agent
// live on a sibling grex replica, already flushed to the database. GET
// /api/agents merges the same way (MergeAgents): local registry plus
// whatever the database has for agents this replica doesn't hold, local
// winning on overlap. Either fallback reflects that replica's last flush,
// not live state: connected and other session fields can be a few seconds
// stale, same tolerance already accepted for the write path (see
// docs/developer/persistence.md). Registry stays the fast path: store is
// never consulted on a hit, and a list-merge store error degrades to a
// registry-only result (logged and counted via metrics, not failed) rather
// than losing known-good local data over the database being unavailable —
// the response's "partial" field reflects that degraded state. metrics is
// optional (nil records nothing).
func New(registry *fleet.Registry, startedAt time.Time, store persistence.StateStore, metrics Metrics) *Handler {
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	if metrics == nil {
		metrics = noopMetrics{}
	}
	return &Handler{registry: registry, startedAt: startedAt, store: store, metrics: metrics}
}

// Mount registers API routes on mux. Paths use Go 1.22+ method patterns.
// If wrap is non-nil, each handler is passed through wrap(route, handler)
// before registration (e.g. for Prometheus HTTP metrics).
func (h *Handler) Mount(mux *http.ServeMux, wrap func(route string, next http.Handler) http.Handler) {
	if wrap == nil {
		wrap = func(_ string, next http.Handler) http.Handler { return next }
	}
	mux.Handle("GET /api/agents", wrap("/api/agents", http.HandlerFunc(h.listAgents)))
	mux.Handle("GET /api/agents/{id}", wrap("/api/agents/{id}", http.HandlerFunc(h.getAgent)))
	mux.Handle("GET /api/status", wrap("/api/status", http.HandlerFunc(h.status)))
	mux.Handle("GET /api/attributes", wrap("/api/attributes", http.HandlerFunc(h.listAttributeKeys)))
	mux.Handle("GET /api/attributes/values", wrap("/api/attributes/values", http.HandlerFunc(h.listAttributeValues)))
}

type listResponse struct {
	Agents []fleet.AgentView `json:"agents"`
	Total  int               `json:"total"`
	Limit  int               `json:"limit"`
	Offset int               `json:"offset"`
	// Partial is true when a configured store's ListAgents call failed and
	// this response reflects the local registry only — agents live on a
	// sibling replica but not this one may be missing. Always false when no
	// store is configured (nothing to merge) or the merge succeeded.
	Partial bool `json:"partial"`
}

func (h *Handler) listAgents(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit, offset, err := parsePagination(query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	filters, err := ParseFilters(query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	localAgents := h.registry.List()
	mergedAgents := localAgents
	var partial bool
	if h.store != nil {
		dbAgents, err := h.store.ListAgents(r.Context())
		if err != nil {
			slog.Error("api: list store fallback failed", "error", err)
			h.metrics.ListStoreFallbackFailed("api")
			partial = true
		} else {
			mergedAgents = MergeAgents(localAgents, dbAgents, time.Now(), h.registry.HeartbeatInterval())
		}
	}

	agents := MatchingAgents(mergedAgents, filters)
	slices.SortFunc(agents, func(a, b fleet.Agent) int {
		if a.InstanceUID < b.InstanceUID {
			return -1
		}
		if a.InstanceUID > b.InstanceUID {
			return 1
		}
		return 0
	})

	total := len(agents)
	start := min(offset, total)
	end := min(start+limit, total)
	page := agents[start:end]
	views := make([]fleet.AgentView, len(page))
	for i, a := range page {
		views[i] = fleet.SummaryView(a)
	}

	writeJSON(w, listResponse{
		Agents:  views,
		Total:   total,
		Limit:   limit,
		Offset:  offset,
		Partial: partial,
	})
}

func (h *Handler) getAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing agent id", http.StatusBadRequest)
		return
	}
	agent, ok := h.registry.Get(id)
	if !ok && h.store != nil {
		var err error
		agent, ok, err = h.store.GetAgent(r.Context(), id)
		if err != nil {
			http.Error(w, "lookup failed", http.StatusInternalServerError)
			return
		}
		if ok && agent.EvictedAt != nil {
			ok = false
		}
		if ok && StaleConnected(agent, time.Now(), h.registry.HeartbeatInterval()) {
			agent.Connected = false
		}
	}
	if !ok {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	writeJSON(w, fleet.DetailView(agent))
}

type statusResponse struct {
	Version       string     `json:"version"`
	Commit        string     `json:"commit"`
	GoVersion     string     `json:"go_version"`
	StartedAt     time.Time  `json:"started_at"`
	UptimeSeconds int64      `json:"uptime_seconds"`
	Fleet         fleetStats `json:"fleet"`
}

type fleetStats struct {
	Total             int `json:"total"`
	Connected         int `json:"connected"`
	Disconnected      int `json:"disconnected"`
	Healthy           int `json:"healthy"`
	Unhealthy         int `json:"unhealthy"`
	HealthUnknown     int `json:"health_unknown"`
	AwaitingFullState int `json:"awaiting_full_state"`
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	agents := h.registry.List()
	var stats fleetStats
	stats.Total = len(agents)
	for _, a := range agents {
		if a.Connected {
			stats.Connected++
		} else {
			stats.Disconnected++
		}
		if !a.DescriptionReported {
			stats.AwaitingFullState++
		}
		switch {
		case !a.HealthReported:
			stats.HealthUnknown++
		case a.Healthy:
			stats.Healthy++
		default:
			stats.Unhealthy++
		}
	}
	now := time.Now()
	writeJSON(w, statusResponse{
		Version:       buildinfo.Version,
		Commit:        buildinfo.Commit,
		GoVersion:     runtime.Version(),
		StartedAt:     h.startedAt.UTC(),
		UptimeSeconds: int64(now.Sub(h.startedAt).Seconds()),
		Fleet:         stats,
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// parsePagination reads limit/offset query params. limit defaults to
// defaultLimit and is capped at maxLimit; offset defaults to 0. Both must be
// valid non-negative integers (limit must be positive) when present.
func parsePagination(q url.Values) (limit, offset int, err error) {
	limit = defaultLimit
	if v := q.Get("limit"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n <= 0 {
			return 0, 0, fmt.Errorf("limit must be a positive integer")
		}
		limit = min(n, maxLimit)
	}
	if v := q.Get("offset"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n < 0 {
			return 0, 0, fmt.Errorf("offset must be a non-negative integer")
		}
		offset = n
	}
	return limit, offset, nil
}

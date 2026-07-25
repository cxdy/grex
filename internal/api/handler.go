// Package api serves the JSON read API over fleet state.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"github.com/dennisme/grex/internal/fleet"
)

const (
	defaultLimit = 100
	maxLimit     = 1000
)

// Handler serves GET /api/agents: a paginated list of every agent currently
// in the fleet registry, with every attribute the registry holds.
type Handler struct {
	registry *fleet.Registry
}

// New builds a Handler over the given registry.
func New(registry *fleet.Registry) *Handler {
	return &Handler{registry: registry}
}

type listResponse struct {
	Agents []fleet.Agent `json:"agents"`
	Total  int           `json:"total"`
	Limit  int           `json:"limit"`
	Offset int           `json:"offset"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	limit, offset, err := parsePagination(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	agents := h.registry.List()
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

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(listResponse{
		Agents: agents[start:end],
		Total:  total,
		Limit:  limit,
		Offset: offset,
	})
}

// parsePagination reads limit/offset query params. limit defaults to
// defaultLimit and is capped at maxLimit; offset defaults to 0. Both must be
// valid non-negative integers (limit must be positive) when present.
func parsePagination(q map[string][]string) (limit, offset int, err error) {
	limit = defaultLimit
	if v := firstValue(q, "limit"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n <= 0 {
			return 0, 0, fmt.Errorf("limit must be a positive integer")
		}
		limit = min(n, maxLimit)
	}
	if v := firstValue(q, "offset"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n < 0 {
			return 0, 0, fmt.Errorf("offset must be a non-negative integer")
		}
		offset = n
	}
	return limit, offset, nil
}

func firstValue(q map[string][]string, key string) string {
	if vs := q[key]; len(vs) > 0 {
		return vs[0]
	}
	return ""
}

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

	query := r.URL.Query()
	limit, offset, err := parsePagination(query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	filters, err := parseFilters(query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	agents := matchingAgents(h.registry.List(), filters)
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

// reservedParams are pagination controls, never treated as filters even if
// an agent happens to report an attribute with the same key.
var reservedParams = map[string]bool{"limit": true, "offset": true}

// boolFields are well-known top-level Agent fields filterable as
// ?key=true|false. These take precedence over AgentDescription attribute
// filtering for the same key: an agent-reported attribute literally named
// "healthy" (unusual, but attribute keys are arbitrary) cannot be filtered
// on since the top-level field always wins. The key set here must match
// fleet.ReservedAttributeKeys exactly (see handler_test.go); that list is
// what the registry uses to warn and count when an agent's own attributes
// collide with these names.
var boolFields = map[string]func(fleet.Agent) bool{
	"healthy":     func(a fleet.Agent) bool { return a.Healthy },
	"connected":   func(a fleet.Agent) bool { return a.Connected },
	"via_gateway": func(a fleet.Agent) bool { return a.Conn.ViaGateway },
}

// filters holds parsed query filters: exact-match AgentDescription
// attributes, and well-known top-level boolean fields.
type filters struct {
	attrs map[string]string
	bools map[string]bool
}

func (f filters) empty() bool { return len(f.attrs) == 0 && len(f.bools) == 0 }

// parseFilters turns every non-reserved query param into a filter. A
// boolFields key must parse as true/false; anything else is an exact-match
// AgentDescription attribute filter. Multiple params are ANDed; a repeated
// key uses only its first value.
func parseFilters(q map[string][]string) (filters, error) {
	f := filters{attrs: make(map[string]string), bools: make(map[string]bool)}
	for key, values := range q {
		if reservedParams[key] || len(values) == 0 {
			continue
		}
		if _, ok := boolFields[key]; ok {
			b, err := strconv.ParseBool(values[0])
			if err != nil {
				return filters{}, fmt.Errorf("%s must be a boolean (true or false)", key)
			}
			f.bools[key] = b
			continue
		}
		f.attrs[key] = values[0]
	}
	return f, nil
}

// matchingAgents returns agents satisfying every filter.
func matchingAgents(agents []fleet.Agent, f filters) []fleet.Agent {
	if f.empty() {
		return agents
	}
	matched := agents[:0]
	for _, agent := range agents {
		if agentMatches(agent, f) {
			matched = append(matched, agent)
		}
	}
	return matched
}

func agentMatches(agent fleet.Agent, f filters) bool {
	for key, want := range f.attrs {
		got, ok := agent.Identifying[key]
		if !ok {
			got, ok = agent.NonIdentifying[key]
		}
		if !ok || got != want {
			return false
		}
	}
	for key, want := range f.bools {
		if boolFields[key](agent) != want {
			return false
		}
	}
	return true
}

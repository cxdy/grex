package api

import (
	"net/http"
	"slices"
	"strings"

	"github.com/dennisme/grex/internal/fleet"
)

// AttributeKeys returns sorted unique AgentDescription attribute keys
// across the fleet (identifying and non-identifying).
func AttributeKeys(agents []fleet.Agent) []string {
	seen := make(map[string]struct{})
	for _, a := range agents {
		for k := range a.Identifying {
			seen[k] = struct{}{}
		}
		for k := range a.NonIdentifying {
			seen[k] = struct{}{}
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// AttributeValues returns sorted unique values for key across the fleet.
func AttributeValues(agents []fleet.Agent, key string) []string {
	if key == "" {
		return nil
	}
	seen := make(map[string]struct{})
	for _, a := range agents {
		if v, ok := a.Identifying[key]; ok {
			seen[v] = struct{}{}
		}
		if v, ok := a.NonIdentifying[key]; ok {
			seen[v] = struct{}{}
		}
	}
	vals := make([]string, 0, len(seen))
	for v := range seen {
		vals = append(vals, v)
	}
	slices.Sort(vals)
	return vals
}

func (h *Handler) listAttributeKeys(w http.ResponseWriter, r *http.Request) {
	keys := AttributeKeys(h.registry.List())
	// Optional prefix filter for autocomplete
	if p := strings.TrimSpace(r.URL.Query().Get("prefix")); p != "" {
		p = strings.ToLower(p)
		filtered := keys[:0]
		for _, k := range keys {
			if strings.Contains(strings.ToLower(k), p) {
				filtered = append(filtered, k)
			}
		}
		keys = filtered
	}
	writeJSON(w, map[string]any{"keys": keys})
}

func (h *Handler) listAttributeValues(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	if key == "" {
		http.Error(w, "key query param is required", http.StatusBadRequest)
		return
	}
	vals := AttributeValues(h.registry.List(), key)
	if p := strings.TrimSpace(r.URL.Query().Get("prefix")); p != "" {
		p = strings.ToLower(p)
		filtered := vals[:0]
		for _, v := range vals {
			if strings.Contains(strings.ToLower(v), p) {
				filtered = append(filtered, v)
			}
		}
		vals = filtered
	}
	writeJSON(w, map[string]any{"key": key, "values": vals})
}

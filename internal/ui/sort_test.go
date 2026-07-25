package ui

import (
	"net/url"
	"testing"
	"time"

	"github.com/dennisme/grex/internal/fleet"
)

func TestParseSortDefaults(t *testing.T) {
	t.Parallel()
	sortKey, order := parseSort(url.Values{})
	if sortKey != "instance" || order != "asc" {
		t.Fatalf("got %s %s, want instance asc", sortKey, order)
	}
}

func TestParseSortToggleFields(t *testing.T) {
	t.Parallel()
	q := url.Values{"sort": {"name"}, "order": {"desc"}}
	sortKey, order := parseSort(q)
	if sortKey != "name" || order != "desc" {
		t.Fatalf("got %s %s", sortKey, order)
	}
	// invalid column falls back
	q = url.Values{"sort": {"attributes"}, "order": {"asc"}}
	sortKey, order = parseSort(q)
	if sortKey != "instance" || order != "asc" {
		t.Fatalf("invalid sort column: got %s %s, want instance asc", sortKey, order)
	}
}

func TestSortAgentsByName(t *testing.T) {
	t.Parallel()
	agents := []fleet.Agent{
		{InstanceUID: "b", Identifying: map[string]string{"service.name": "zeta"}},
		{InstanceUID: "a", Identifying: map[string]string{"service.name": "alpha"}},
	}
	sortAgents(agents, "name", "asc")
	if agents[0].InstanceUID != "a" || fleet.DisplayNameOf(agents[0]) != "alpha" {
		t.Fatalf("asc name: first = %s %s", agents[0].InstanceUID, fleet.DisplayNameOf(agents[0]))
	}
	sortAgents(agents, "name", "desc")
	if fleet.DisplayNameOf(agents[0]) != "zeta" {
		t.Fatalf("desc name: first = %s", fleet.DisplayNameOf(agents[0]))
	}
}

func TestSortAgentsByLastSeen(t *testing.T) {
	t.Parallel()
	t1 := time.Unix(100, 0).UTC()
	t2 := time.Unix(200, 0).UTC()
	agents := []fleet.Agent{
		{InstanceUID: "old", LastSeen: t1},
		{InstanceUID: "new", LastSeen: t2},
	}
	sortAgents(agents, "last_seen", "desc")
	if agents[0].InstanceUID != "new" {
		t.Fatalf("desc last_seen: first = %s", agents[0].InstanceUID)
	}
}

func TestSortAgentsByStatus(t *testing.T) {
	t.Parallel()
	agents := []fleet.Agent{
		{InstanceUID: "d", Connected: false, HealthReported: true, Healthy: true},
		{InstanceUID: "h", Connected: true, HealthReported: true, Healthy: true},
		{InstanceUID: "u", Connected: true, HealthReported: true, Healthy: false},
	}
	sortAgents(agents, "status", "asc")
	if agents[0].InstanceUID != "h" {
		t.Fatalf("status asc first = %s, want healthy connected", agents[0].InstanceUID)
	}
	if agents[2].InstanceUID != "d" {
		t.Fatalf("status asc last = %s, want disconnected", agents[2].InstanceUID)
	}
}

func TestSortHrefTogglesOrder(t *testing.T) {
	t.Parallel()
	q := url.Values{"sort": {"name"}, "order": {"asc"}, "offset": {"10"}}
	href := sortHref(q, "name", "name", "asc")
	if href != "/?order=desc&sort=name" && href != "/?sort=name&order=desc" {
		// order of query keys may vary
		u, err := url.Parse(href)
		if err != nil {
			t.Fatal(err)
		}
		if u.Query().Get("sort") != "name" || u.Query().Get("order") != "desc" {
			t.Fatalf("href = %s", href)
		}
		if u.Query().Get("offset") != "" {
			t.Error("sort should reset offset")
		}
	}
}

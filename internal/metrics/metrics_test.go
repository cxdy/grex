package metrics

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/dennisme/grex/internal/fleet"
)

func strAttr(key, value string) *protobufs.KeyValue {
	return &protobufs.KeyValue{
		Key:   key,
		Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: value}},
	}
}

func report(r *fleet.Registry, uid uuid.UUID, meta fleet.ConnMeta, healthy bool) {
	r.Report(&protobufs.AgentToServer{
		InstanceUid: uid[:],
		AgentDescription: &protobufs.AgentDescription{
			NonIdentifyingAttributes: []*protobufs.KeyValue{
				strAttr("deployment.environment", "dev"),
			},
		},
		Health: &protobufs.ComponentHealth{Healthy: healthy},
	}, meta)
}

func newFleet(required ...string) *fleet.Registry {
	return fleet.New(fleet.Config{
		HeartbeatInterval:     30 * time.Second,
		StaleMissedHeartbeats: 3,
		RequiredAttributes:    required,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
}

func TestNewRegistryHasRuntimeCollectors(t *testing.T) {
	reg := NewRegistry()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, fam := range families {
		if fam.GetName() == "go_goroutines" {
			return
		}
	}
	t.Error("go_goroutines not registered")
}

func TestEventCounterPlacement(t *testing.T) {
	serverReg := prometheus.NewRegistry()
	fleetReg := prometheus.NewRegistry()
	events := NewEvents(serverReg, fleetReg)
	events.Message()
	events.AgentConnected("agent-1")

	names := func(reg *prometheus.Registry) map[string]bool {
		t.Helper()
		families, err := reg.Gather()
		if err != nil {
			t.Fatal(err)
		}
		out := make(map[string]bool)
		for _, fam := range families {
			out[fam.GetName()] = true
		}
		return out
	}

	server := names(serverReg)
	fleet := names(fleetReg)
	if !server["grex_opamp_messages_total"] || server["grex_agent_connects_total"] {
		t.Errorf("server registry families wrong: %v", server)
	}
	if !fleet["grex_agent_connects_total"] || fleet["grex_opamp_messages_total"] {
		t.Errorf("fleet registry families wrong: %v", fleet)
	}
}

func TestEventCounters(t *testing.T) {
	reg := prometheus.NewRegistry()
	events := NewEvents(reg, reg)

	events.AgentConnected("agent-1")
	events.AgentConnected("agent-2")
	events.AgentDisconnected("agent-1")
	events.AgentEvicted("agent-1")
	events.ReportReceived("agent-1", "status")
	events.ReportReceived("agent-2", "status")
	events.ReportReceived("agent-1", "health")
	events.MissingAttribute("agent-1", "team")
	events.ReservedAttributeConflict("agent-1", "healthy")
	events.Message()
	events.MessageError()
	events.GatewayConnect(true)
	events.GatewayConnect(false)
	events.GatewayConnectionOpened()
	events.GatewayConnectionOpened()
	events.GatewayConnectionClosed()
	events.AuthDenied("no_cert")
	events.AuthDenied("no_cert")
	events.AuthAllowed("viewer")

	assert := func(name string, want float64, c prometheus.Collector) {
		t.Helper()
		if got := testutil.ToFloat64(c); got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	assert("agent_connects_total", 2, events.agentConnects)
	assert("agent_disconnects_total", 1, events.agentDisconnects)
	assert("agents_evicted_total", 1, events.agentsEvicted)
	assert("reports status", 2, events.reports.WithLabelValues("status"))
	assert("reports health", 1, events.reports.WithLabelValues("health"))
	assert("missing attributes team", 1, events.missingAttributes.WithLabelValues("team"))
	assert("reserved attribute conflicts healthy", 1, events.reservedAttributeConflicts.WithLabelValues("healthy"))
	assert("opamp_messages_total", 1, events.messages)
	assert("opamp_message_errors_total", 1, events.messageErrors)
	assert("gateway connects accepted", 1, events.gatewayConnects.WithLabelValues("accepted"))
	assert("gateway connects rejected", 1, events.gatewayConnects.WithLabelValues("rejected"))
	assert("gateway_connections", 1, events.gatewayConnections)
	assert("auth denied no_cert", 2, events.authDenied.WithLabelValues("no_cert"))
	assert("auth allowed viewer", 1, events.authAllowed.WithLabelValues("viewer"))
}

func TestNewInfoGauge(t *testing.T) {
	reg := prometheus.NewRegistry()
	NewInfoGauge(reg, "grex_build_info", "Build information.", prometheus.Labels{
		"version":    "1.2.3",
		"commit":     "abc123",
		"go_version": "go1.26",
	})

	expected := `
# HELP grex_build_info Build information.
# TYPE grex_build_info gauge
grex_build_info{commit="abc123",go_version="go1.26",version="1.2.3"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(expected), "grex_build_info"); err != nil {
		t.Error(err)
	}
}

func TestFleetCollectorAggregates(t *testing.T) {
	registry := newFleet("team")
	direct := uuid.New()
	relayed := uuid.New()
	gone := uuid.New()

	report(registry, direct, fleet.ConnMeta{Transport: "ws"}, true)
	report(registry, relayed, fleet.ConnMeta{Transport: "ws", ViaGateway: true}, true)
	report(registry, gone, fleet.ConnMeta{Transport: "http"}, false)
	registry.SetConnected(gone.String(), false)

	collector := NewFleetCollector(registry, 1000)
	expected := `
# HELP grex_agents_connected Agents currently connected.
# TYPE grex_agents_connected gauge
grex_agents_connected{transport="ws",via="direct"} 1
grex_agents_connected{transport="ws",via="gateway"} 1
# HELP grex_agents_disconnected Agents retained but not currently connected.
# TYPE grex_agents_disconnected gauge
grex_agents_disconnected 1
# HELP grex_agents_noncompliant Agents missing at least one required attribute.
# TYPE grex_agents_noncompliant gauge
grex_agents_noncompliant 3
`
	err := testutil.CollectAndCompare(collector, strings.NewReader(expected),
		"grex_agents_connected", "grex_agents_disconnected", "grex_agents_noncompliant")
	if err != nil {
		t.Error(err)
	}
}

func TestFleetCollectorPerAgentSeries(t *testing.T) {
	registry := newFleet()
	uid := uuid.New()
	report(registry, uid, fleet.ConnMeta{Transport: "ws"}, true)

	collector := NewFleetCollector(registry, 1000)
	expected := `
# HELP grex_agent_health Agent health as reported: 1 healthy, 0 unhealthy.
# TYPE grex_agent_health gauge
grex_agent_health{instance_uid="` + uid.String() + `"} 1
`
	if err := testutil.CollectAndCompare(collector, strings.NewReader(expected), "grex_agent_health"); err != nil {
		t.Error(err)
	}
	if got := testutil.CollectAndCount(collector, "grex_agent_last_seen_timestamp_seconds"); got != 1 {
		t.Errorf("last_seen series = %d, want 1", got)
	}
}

// Agents that have not yet reported health or description, e.g. right after
// a grex restart behind a gateway, must not read as unhealthy.
func TestFleetCollectorOmitsUnreportedState(t *testing.T) {
	registry := newFleet()
	uid := uuid.New()
	// Bare heartbeat: registered, nothing reported yet.
	registry.Report(&protobufs.AgentToServer{InstanceUid: uid[:]}, fleet.ConnMeta{Transport: "ws"})

	collector := NewFleetCollector(registry, 1000)
	if got := testutil.CollectAndCount(collector, "grex_agent_health"); got != 0 {
		t.Errorf("agent_health series for unreported agent = %d, want 0", got)
	}
	expected := `
# HELP grex_agents_awaiting_full_state Agents that have not yet reported their description.
# TYPE grex_agents_awaiting_full_state gauge
grex_agents_awaiting_full_state 1
`
	if err := testutil.CollectAndCompare(collector, strings.NewReader(expected), "grex_agents_awaiting_full_state"); err != nil {
		t.Error(err)
	}

	// Full state arrives: health appears, awaiting drops to zero.
	report(registry, uid, fleet.ConnMeta{Transport: "ws"}, true)
	if got := testutil.CollectAndCount(collector, "grex_agent_health"); got != 1 {
		t.Errorf("agent_health series after report = %d, want 1", got)
	}
	expected = `
# HELP grex_agents_awaiting_full_state Agents that have not yet reported their description.
# TYPE grex_agents_awaiting_full_state gauge
grex_agents_awaiting_full_state 0
`
	if err := testutil.CollectAndCompare(collector, strings.NewReader(expected), "grex_agents_awaiting_full_state"); err != nil {
		t.Error(err)
	}
}

func TestFleetCollectorCapDropsPerAgentSeries(t *testing.T) {
	registry := newFleet()
	report(registry, uuid.New(), fleet.ConnMeta{Transport: "ws"}, true)
	report(registry, uuid.New(), fleet.ConnMeta{Transport: "ws"}, true)

	collector := NewFleetCollector(registry, 1)
	if got := testutil.CollectAndCount(collector, "grex_agent_health"); got != 0 {
		t.Errorf("agent_health series over cap = %d, want 0", got)
	}
	if got := testutil.CollectAndCount(collector, "grex_agents_connected"); got == 0 {
		t.Error("aggregate series missing when over cap")
	}

	// The cap firing must be directly observable, not just inferable from
	// per-agent series disappearing.
	expected := `
# HELP grex_fleet_size Total agents registered, regardless of the per-agent series cap.
# TYPE grex_fleet_size gauge
grex_fleet_size 2
# HELP grex_agent_series_capped Whether per-agent series are currently omitted because the fleet exceeds metrics.per_agent_series_limit.
# TYPE grex_agent_series_capped gauge
grex_agent_series_capped 1
`
	err := testutil.CollectAndCompare(collector, strings.NewReader(expected),
		"grex_fleet_size", "grex_agent_series_capped")
	if err != nil {
		t.Error(err)
	}
}

func TestFleetCollectorSizeAndCappedUnderLimit(t *testing.T) {
	registry := newFleet()
	report(registry, uuid.New(), fleet.ConnMeta{Transport: "ws"}, true)

	collector := NewFleetCollector(registry, 1000)
	expected := `
# HELP grex_fleet_size Total agents registered, regardless of the per-agent series cap.
# TYPE grex_fleet_size gauge
grex_fleet_size 1
# HELP grex_agent_series_capped Whether per-agent series are currently omitted because the fleet exceeds metrics.per_agent_series_limit.
# TYPE grex_agent_series_capped gauge
grex_agent_series_capped 0
`
	err := testutil.CollectAndCompare(collector, strings.NewReader(expected),
		"grex_fleet_size", "grex_agent_series_capped")
	if err != nil {
		t.Error(err)
	}
}

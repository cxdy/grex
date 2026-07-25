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

func newFleet(events fleet.Events, required ...string) *fleet.Registry {
	return fleet.New(fleet.Config{
		HeartbeatInterval:     30 * time.Second,
		StaleMissedHeartbeats: 3,
		RequiredAttributes:    required,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), events)
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
	events.AgentConnected()

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

	events.AgentConnected()
	events.AgentConnected()
	events.AgentDisconnected()
	events.AgentEvicted()
	events.ReportReceived("status")
	events.ReportReceived("status")
	events.ReportReceived("health")
	events.MissingAttribute("team")
	events.Message()
	events.MessageError()
	events.GatewayConnect(true)
	events.GatewayConnect(false)
	events.GatewayConnectionOpened()
	events.GatewayConnectionOpened()
	events.GatewayConnectionClosed()

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
	assert("opamp_messages_total", 1, events.messages)
	assert("opamp_message_errors_total", 1, events.messageErrors)
	assert("gateway connects accepted", 1, events.gatewayConnects.WithLabelValues("accepted"))
	assert("gateway connects rejected", 1, events.gatewayConnects.WithLabelValues("rejected"))
	assert("gateway_connections", 1, events.gatewayConnections)
}

func TestFleetCollectorAggregates(t *testing.T) {
	registry := newFleet(nil, "team")
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
	registry := newFleet(nil)
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

func TestFleetCollectorCapDropsPerAgentSeries(t *testing.T) {
	registry := newFleet(nil)
	report(registry, uuid.New(), fleet.ConnMeta{Transport: "ws"}, true)
	report(registry, uuid.New(), fleet.ConnMeta{Transport: "ws"}, true)

	collector := NewFleetCollector(registry, 1)
	if got := testutil.CollectAndCount(collector, "grex_agent_health"); got != 0 {
		t.Errorf("agent_health series over cap = %d, want 0", got)
	}
	if got := testutil.CollectAndCount(collector, "grex_agents_connected"); got == 0 {
		t.Error("aggregate series missing when over cap")
	}
}

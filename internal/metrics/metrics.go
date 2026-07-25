// Package metrics owns the Prometheus registry and exposes fleet and OpAMP
// server metrics: event counters incremented at the event site, and gauges
// derived from fleet state at scrape time.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/dennisme/grex/internal/fleet"
)

// NewRegistry builds the process-wide Prometheus registry with Go runtime and
// process collectors registered.
func NewRegistry() *prometheus.Registry {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return registry
}

// NewInfoGauge registers a static gauge set to 1 with labels as its only
// data, the standard Prometheus pattern for exposing build or config values
// (e.g. `grex_build_info{version="1.2.3"} 1`) as queryable label values
// rather than log lines.
func NewInfoGauge(reg prometheus.Registerer, name, help string, labels prometheus.Labels) prometheus.Gauge {
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        name,
		Help:        help,
		ConstLabels: labels,
	})
	gauge.Set(1)
	reg.MustRegister(gauge)
	return gauge
}

// Events holds the event-driven fleet and OpAMP counters. It implements
// fleet.Events and the opamp handler's metrics hooks.
type Events struct {
	agentConnects              prometheus.Counter
	agentDisconnects           prometheus.Counter
	agentsEvicted              prometheus.Counter
	reports                    *prometheus.CounterVec
	missingAttributes          *prometheus.CounterVec
	reservedAttributeConflicts *prometheus.CounterVec
	messages                   prometheus.Counter
	messageErrors              prometheus.Counter
	gatewayConnects            *prometheus.CounterVec
	gatewayConnections         prometheus.Gauge
	authDenied                 *prometheus.CounterVec
	authAllowed                *prometheus.CounterVec
}

// NewEvents builds the counters. OpAMP server-health counters register on
// server; fleet-scoped counters register on fleet so the two groups can be
// scraped as separate jobs.
func NewEvents(server, fleet prometheus.Registerer) *Events {
	e := &Events{
		agentConnects: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "grex_agent_connects_total",
			Help: "Agent connect transitions.",
		}),
		agentDisconnects: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "grex_agent_disconnects_total",
			Help: "Agent disconnect transitions.",
		}),
		agentsEvicted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "grex_agents_evicted_total",
			Help: "Agents evicted after missing the check-in threshold.",
		}),
		reports: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "grex_agent_reports_total",
			Help: "Agent reports received, by kind.",
		}, []string{"type"}),
		missingAttributes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "grex_agent_missing_attributes_total",
			Help: "Required AgentDescription attributes reported missing, by key.",
		}, []string{"attribute"}),
		reservedAttributeConflicts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "grex_agent_reserved_attribute_conflicts_total",
			Help: "AgentDescription attributes reported that collide with a well-known read API filter field, by key.",
		}, []string{"attribute"}),
		messages: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "grex_opamp_messages_total",
			Help: "OpAMP messages processed.",
		}),
		messageErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "grex_opamp_message_errors_total",
			Help: "OpAMP messages that failed to read or decode.",
		}),
		gatewayConnects: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "grex_gateway_connects_total",
			Help: "Gateway per-agent connect delegations answered, by result.",
		}, []string{"result"}),
		gatewayConnections: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "grex_gateway_connections",
			Help: "Open connections that have sent a gateway connect message.",
		}),
		authDenied: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "grex_auth_denied_total",
			Help: "mTLS requests to the UI or telemetry listener denied, by reason.",
		}, []string{"reason"}),
		authAllowed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "grex_auth_allowed_total",
			Help: "mTLS requests to the UI or telemetry listener allowed, by resolved role.",
		}, []string{"role"}),
	}
	server.MustRegister(e.messages, e.messageErrors, e.authDenied, e.authAllowed)
	fleet.MustRegister(
		e.agentConnects, e.agentDisconnects, e.agentsEvicted,
		e.reports, e.missingAttributes, e.reservedAttributeConflicts,
		e.gatewayConnects, e.gatewayConnections,
	)
	return e
}

// AgentConnected implements fleet.Events.
func (e *Events) AgentConnected() { e.agentConnects.Inc() }

// AgentDisconnected implements fleet.Events.
func (e *Events) AgentDisconnected() { e.agentDisconnects.Inc() }

// AgentEvicted implements fleet.Events.
func (e *Events) AgentEvicted() { e.agentsEvicted.Inc() }

// ReportReceived implements fleet.Events.
func (e *Events) ReportReceived(kind string) { e.reports.WithLabelValues(kind).Inc() }

// MissingAttribute implements fleet.Events.
func (e *Events) MissingAttribute(key string) { e.missingAttributes.WithLabelValues(key).Inc() }

// ReservedAttributeConflict implements fleet.Events.
func (e *Events) ReservedAttributeConflict(key string) {
	e.reservedAttributeConflicts.WithLabelValues(key).Inc()
}

// Message counts one processed OpAMP message.
func (e *Events) Message() { e.messages.Inc() }

// MessageError counts one unreadable OpAMP message.
func (e *Events) MessageError() { e.messageErrors.Inc() }

// GatewayConnect counts one answered gateway connect delegation.
func (e *Events) GatewayConnect(accepted bool) {
	result := "accepted"
	if !accepted {
		result = "rejected"
	}
	e.gatewayConnects.WithLabelValues(result).Inc()
}

// GatewayConnectionOpened tracks a connection identifying itself as a gateway.
func (e *Events) GatewayConnectionOpened() { e.gatewayConnections.Inc() }

// GatewayConnectionClosed tracks a gateway connection closing.
func (e *Events) GatewayConnectionClosed() { e.gatewayConnections.Dec() }

// AuthDenied counts one mTLS request denied on the UI or telemetry listener.
func (e *Events) AuthDenied(reason string) { e.authDenied.WithLabelValues(reason).Inc() }

// AuthAllowed counts one mTLS request allowed on the UI or telemetry
// listener, by the role it resolved to.
func (e *Events) AuthAllowed(role string) { e.authAllowed.WithLabelValues(role).Inc() }

var (
	descAgentsConnected = prometheus.NewDesc(
		"grex_agents_connected",
		"Agents currently connected.",
		[]string{"transport", "via"}, nil)
	descAgentsDisconnected = prometheus.NewDesc(
		"grex_agents_disconnected",
		"Agents retained but not currently connected.",
		nil, nil)
	descAgentsNoncompliant = prometheus.NewDesc(
		"grex_agents_noncompliant",
		"Agents missing at least one required attribute.",
		nil, nil)
	descAgentsAwaitingFullState = prometheus.NewDesc(
		"grex_agents_awaiting_full_state",
		"Agents that have not yet reported their description.",
		nil, nil)
	descFleetSize = prometheus.NewDesc(
		"grex_fleet_size",
		"Total agents registered, regardless of the per-agent series cap.",
		nil, nil)
	descAgentSeriesCapped = prometheus.NewDesc(
		"grex_agent_series_capped",
		"Whether per-agent series are currently omitted because the fleet exceeds metrics.per_agent_series_limit.",
		nil, nil)
	descAgentHealth = prometheus.NewDesc(
		"grex_agent_health",
		"Agent health as reported: 1 healthy, 0 unhealthy.",
		[]string{"instance_uid"}, nil)
	descAgentLastSeen = prometheus.NewDesc(
		"grex_agent_last_seen_timestamp_seconds",
		"Unix time of the agent's last check-in.",
		[]string{"instance_uid"}, nil)
)

// FleetCollector derives gauges from fleet state at scrape time.
type FleetCollector struct {
	registry *fleet.Registry
	// perAgentLimit caps per-agent series; fleets larger than the limit
	// expose aggregates only.
	perAgentLimit int
}

// NewFleetCollector builds a collector over the fleet registry.
func NewFleetCollector(registry *fleet.Registry, perAgentLimit int) *FleetCollector {
	return &FleetCollector{registry: registry, perAgentLimit: perAgentLimit}
}

// Describe implements prometheus.Collector.
func (c *FleetCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- descAgentsConnected
	ch <- descAgentsDisconnected
	ch <- descAgentsNoncompliant
	ch <- descAgentsAwaitingFullState
	ch <- descFleetSize
	ch <- descAgentSeriesCapped
	ch <- descAgentHealth
	ch <- descAgentLastSeen
}

// Collect implements prometheus.Collector.
func (c *FleetCollector) Collect(ch chan<- prometheus.Metric) {
	agents := c.registry.List()

	connected := make(map[[2]string]float64)
	var disconnected, noncompliant, awaiting float64
	for _, agent := range agents {
		if agent.Connected {
			via := "direct"
			if agent.Conn.ViaGateway {
				via = "gateway"
			}
			connected[[2]string{agent.Conn.Transport, via}]++
		} else {
			disconnected++
		}
		if len(agent.MissingAttributes) > 0 {
			noncompliant++
		}
		if !agent.DescriptionReported {
			awaiting++
		}
	}
	for labels, count := range connected {
		ch <- prometheus.MustNewConstMetric(descAgentsConnected,
			prometheus.GaugeValue, count, labels[0], labels[1])
	}
	ch <- prometheus.MustNewConstMetric(descAgentsDisconnected,
		prometheus.GaugeValue, disconnected)
	ch <- prometheus.MustNewConstMetric(descAgentsNoncompliant,
		prometheus.GaugeValue, noncompliant)
	ch <- prometheus.MustNewConstMetric(descAgentsAwaitingFullState,
		prometheus.GaugeValue, awaiting)
	ch <- prometheus.MustNewConstMetric(descFleetSize,
		prometheus.GaugeValue, float64(len(agents)))

	capped := 0.0
	if len(agents) > c.perAgentLimit {
		capped = 1.0
	}
	ch <- prometheus.MustNewConstMetric(descAgentSeriesCapped,
		prometheus.GaugeValue, capped)

	if len(agents) > c.perAgentLimit {
		return
	}
	for _, agent := range agents {
		// Health is omitted until the agent reports it; a bare registry
		// entry (e.g. right after a server restart) must not read as
		// unhealthy.
		if agent.HealthReported {
			health := 0.0
			if agent.Healthy {
				health = 1.0
			}
			ch <- prometheus.MustNewConstMetric(descAgentHealth,
				prometheus.GaugeValue, health, agent.InstanceUID)
		}
		ch <- prometheus.MustNewConstMetric(descAgentLastSeen,
			prometheus.GaugeValue, float64(agent.LastSeen.Unix()), agent.InstanceUID)
	}
}

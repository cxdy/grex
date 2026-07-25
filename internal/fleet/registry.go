// Package fleet holds the in-memory registry of OpAMP agents. State is keyed
// by instance_uid, never by connection, because gateway connections multiplex
// many agents.
package fleet

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/protobufs"
)

// ConnMeta describes how an agent is connected.
type ConnMeta struct {
	// RemoteAddr is the peer address: the agent's for direct connections,
	// the gateway's for relayed ones.
	RemoteAddr string
	// TLSSubject is the client certificate subject of the peer.
	TLSSubject string
	// ViaGateway is true when the agent is relayed through an OpAMP gateway.
	ViaGateway bool
	// Transport is the OpAMP transport in use: "ws" or "http".
	Transport string
}

// Events receives fleet lifecycle notifications, e.g. for metrics. All
// methods are called with the registry lock held; implementations must not
// call back into the registry.
type Events interface {
	AgentConnected()
	AgentDisconnected()
	AgentEvicted()
	// ReportReceived is called per report kind present in a message:
	// "status", "health", or "effective_config".
	ReportReceived(kind string)
	// MissingAttribute is called for each newly missing required attribute
	// when an agent's compliance state changes.
	MissingAttribute(key string)
}

type noopEvents struct{}

func (noopEvents) AgentConnected()         {}
func (noopEvents) AgentDisconnected()      {}
func (noopEvents) AgentEvicted()           {}
func (noopEvents) ReportReceived(string)   {}
func (noopEvents) MissingAttribute(string) {}

// Agent is a point-in-time snapshot of one agent's state.
type Agent struct {
	InstanceUID       string
	Identifying       map[string]string
	NonIdentifying    map[string]string
	Capabilities      uint64
	Healthy           bool
	HealthError       string
	EffectiveConfig   string
	Conn              ConnMeta
	Connected         bool
	FirstSeen         time.Time
	LastSeen          time.Time
	MissingAttributes []string
}

// Config holds the registry settings.
type Config struct {
	// HeartbeatInterval is how often each agent is expected to check in.
	HeartbeatInterval time.Duration
	// StaleMissedHeartbeats is how many consecutive check-ins an agent may
	// miss before Sweep evicts it.
	StaleMissedHeartbeats int
	// RequiredAttributes lists AgentDescription attribute keys every agent
	// must report. Empty means no enforcement.
	RequiredAttributes []string
}

// Registry is the concurrency-safe fleet state.
type Registry struct {
	cfg    Config
	log    *slog.Logger
	now    func() time.Time
	events Events

	mu     sync.RWMutex
	agents map[string]*Agent
}

// New builds an empty registry. A nil events receives no notifications.
func New(cfg Config, logger *slog.Logger, events Events) *Registry {
	if events == nil {
		events = noopEvents{}
	}
	return &Registry{
		cfg:    cfg,
		log:    logger,
		now:    time.Now,
		events: events,
		agents: make(map[string]*Agent),
	}
}

// InstanceUID renders the 16-byte OpAMP instance uid as a UUID string.
func InstanceUID(raw []byte) (string, error) {
	uid, err := uuid.FromBytes(raw)
	if err != nil {
		return "", err
	}
	return uid.String(), nil
}

// Report folds an AgentToServer message into the registry. Messages with an
// invalid instance_uid are dropped. Fields absent from the message are
// retained from earlier reports.
func (r *Registry) Report(msg *protobufs.AgentToServer, meta ConnMeta) {
	id, err := InstanceUID(msg.GetInstanceUid())
	if err != nil {
		r.log.Warn("dropping report with invalid instance uid", "error", err)
		return
	}
	now := r.now()

	r.mu.Lock()
	defer r.mu.Unlock()

	agent, ok := r.agents[id]
	if !ok {
		agent = &Agent{InstanceUID: id, FirstSeen: now}
		r.agents[id] = agent
		r.log.Debug("agent registered",
			"instance_uid", id,
			"remote_addr", meta.RemoteAddr,
			"via_gateway", meta.ViaGateway)
	}
	if !agent.Connected {
		r.events.AgentConnected()
	}
	agent.LastSeen = now
	agent.Connected = true
	agent.Conn = meta
	if msg.Capabilities != 0 {
		agent.Capabilities = msg.Capabilities
	}
	if desc := msg.GetAgentDescription(); desc != nil {
		r.events.ReportReceived("status")
		agent.Identifying = attrsToMap(desc.GetIdentifyingAttributes())
		agent.NonIdentifying = attrsToMap(desc.GetNonIdentifyingAttributes())
		r.checkRequiredAttributes(agent)
	}
	if health := msg.GetHealth(); health != nil {
		r.events.ReportReceived("health")
		agent.Healthy = health.GetHealthy()
		agent.HealthError = health.GetLastError()
	}
	if cfgMap := msg.GetEffectiveConfig().GetConfigMap().GetConfigMap(); len(cfgMap) > 0 {
		r.events.ReportReceived("effective_config")
		names := make([]string, 0, len(cfgMap))
		for name := range cfgMap {
			names = append(names, name)
		}
		sort.Strings(names)
		var b strings.Builder
		for _, name := range names {
			if name != "" {
				b.WriteString("# " + name + "\n")
			}
			b.Write(cfgMap[name].GetBody())
			b.WriteString("\n")
		}
		agent.EffectiveConfig = b.String()
	}
}

func (r *Registry) checkRequiredAttributes(agent *Agent) {
	var missing []string
	for _, key := range r.cfg.RequiredAttributes {
		if _, ok := agent.Identifying[key]; ok {
			continue
		}
		if _, ok := agent.NonIdentifying[key]; ok {
			continue
		}
		missing = append(missing, key)
	}
	// Warn only on state change so repeated reports from a noncompliant
	// agent do not flood the log.
	changed := !slices.Equal(agent.MissingAttributes, missing)
	agent.MissingAttributes = missing
	if len(missing) > 0 && changed {
		for _, key := range missing {
			r.events.MissingAttribute(key)
		}
		r.log.Warn("agent missing required attributes",
			"instance_uid", agent.InstanceUID, "missing", missing)
	}
}

// SetConnected marks an agent's connection state. Disconnected agents stay
// registered until Sweep evicts them.
func (r *Registry) SetConnected(id string, connected bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	agent, ok := r.agents[id]
	if !ok {
		return
	}
	if agent.Connected && !connected {
		r.events.AgentDisconnected()
	} else if !agent.Connected && connected {
		r.events.AgentConnected()
	}
	agent.Connected = connected
}

// Get returns a snapshot of one agent.
func (r *Registry) Get(id string) (Agent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	agent, ok := r.agents[id]
	if !ok {
		return Agent{}, false
	}
	return snapshot(agent), true
}

// List returns a snapshot of every registered agent.
func (r *Registry) List() []Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := make([]Agent, 0, len(r.agents))
	for _, agent := range r.agents {
		list = append(list, snapshot(agent))
	}
	return list
}

// Sweep evicts agents whose last check-in is older than
// HeartbeatInterval * StaleMissedHeartbeats and returns their instance uids.
func (r *Registry) Sweep(now time.Time) []string {
	threshold := r.cfg.HeartbeatInterval * time.Duration(r.cfg.StaleMissedHeartbeats)
	r.mu.Lock()
	defer r.mu.Unlock()
	var evicted []string
	for id, agent := range r.agents {
		if now.Sub(agent.LastSeen) > threshold {
			delete(r.agents, id)
			evicted = append(evicted, id)
			r.events.AgentEvicted()
			r.log.Info("agent evicted",
				"instance_uid", id, "last_seen", agent.LastSeen)
		}
	}
	return evicted
}

// Run sweeps on every heartbeat interval until the context is cancelled.
func (r *Registry) Run(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			r.Sweep(now)
		}
	}
}

func snapshot(agent *Agent) Agent {
	s := *agent
	s.Identifying = maps.Clone(agent.Identifying)
	s.NonIdentifying = maps.Clone(agent.NonIdentifying)
	s.MissingAttributes = append([]string(nil), agent.MissingAttributes...)
	return s
}

func attrsToMap(attrs []*protobufs.KeyValue) map[string]string {
	m := make(map[string]string, len(attrs))
	for _, kv := range attrs {
		m[kv.GetKey()] = anyValueString(kv.GetValue())
	}
	return m
}

func anyValueString(v *protobufs.AnyValue) string {
	switch val := v.GetValue().(type) {
	case *protobufs.AnyValue_StringValue:
		return val.StringValue
	default:
		return v.String()
	}
}

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
}

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
	cfg Config
	log *slog.Logger
	now func() time.Time

	mu     sync.RWMutex
	agents map[string]*Agent
}

// New builds an empty registry.
func New(cfg Config, logger *slog.Logger) *Registry {
	return &Registry{
		cfg:    cfg,
		log:    logger,
		now:    time.Now,
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
	agent.LastSeen = now
	agent.Connected = true
	agent.Conn = meta
	if msg.Capabilities != 0 {
		agent.Capabilities = msg.Capabilities
	}
	if desc := msg.GetAgentDescription(); desc != nil {
		agent.Identifying = attrsToMap(desc.GetIdentifyingAttributes())
		agent.NonIdentifying = attrsToMap(desc.GetNonIdentifyingAttributes())
		r.checkRequiredAttributes(agent)
	}
	if health := msg.GetHealth(); health != nil {
		agent.Healthy = health.GetHealthy()
		agent.HealthError = health.GetLastError()
	}
	if cfgMap := msg.GetEffectiveConfig().GetConfigMap().GetConfigMap(); len(cfgMap) > 0 {
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
		r.log.Warn("agent missing required attributes",
			"instance_uid", agent.InstanceUID, "missing", missing)
	}
}

// SetConnected marks an agent's connection state. Disconnected agents stay
// registered until Sweep evicts them.
func (r *Registry) SetConnected(id string, connected bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if agent, ok := r.agents[id]; ok {
		agent.Connected = connected
	}
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

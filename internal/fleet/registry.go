// Package fleet holds the in-memory registry of OpAMP agents. State is keyed
// by instance_uid, never by connection, because gateway connections multiplex
// many agents.
package fleet

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/protobufs"
)

// ConnMeta describes how an agent is connected.
type ConnMeta struct {
	// RemoteAddr is the peer address: the agent's for direct connections,
	// the gateway's for relayed ones.
	RemoteAddr string `json:"remote_addr,omitempty"`
	// TLSSubject is the client certificate subject of the peer.
	TLSSubject string `json:"tls_subject,omitempty"`
	// ViaGateway is true when the agent is relayed through an OpAMP gateway.
	ViaGateway bool `json:"via_gateway"`
	// Transport is the OpAMP transport in use: "ws" or "http".
	Transport string `json:"transport,omitempty"`
}

// ReservedAttributeKeys are AgentDescription attribute keys that collide
// with the read API's well-known top-level agent filter fields
// (internal/api's ?healthy=/?connected=/?via_gateway=). An agent that
// reports one of these as an identifying or non-identifying attribute has
// that attribute permanently shadowed: the API filter always resolves the
// key against the top-level field, never the attribute map, for the caller
// there is no way to filter on the attribute's value.
var ReservedAttributeKeys = []string{"healthy", "connected", "via_gateway"}

// Events receives fleet lifecycle notifications, e.g. for metrics and
// persistence dirty-tracking. All methods are called with the registry lock
// held; implementations must not call back into the registry. instanceUID
// identifies which agent changed, so a listener that needs to act per-agent
// (e.g. marking it dirty for a durability flush) doesn't need its own
// tracking inside Registry.
type Events interface {
	AgentConnected(instanceUID string)
	AgentDisconnected(instanceUID string)
	AgentEvicted(instanceUID string)
	// ReportReceived is called per report kind present in a message:
	// "status", "health", "effective_config", or "package_statuses".
	ReportReceived(instanceUID, kind string)
	// MissingAttribute is called for each newly missing required attribute
	// when an agent's compliance state changes.
	MissingAttribute(instanceUID, key string)
	// ReservedAttributeConflict is called for each ReservedAttributeKeys key
	// an agent's AgentDescription reports, when that conflict set changes.
	ReservedAttributeConflict(instanceUID, key string)
}

type noopEvents struct{}

func (noopEvents) AgentConnected(string)                    {}
func (noopEvents) AgentDisconnected(string)                 {}
func (noopEvents) AgentEvicted(string)                      {}
func (noopEvents) ReportReceived(string, string)            {}
func (noopEvents) MissingAttribute(string, string)          {}
func (noopEvents) ReservedAttributeConflict(string, string) {}

// multiEvents fans a single Registry's notifications out to every listener
// in order, e.g. metrics and a persistence dirty-tracker both listening to
// the same Registry.
type multiEvents []Events

// MultiEvents combines multiple Events implementations into one, so
// Registry (which holds exactly one) can notify all of them.
func MultiEvents(events ...Events) Events { return multiEvents(events) }

func (m multiEvents) AgentConnected(id string) {
	for _, e := range m {
		e.AgentConnected(id)
	}
}

func (m multiEvents) AgentDisconnected(id string) {
	for _, e := range m {
		e.AgentDisconnected(id)
	}
}

func (m multiEvents) AgentEvicted(id string) {
	for _, e := range m {
		e.AgentEvicted(id)
	}
}

func (m multiEvents) ReportReceived(id, kind string) {
	for _, e := range m {
		e.ReportReceived(id, kind)
	}
}

func (m multiEvents) MissingAttribute(id, key string) {
	for _, e := range m {
		e.MissingAttribute(id, key)
	}
}

func (m multiEvents) ReservedAttributeConflict(id, key string) {
	for _, e := range m {
		e.ReservedAttributeConflict(id, key)
	}
}

// Agent is a point-in-time snapshot of one agent's state.
type Agent struct {
	InstanceUID string `json:"instance_uid"`
	// SequenceNum is the agent's last AgentToServer sequence_num. A jump of
	// more than 1 from the previous value indicates a dropped message.
	SequenceNum    uint64            `json:"sequence_num"`
	Identifying    map[string]string `json:"identifying_attributes,omitempty"`
	NonIdentifying map[string]string `json:"non_identifying_attributes,omitempty"`
	// Capabilities is the raw AgentCapabilities bitmask; MarshalJSON also
	// includes it decoded into named fields via DecodedCapabilities.
	Capabilities uint64 `json:"capabilities"`
	Healthy      bool   `json:"healthy"`
	HealthError  string `json:"health_error,omitempty"`
	// HealthStatus is the agent-defined status string (health.status); its
	// meaning is not standardized at the protocol level.
	HealthStatus string `json:"health_status,omitempty"`
	// HealthStartTime is when the reporting component started
	// (health.start_time_unix_nano). Zero if unset.
	HealthStartTime time.Time `json:"health_start_time"`
	// HealthStatusTime is when HealthStatus was observed. Zero if the agent
	// has not set health.status_time_unix_nano.
	HealthStatusTime time.Time `json:"health_status_time"`
	// HealthReported is true once the agent has sent a health report;
	// Healthy is meaningless before then.
	HealthReported bool `json:"health_reported"`
	// DescriptionReported is true once the agent has sent its
	// AgentDescription. False means grex is awaiting the agent's full state.
	DescriptionReported bool `json:"description_reported"`
	// EffectiveConfig maps config file/section name to body, mirroring
	// effective_config.config_map. The single-file convention uses "" as
	// the key.
	EffectiveConfig map[string]string `json:"effective_config,omitempty"`
	// Packages holds the agent's last reported package_statuses, keyed by
	// package name. Absent from a report retains the prior set.
	Packages          map[string]Package `json:"packages,omitempty"`
	Conn              ConnMeta           `json:"connection"`
	Connected         bool               `json:"connected"`
	FirstSeen         time.Time          `json:"first_seen"`
	LastSeen          time.Time          `json:"last_seen"`
	MissingAttributes []string           `json:"missing_attributes,omitempty"`
	// ReservedAttributeConflicts lists ReservedAttributeKeys this agent
	// reports as an AgentDescription attribute; those values are shadowed
	// in the read API by the top-level field of the same name.
	ReservedAttributeConflicts []string `json:"reserved_attribute_conflicts,omitempty"`
	// EvictedAt is set only on an Agent read back from persistence.StateStore
	// for a row that's been soft-deleted (see docs/developer/persistence.md).
	// Registry-held agents never set it. A read path presenting "live" fleet
	// state must treat a non-nil EvictedAt the same as not-found.
	EvictedAt *time.Time `json:"-"`
	// SessionUpdatedAt is set only on an Agent read back from
	// persistence.StateStore: the last time some replica's periodic session
	// snapshot confirmed this agent's Connected value (see
	// persistence.SessionSnapshotter), independent of LastSeen — identity/
	// health data (LastSeen's table) and liveness (this field's table) are
	// flushed on different cadences on purpose (see docs/spec/design.md's
	// Agent state schema). Registry-held agents never set it; api.StaleConnected
	// is the only reader.
	SessionUpdatedAt time.Time `json:"-"`
}

// Capabilities is AgentCapabilities decoded into named fields.
type Capabilities struct {
	ReportsStatus                   bool `json:"reports_status"`
	AcceptsRemoteConfig             bool `json:"accepts_remote_config"`
	ReportsEffectiveConfig          bool `json:"reports_effective_config"`
	AcceptsPackages                 bool `json:"accepts_packages"`
	ReportsPackageStatuses          bool `json:"reports_package_statuses"`
	ReportsOwnTraces                bool `json:"reports_own_traces"`
	ReportsOwnMetrics               bool `json:"reports_own_metrics"`
	ReportsOwnLogs                  bool `json:"reports_own_logs"`
	AcceptsOpAMPConnectionSettings  bool `json:"accepts_opamp_connection_settings"`
	AcceptsOtherConnectionSettings  bool `json:"accepts_other_connection_settings"`
	AcceptsRestartCommand           bool `json:"accepts_restart_command"`
	ReportsHealth                   bool `json:"reports_health"`
	ReportsRemoteConfig             bool `json:"reports_remote_config"`
	ReportsHeartbeat                bool `json:"reports_heartbeat"`
	ReportsAvailableComponents      bool `json:"reports_available_components"`
	ReportsConnectionSettingsStatus bool `json:"reports_connection_settings_status"`
}

// DecodedCapabilities decodes the raw Capabilities bitmask into named
// fields for API consumers.
func (a Agent) DecodedCapabilities() Capabilities {
	has := func(bit protobufs.AgentCapabilities) bool {
		return a.Capabilities&uint64(bit) != 0 //nolint:gosec // bit is a small non-negative protocol-defined enum constant
	}
	return Capabilities{
		ReportsStatus:                   has(protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus),
		AcceptsRemoteConfig:             has(protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig),
		ReportsEffectiveConfig:          has(protobufs.AgentCapabilities_AgentCapabilities_ReportsEffectiveConfig),
		AcceptsPackages:                 has(protobufs.AgentCapabilities_AgentCapabilities_AcceptsPackages),
		ReportsPackageStatuses:          has(protobufs.AgentCapabilities_AgentCapabilities_ReportsPackageStatuses),
		ReportsOwnTraces:                has(protobufs.AgentCapabilities_AgentCapabilities_ReportsOwnTraces),
		ReportsOwnMetrics:               has(protobufs.AgentCapabilities_AgentCapabilities_ReportsOwnMetrics),
		ReportsOwnLogs:                  has(protobufs.AgentCapabilities_AgentCapabilities_ReportsOwnLogs),
		AcceptsOpAMPConnectionSettings:  has(protobufs.AgentCapabilities_AgentCapabilities_AcceptsOpAMPConnectionSettings),
		AcceptsOtherConnectionSettings:  has(protobufs.AgentCapabilities_AgentCapabilities_AcceptsOtherConnectionSettings),
		AcceptsRestartCommand:           has(protobufs.AgentCapabilities_AgentCapabilities_AcceptsRestartCommand),
		ReportsHealth:                   has(protobufs.AgentCapabilities_AgentCapabilities_ReportsHealth),
		ReportsRemoteConfig:             has(protobufs.AgentCapabilities_AgentCapabilities_ReportsRemoteConfig),
		ReportsHeartbeat:                has(protobufs.AgentCapabilities_AgentCapabilities_ReportsHeartbeat),
		ReportsAvailableComponents:      has(protobufs.AgentCapabilities_AgentCapabilities_ReportsAvailableComponents),
		ReportsConnectionSettingsStatus: has(protobufs.AgentCapabilities_AgentCapabilities_ReportsConnectionSettingsStatus),
	}
}

// Package is one entry from an agent's package_statuses report.
type Package struct {
	Name                 string `json:"name"`
	AgentHasVersion      string `json:"agent_has_version,omitempty"`
	ServerOfferedVersion string `json:"server_offered_version,omitempty"`
	Status               string `json:"status,omitempty"`
	ErrorMessage         string `json:"error_message,omitempty"`
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
		r.events.AgentConnected(id)
	}
	agent.LastSeen = now
	agent.Connected = true
	agent.Conn = meta
	agent.SequenceNum = msg.GetSequenceNum()
	if msg.Capabilities != 0 {
		agent.Capabilities = msg.Capabilities
	}
	if desc := msg.GetAgentDescription(); desc != nil {
		r.events.ReportReceived(id, "status")
		agent.DescriptionReported = true
		agent.Identifying = attrsToMap(desc.GetIdentifyingAttributes())
		agent.NonIdentifying = attrsToMap(desc.GetNonIdentifyingAttributes())
		r.checkRequiredAttributes(agent)
		r.checkReservedAttributeConflicts(agent)
	}
	if health := msg.GetHealth(); health != nil {
		r.events.ReportReceived(id, "health")
		agent.HealthReported = true
		agent.Healthy = health.GetHealthy()
		agent.HealthError = health.GetLastError()
		agent.HealthStatus = health.GetStatus()
		if ns := health.GetStartTimeUnixNano(); ns != 0 {
			agent.HealthStartTime = time.Unix(0, int64(ns)) //nolint:gosec // protocol-defined nanosecond timestamp
		}
		if ns := health.GetStatusTimeUnixNano(); ns != 0 {
			agent.HealthStatusTime = time.Unix(0, int64(ns)) //nolint:gosec // protocol-defined nanosecond timestamp
		}
	}
	if pkgs := msg.GetPackageStatuses().GetPackages(); len(pkgs) > 0 {
		r.events.ReportReceived(id, "package_statuses")
		agent.Packages = make(map[string]Package, len(pkgs))
		for name, p := range pkgs {
			agent.Packages[name] = Package{
				Name:                 p.GetName(),
				AgentHasVersion:      p.GetAgentHasVersion(),
				ServerOfferedVersion: p.GetServerOfferedVersion(),
				Status:               p.GetStatus().String(),
				ErrorMessage:         p.GetErrorMessage(),
			}
		}
	}
	if cfgMap := msg.GetEffectiveConfig().GetConfigMap().GetConfigMap(); len(cfgMap) > 0 {
		r.events.ReportReceived(id, "effective_config")
		agent.EffectiveConfig = make(map[string]string, len(cfgMap))
		for name, file := range cfgMap {
			agent.EffectiveConfig[name] = string(file.GetBody())
		}
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
			r.events.MissingAttribute(agent.InstanceUID, key)
		}
		r.log.Warn("agent missing required attributes",
			"instance_uid", agent.InstanceUID, "missing", missing)
	}
}

// checkReservedAttributeConflicts records which ReservedAttributeKeys this
// agent reports as an AgentDescription attribute, warning and counting only
// on state change so repeated reports do not flood the log or the counter.
func (r *Registry) checkReservedAttributeConflicts(agent *Agent) {
	var conflicts []string
	for _, key := range ReservedAttributeKeys {
		if _, ok := agent.Identifying[key]; ok {
			conflicts = append(conflicts, key)
			continue
		}
		if _, ok := agent.NonIdentifying[key]; ok {
			conflicts = append(conflicts, key)
		}
	}
	changed := !slices.Equal(agent.ReservedAttributeConflicts, conflicts)
	agent.ReservedAttributeConflicts = conflicts
	if len(conflicts) > 0 && changed {
		for _, key := range conflicts {
			r.events.ReservedAttributeConflict(agent.InstanceUID, key)
		}
		r.log.Warn("agent attribute shadowed by a well-known API filter field",
			"instance_uid", agent.InstanceUID, "conflicts", conflicts)
	}
}

// SetConnected marks an agent's connection state. Disconnected agents stay
// registered until Sweep evicts them. Deliberately does not touch LastSeen:
// a durable StateStore's guarded writes are keyed on LastSeen as event time,
// and a disconnect detected well after the agent's last real message (e.g.
// by Sweep, possibly after it already reconnected to a different grex
// replica and flushed newer data) must still carry the old timestamp so
// that stale write is correctly rejected rather than clobbering the newer
// one. If this ever changes, that guarantee breaks.
func (r *Registry) SetConnected(id string, connected bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	agent, ok := r.agents[id]
	if !ok {
		return
	}
	if agent.Connected && !connected {
		r.events.AgentDisconnected(id)
	} else if !agent.Connected && connected {
		r.events.AgentConnected(id)
	}
	agent.Connected = connected
}

// Get returns a snapshot of one agent.
// HeartbeatInterval returns the same threshold Sweep uses to mark a locally
// registered agent disconnected after a missed check-in. Exposed so callers
// merging in DB-only agent records (see internal/api's MergeAgents) can
// apply the identical staleness rule instead of trusting a stored Connected
// value that Sweep never gets a chance to correct.
func (r *Registry) HeartbeatInterval() time.Duration {
	return r.cfg.HeartbeatInterval
}

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

// Sweep updates liveness for agents that have stopped checking in, then
// evicts those past the stale threshold. Returns instance uids of evicted
// agents.
//
// Liveness (two stages):
//  1. Missed one HeartbeatInterval without a check-in → mark disconnected.
//     This covers gateway-relayed agents: grex does not see per-agent TCP
//     close when an agent dies behind an OpAMP gateway, only the absence of
//     messages. Health bits are left as last reported so the UI can show
//     "Disconnected" rather than inventing Unhealthy.
//  2. Missed StaleMissedHeartbeats intervals → evict from the registry.
//
// Same LastSeen caveat as SetConnected: this never touches it, only
// Connected. See SetConnected's doc comment for why.
func (r *Registry) Sweep(now time.Time) []string {
	disconnectAfter := r.cfg.HeartbeatInterval
	evictAfter := r.cfg.HeartbeatInterval * time.Duration(r.cfg.StaleMissedHeartbeats)
	r.mu.Lock()
	defer r.mu.Unlock()
	var evicted []string
	for id, agent := range r.agents {
		age := now.Sub(agent.LastSeen)
		if agent.Connected && age > disconnectAfter {
			agent.Connected = false
			r.events.AgentDisconnected(id)
			r.log.Info("agent disconnected (missed check-in)",
				"instance_uid", id, "last_seen", agent.LastSeen, "age", age)
		}
		if age > evictAfter {
			delete(r.agents, id)
			evicted = append(evicted, id)
			r.events.AgentEvicted(id)
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
	s.EffectiveConfig = maps.Clone(agent.EffectiveConfig)
	s.Packages = maps.Clone(agent.Packages)
	s.MissingAttributes = append([]string(nil), agent.MissingAttributes...)
	s.ReservedAttributeConflicts = append([]string(nil), agent.ReservedAttributeConflicts...)
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

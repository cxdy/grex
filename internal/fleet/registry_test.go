package fleet

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/protobufs"
)

// recordingEvents counts event hook firings for assertions.
type recordingEvents struct {
	mu                sync.Mutex
	connects          int
	disconnects       int
	evictions         int
	reports           map[string]int
	missingAttributes map[string]int
}

func newRecordingEvents() *recordingEvents {
	return &recordingEvents{
		reports:           make(map[string]int),
		missingAttributes: make(map[string]int),
	}
}

func (e *recordingEvents) AgentConnected() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.connects++
}

func (e *recordingEvents) AgentDisconnected() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.disconnects++
}

func (e *recordingEvents) AgentEvicted() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.evictions++
}

func (e *recordingEvents) ReportReceived(kind string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.reports[kind]++
}

func (e *recordingEvents) MissingAttribute(key string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.missingAttributes[key]++
}

func testRegistry(required ...string) (*Registry, *bytes.Buffer) {
	r, logBuf, _ := testRegistryEvents(required...)
	return r, logBuf
}

func testRegistryEvents(required ...string) (*Registry, *bytes.Buffer, *recordingEvents) {
	var logBuf bytes.Buffer
	events := newRecordingEvents()
	r := New(Config{
		HeartbeatInterval:     30 * time.Second,
		StaleMissedHeartbeats: 3,
		RequiredAttributes:    required,
	}, slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})), events)
	return r, &logBuf, events
}

func strAttr(key, value string) *protobufs.KeyValue {
	return &protobufs.KeyValue{
		Key:   key,
		Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: value}},
	}
}

func testUID() uuid.UUID { return uuid.New() }

func statusMsg(uid uuid.UUID) *protobufs.AgentToServer {
	return &protobufs.AgentToServer{
		InstanceUid:  uid[:],
		Capabilities: uint64(protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus),
		AgentDescription: &protobufs.AgentDescription{
			IdentifyingAttributes: []*protobufs.KeyValue{
				strAttr("service.name", "otelcol-contrib"),
				strAttr("service.version", "0.157.0"),
			},
			NonIdentifyingAttributes: []*protobufs.KeyValue{
				strAttr("deployment.environment", "dev"),
			},
		},
	}
}

func TestReportRegistersAgent(t *testing.T) {
	r, logBuf := testRegistry()
	uid := testUID()
	meta := ConnMeta{RemoteAddr: "10.0.0.5:1234", TLSSubject: "CN=otelcol"}

	r.Report(statusMsg(uid), meta)

	logged := logBuf.String()
	if !strings.Contains(logged, "agent registered") || !strings.Contains(logged, uid.String()) {
		t.Errorf("registration not logged: %q", logged)
	}
	logBuf.Reset()
	r.Report(statusMsg(uid), meta)
	if strings.Contains(logBuf.String(), "agent registered") {
		t.Error("re-report of known agent logged as registration")
	}

	agent, ok := r.Get(uid.String())
	if !ok {
		t.Fatal("agent not registered")
	}
	if agent.Identifying["service.name"] != "otelcol-contrib" {
		t.Errorf("Identifying = %v", agent.Identifying)
	}
	if agent.NonIdentifying["deployment.environment"] != "dev" {
		t.Errorf("NonIdentifying = %v", agent.NonIdentifying)
	}
	if agent.Conn != meta {
		t.Errorf("Conn = %+v, want %+v", agent.Conn, meta)
	}
	if !agent.Connected {
		t.Error("Connected = false after report")
	}
	if agent.FirstSeen.IsZero() || agent.LastSeen.IsZero() {
		t.Error("timestamps not set")
	}
	if agent.Capabilities != uint64(protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus) {
		t.Errorf("Capabilities = %d", agent.Capabilities)
	}
	if len(r.List()) != 1 {
		t.Errorf("List len = %d", len(r.List()))
	}
}

// Per-agent lifecycle logging is debug level so production fleets with
// thousands of connections stay quiet at the default info level.
func TestRegistrationSilentAtInfoLevel(t *testing.T) {
	var logBuf bytes.Buffer
	r := New(Config{
		HeartbeatInterval:     30 * time.Second,
		StaleMissedHeartbeats: 3,
	}, slog.New(slog.NewTextHandler(&logBuf, nil)), nil)

	r.Report(statusMsg(testUID()), ConnMeta{})
	if strings.Contains(logBuf.String(), "agent registered") {
		t.Errorf("registration logged at info level: %q", logBuf.String())
	}
}

func TestReportUpdatesHealthAndEffectiveConfig(t *testing.T) {
	r, _ := testRegistry()
	uid := testUID()
	r.Report(statusMsg(uid), ConnMeta{})

	r.Report(&protobufs.AgentToServer{
		InstanceUid: uid[:],
		Health:      &protobufs.ComponentHealth{Healthy: false, LastError: "exporter down"},
		EffectiveConfig: &protobufs.EffectiveConfig{
			ConfigMap: &protobufs.AgentConfigMap{
				ConfigMap: map[string]*protobufs.AgentConfigFile{
					"": {Body: []byte("receivers: {}")},
				},
			},
		},
	}, ConnMeta{})

	agent, ok := r.Get(uid.String())
	if !ok {
		t.Fatal("agent missing")
	}
	if agent.Healthy || agent.HealthError != "exporter down" {
		t.Errorf("health = %v %q", agent.Healthy, agent.HealthError)
	}
	if !strings.Contains(agent.EffectiveConfig, "receivers: {}") {
		t.Errorf("EffectiveConfig = %q", agent.EffectiveConfig)
	}
	// Fields absent from a later message are retained.
	if agent.Identifying["service.name"] != "otelcol-contrib" {
		t.Error("description lost on partial report")
	}
}

func TestRequiredAttributesCompliance(t *testing.T) {
	r, logBuf := testRegistry("deployment.environment", "team")
	uid := testUID()
	r.Report(statusMsg(uid), ConnMeta{})

	agent, _ := r.Get(uid.String())
	if !slices.Equal(agent.MissingAttributes, []string{"team"}) {
		t.Errorf("MissingAttributes = %v, want [team]", agent.MissingAttributes)
	}
	logged := logBuf.String()
	if !strings.Contains(logged, "team") || !strings.Contains(logged, uid.String()) {
		t.Errorf("warning log missing agent or key: %q", logged)
	}

	// Re-reporting the same missing set does not repeat the warning.
	logBuf.Reset()
	r.Report(statusMsg(uid), ConnMeta{})
	if strings.Contains(logBuf.String(), "missing") {
		t.Errorf("unchanged missing set warned again: %q", logBuf.String())
	}

	// Becoming compliant and then noncompliant again warns again.
	fixed := statusMsg(uid)
	fixed.AgentDescription.NonIdentifyingAttributes = append(
		fixed.AgentDescription.NonIdentifyingAttributes, strAttr("team", "o11y"))
	r.Report(fixed, ConnMeta{})
	logBuf.Reset()
	r.Report(statusMsg(uid), ConnMeta{})
	if !strings.Contains(logBuf.String(), "missing") {
		t.Error("regression to noncompliant did not warn")
	}

	// Compliant agent has no missing attributes and no new warning.
	logBuf.Reset()
	uid2 := testUID()
	msg := statusMsg(uid2)
	msg.AgentDescription.NonIdentifyingAttributes = append(
		msg.AgentDescription.NonIdentifyingAttributes, strAttr("team", "o11y"))
	r.Report(msg, ConnMeta{})
	agent2, _ := r.Get(uid2.String())
	if agent2.MissingAttributes != nil {
		t.Errorf("MissingAttributes = %v, want nil", agent2.MissingAttributes)
	}
	if strings.Contains(logBuf.String(), "missing") {
		t.Errorf("unexpected missing-attribute warning for compliant agent: %q", logBuf.String())
	}
}

func TestSweepEvictsStaleAgents(t *testing.T) {
	r, logBuf := testRegistry()
	base := time.Now()
	r.now = func() time.Time { return base }
	uid := testUID()
	r.Report(statusMsg(uid), ConnMeta{})

	// Before the threshold (3 * 30s): retained.
	if evicted := r.Sweep(base.Add(89 * time.Second)); len(evicted) != 0 {
		t.Fatalf("evicted early: %v", evicted)
	}
	// Past the threshold: evicted.
	evicted := r.Sweep(base.Add(91 * time.Second))
	if !slices.Equal(evicted, []string{uid.String()}) {
		t.Fatalf("evicted = %v, want [%s]", evicted, uid)
	}
	if _, ok := r.Get(uid.String()); ok {
		t.Error("agent still present after eviction")
	}
	if !strings.Contains(logBuf.String(), "evicted") {
		t.Errorf("eviction not logged: %q", logBuf.String())
	}
}

func TestReconnectAfterEvictionReRegisters(t *testing.T) {
	r, _ := testRegistry()
	base := time.Now()
	r.now = func() time.Time { return base }
	uid := testUID()
	r.Report(statusMsg(uid), ConnMeta{})
	r.Sweep(base.Add(time.Hour))

	later := base.Add(2 * time.Hour)
	r.now = func() time.Time { return later }
	r.Report(statusMsg(uid), ConnMeta{})
	agent, ok := r.Get(uid.String())
	if !ok {
		t.Fatal("agent not re-registered")
	}
	if !agent.FirstSeen.Equal(later) {
		t.Errorf("FirstSeen = %v, want fresh registration at %v", agent.FirstSeen, later)
	}
}

func TestSetConnected(t *testing.T) {
	r, _ := testRegistry()
	uid := testUID()
	r.Report(statusMsg(uid), ConnMeta{})

	r.SetConnected(uid.String(), false)
	agent, ok := r.Get(uid.String())
	if !ok {
		t.Fatal("disconnected agent should stay registered until evicted")
	}
	if agent.Connected {
		t.Error("Connected = true after SetConnected(false)")
	}
}

func TestEventsFire(t *testing.T) {
	r, _, events := testRegistryEvents()
	base := time.Now()
	r.now = func() time.Time { return base }
	uid := testUID()

	// Registration fires a connect and a status report.
	r.Report(statusMsg(uid), ConnMeta{})
	// Health and effective config reports are counted by kind.
	r.Report(&protobufs.AgentToServer{
		InstanceUid: uid[:],
		Health:      &protobufs.ComponentHealth{Healthy: true},
		EffectiveConfig: &protobufs.EffectiveConfig{
			ConfigMap: &protobufs.AgentConfigMap{
				ConfigMap: map[string]*protobufs.AgentConfigFile{"": {Body: []byte("x")}},
			},
		},
	}, ConnMeta{})
	// Disconnect fires once; repeating it does not.
	r.SetConnected(uid.String(), false)
	r.SetConnected(uid.String(), false)
	// Reconnect fires a second connect.
	r.Report(statusMsg(uid), ConnMeta{})
	// Eviction fires.
	r.Sweep(base.Add(time.Hour))

	if events.connects != 2 {
		t.Errorf("connects = %d, want 2 (register + reconnect)", events.connects)
	}
	if events.disconnects != 1 {
		t.Errorf("disconnects = %d, want 1", events.disconnects)
	}
	if events.evictions != 1 {
		t.Errorf("evictions = %d, want 1", events.evictions)
	}
	if events.reports["status"] != 2 {
		t.Errorf("status reports = %d, want 2", events.reports["status"])
	}
	if events.reports["health"] != 1 || events.reports["effective_config"] != 1 {
		t.Errorf("reports = %v", events.reports)
	}
}

func TestMissingAttributeEvents(t *testing.T) {
	r, _, events := testRegistryEvents("team", "deployment.environment")
	uid := testUID()

	r.Report(statusMsg(uid), ConnMeta{}) // has deployment.environment, missing team
	r.Report(statusMsg(uid), ConnMeta{}) // unchanged: no second event

	if events.missingAttributes["team"] != 1 {
		t.Errorf("missing team events = %d, want 1 (state change only)", events.missingAttributes["team"])
	}
	if events.missingAttributes["deployment.environment"] != 0 {
		t.Errorf("deployment.environment counted missing: %v", events.missingAttributes)
	}
}

func TestNilEventsSafe(t *testing.T) {
	r := New(Config{
		HeartbeatInterval:     30 * time.Second,
		StaleMissedHeartbeats: 3,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	uid := testUID()
	r.Report(statusMsg(uid), ConnMeta{})
	r.SetConnected(uid.String(), false)
	r.Sweep(time.Now().Add(time.Hour))
}

func TestReportIgnoresInvalidInstanceUID(t *testing.T) {
	r, _ := testRegistry()
	r.Report(&protobufs.AgentToServer{InstanceUid: []byte{1, 2, 3}}, ConnMeta{})
	if len(r.List()) != 0 {
		t.Errorf("agent registered from invalid instance uid: %v", r.List())
	}
}

func TestConcurrentAccess(t *testing.T) {
	r, _ := testRegistry()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			uid := testUID()
			for range 50 {
				r.Report(statusMsg(uid), ConnMeta{RemoteAddr: fmt.Sprintf("10.0.0.%d:1", n)})
				r.List()
				r.Get(uid.String())
				r.Sweep(time.Now())
			}
		}(i)
	}
	wg.Wait()
	if len(r.List()) != 8 {
		t.Errorf("List len = %d, want 8", len(r.List()))
	}
}

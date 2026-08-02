package persistence

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dennisme/grex/internal/fleet"
	"github.com/open-telemetry/opamp-go/protobufs"
)

// testInstanceUID builds a deterministic, distinct fleet.Agent instance_uid
// from a small integer, so tests can report several different agents
// without hand-writing a 16-byte literal each time.
func testInstanceUID(t *testing.T, n byte) (id string, raw []byte) {
	t.Helper()
	raw = make([]byte, 16)
	raw[15] = n
	id, err := fleet.InstanceUID(raw)
	if err != nil {
		t.Fatal(err)
	}
	return id, raw
}

// erroringStateStore fails every SaveAgent/SoftDeleteAgent call, to exercise
// Flusher.flushOnce's error-logging branches. GetAgent/ListAgents/DeleteAgent
// are unused by Flusher and just panic if ever called.
type erroringStateStore struct{}

func (erroringStateStore) SaveAgent(context.Context, fleet.Agent) error {
	return errors.New("save failed")
}

func (erroringStateStore) SaveSession(context.Context, fleet.Agent) error {
	return errors.New("save failed")
}

func (erroringStateStore) SoftDeleteAgent(context.Context, string, time.Time) error {
	return errors.New("soft delete failed")
}

func (erroringStateStore) GetAgent(context.Context, string) (fleet.Agent, bool, error) {
	panic("not used by Flusher")
}

func (erroringStateStore) ListAgents(context.Context) ([]fleet.Agent, error) {
	panic("not used by Flusher")
}

func (erroringStateStore) DeleteAgent(context.Context, string) error {
	panic("not used by Flusher")
}

var _ StateStore = erroringStateStore{}

// erroringConnectionStore fails every UpsertAgentConnection call, to
// exercise flushOnce's connection-upsert error-logging branch.
// Get/List/Delete are unused by Flusher and just panic if ever called.
type erroringConnectionStore struct{}

func (erroringConnectionStore) UpsertAgentConnection(context.Context, AgentConnection) error {
	return errors.New("upsert failed")
}

func (erroringConnectionStore) GetAgentConnection(context.Context, string) (AgentConnection, bool, error) {
	panic("not used by Flusher")
}

func (erroringConnectionStore) ListAgentConnections(context.Context) ([]AgentConnection, error) {
	panic("not used by Flusher")
}

func (erroringConnectionStore) DeleteAgentConnection(context.Context, string) error {
	panic("not used by Flusher")
}

var _ ConnectionStore = erroringConnectionStore{}

func TestDirtyTrackerDrain(t *testing.T) {
	d := NewDirtyTracker()
	if got := d.Drain(); got != nil {
		t.Fatalf("Drain on empty tracker = %v, want nil", got)
	}

	d.AgentConnected("agent-1")
	d.ReportReceived("agent-2", "status")
	d.AgentConnected("agent-1") // duplicate, still one entry
	d.MissingAttribute("agent-3", "team")
	d.ReservedAttributeConflict("agent-4", "healthy")

	got := d.Drain()
	want := map[string]bool{"agent-1": true, "agent-2": true, "agent-3": true, "agent-4": true}
	if len(got) != len(want) {
		t.Fatalf("Drain = %v, want %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("Drain returned unexpected id %q", id)
		}
	}

	if got := d.Drain(); got != nil {
		t.Fatalf("second Drain = %v, want nil (cleared)", got)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestFlusherSavesDirtyAgents(t *testing.T) {
	dirty := NewDirtyTracker()
	store := &fakeStateStore{}
	registry := fleet.New(fleet.Config{HeartbeatInterval: time.Minute, StaleMissedHeartbeats: 3}, discardLogger(), dirty)

	uid := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	registry.Report(&protobufs.AgentToServer{InstanceUid: uid}, fleet.ConnMeta{})

	flusher := NewFlusher(registry, dirty, store, time.Hour, discardLogger(), 4, nil, &fakeConnectionStore{}, "replica-1", "")
	flusher.flushOnce(context.Background())

	id, err := fleet.InstanceUID(uid)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.agents[id]; !ok {
		t.Fatalf("flush did not save agent %s; saved: %v", id, store.agents)
	}
}

// TestFlusherUpsertsAgentConnectionForConnectedAgent covers the
// agent_connections side of flushOnce: a dirty agent still in the registry
// and Connected gets upserted under this Flusher's own replicaID/label, per
// docs/spec/design.md's Dispatch routing section.
func TestFlusherUpsertsAgentConnectionForConnectedAgent(t *testing.T) {
	dirty := NewDirtyTracker()
	store := &fakeStateStore{}
	conns := &fakeConnectionStore{}
	registry := fleet.New(fleet.Config{HeartbeatInterval: time.Minute, StaleMissedHeartbeats: 3}, discardLogger(), dirty)

	uid := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	registry.Report(&protobufs.AgentToServer{InstanceUid: uid}, fleet.ConnMeta{})
	id, err := fleet.InstanceUID(uid)
	if err != nil {
		t.Fatal(err)
	}

	flusher := NewFlusher(registry, dirty, store, time.Hour, discardLogger(), 4, nil, conns, "replica-1", "grex-pod-a")
	flusher.flushOnce(context.Background())

	got, ok, err := conns.GetAgentConnection(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("flush did not upsert agent_connections for %s", id)
	}
	if got.ReplicaID != "replica-1" || got.ReplicaLabel != "grex-pod-a" {
		t.Errorf("upserted connection = %+v, want ReplicaID=replica-1 ReplicaLabel=grex-pod-a", got)
	}
}

// TestFlusherSkipsAgentConnectionForDisconnectedAgent covers the
// upsert-only decision: a dirty agent still in the registry but
// Connected == false must not touch agent_connections at all (no delete,
// no upsert) — deleting here would race a different replica's fresher
// upsert for the same instance_uid after it reconnects elsewhere.
func TestFlusherSkipsAgentConnectionForDisconnectedAgent(t *testing.T) {
	dirty := NewDirtyTracker()
	store := &fakeStateStore{}
	conns := &fakeConnectionStore{}
	registry := fleet.New(fleet.Config{HeartbeatInterval: time.Minute, StaleMissedHeartbeats: 3}, discardLogger(), dirty)

	uid := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	registry.Report(&protobufs.AgentToServer{InstanceUid: uid}, fleet.ConnMeta{})
	id, err := fleet.InstanceUID(uid)
	if err != nil {
		t.Fatal(err)
	}
	registry.SetConnected(id, false)

	flusher := NewFlusher(registry, dirty, store, time.Hour, discardLogger(), 4, nil, conns, "replica-1", "grex-pod-a")
	flusher.flushOnce(context.Background())

	if conns.hasConnection(id) {
		t.Fatalf("flush upserted agent_connections for a disconnected agent: %+v", conns.conns[id])
	}
}

// TestFlusherSkipsAgentConnectionForEvictedAgent covers the soft-delete
// branch (agent no longer in the registry): it must not touch
// agent_connections either, same upsert-only reasoning as the disconnected
// case above.
func TestFlusherSkipsAgentConnectionForEvictedAgent(t *testing.T) {
	dirty := NewDirtyTracker()
	store := &fakeStateStore{}
	conns := &fakeConnectionStore{}
	registry := fleet.New(fleet.Config{HeartbeatInterval: time.Minute, StaleMissedHeartbeats: 3}, discardLogger(), nil)

	dirty.AgentEvicted("gone")
	flusher := NewFlusher(registry, dirty, store, time.Hour, discardLogger(), 4, nil, conns, "replica-1", "grex-pod-a")
	flusher.flushOnce(context.Background())

	if conns.hasConnection("gone") {
		t.Fatal("flush upserted agent_connections for an evicted agent")
	}
}

func TestFlusherSoftDeletesAgentNoLongerInRegistry(t *testing.T) {
	dirty := NewDirtyTracker()
	store := &fakeStateStore{}
	registry := fleet.New(fleet.Config{HeartbeatInterval: time.Minute, StaleMissedHeartbeats: 3}, discardLogger(), nil)

	dirty.AgentEvicted("gone")
	flusher := NewFlusher(registry, dirty, store, time.Hour, discardLogger(), 4, nil, &fakeConnectionStore{}, "replica-1", "")
	flusher.flushOnce(context.Background())

	if len(store.agents) != 0 {
		t.Fatalf("flush saved a full agent for one not in the registry: %v", store.agents)
	}
	if _, ok := store.evicted["gone"]; !ok {
		t.Fatal("flush did not soft-delete the evicted agent")
	}
}

// TestFlusherBatchesSoftDeletes covers docs/spec/design.md's Scaling gaps
// items 3-4: when the store supports BatchStateStore, several evicted
// agents' soft-deletes go through one chunked pgx.Batch round trip instead
// of one SoftDeleteAgent call per agent. fakeBatchStore.SoftDeleteAgent
// panics — if flushOnce ever fell back to the per-row path here, this test
// would panic instead of just failing an assertion, a stronger signal that
// the batched path is genuinely what ran.
func TestFlusherBatchesSoftDeletes(t *testing.T) {
	dirty := NewDirtyTracker()
	store := &fakeBatchStore{}
	registry := fleet.New(fleet.Config{HeartbeatInterval: time.Minute, StaleMissedHeartbeats: 3}, discardLogger(), nil)

	dirty.AgentEvicted("agent-1")
	dirty.AgentEvicted("agent-2")
	dirty.AgentEvicted("agent-3")

	flusher := NewFlusher(registry, dirty, store, time.Hour, discardLogger(), 4, nil, store, "replica-1", "")
	flusher.flushOnce(context.Background())

	for _, id := range []string{"agent-1", "agent-2", "agent-3"} {
		if !store.wasEvicted(id) {
			t.Errorf("%s was not soft-deleted", id)
		}
	}
	if got := store.sendBatchCalls(); got != 1 {
		t.Errorf("sendBatchCalls = %d, want 1 (all three fit in one chunk)", got)
	}
}

// TestFlusherBatchesAgentConnectionUpserts is TestFlusherBatchesSoftDeletes'
// counterpart for the other batchable write: UpsertAgentConnection for
// connected agents.
func TestFlusherBatchesAgentConnectionUpserts(t *testing.T) {
	dirty := NewDirtyTracker()
	batchStore := &fakeBatchStore{}
	saveStore := &fakeStateStore{} // SaveAgent stays per-row regardless — not batched, see flush.go
	registry := fleet.New(fleet.Config{HeartbeatInterval: time.Minute, StaleMissedHeartbeats: 3}, discardLogger(), dirty)

	for n := byte(1); n <= 3; n++ {
		id, raw := testInstanceUID(t, n)
		registry.Report(&protobufs.AgentToServer{InstanceUid: raw}, fleet.ConnMeta{})
		_ = id
	}

	flusher := NewFlusher(registry, dirty, saveStore, time.Hour, discardLogger(), 4, nil, batchStore, "replica-1", "grex-pod-a")
	flusher.flushOnce(context.Background())

	for n := byte(1); n <= 3; n++ {
		id, _ := testInstanceUID(t, n)
		if !batchStore.wasConnectionUpserted(id) {
			t.Errorf("%s: agent_connections was not upserted", id)
		}
	}
	if got := batchStore.sendBatchCalls(); got != 1 {
		t.Errorf("sendBatchCalls = %d, want 1 (all three fit in one chunk)", got)
	}
}

// TestFlusherBatchChunkErrorDoesNotStopRestOfChunk is the concrete proof
// behind the goal's core claim: one bad row's error, surfaced when its
// batch result is read, must not prevent the rest of that same chunk's
// results from being read and applied.
func TestFlusherBatchChunkErrorDoesNotStopRestOfChunk(t *testing.T) {
	dirty := NewDirtyTracker()
	store := &fakeBatchStore{errFor: map[string]error{"agent-2": errors.New("constraint violation")}}
	registry := fleet.New(fleet.Config{HeartbeatInterval: time.Minute, StaleMissedHeartbeats: 3}, discardLogger(), nil)

	dirty.AgentEvicted("agent-1")
	dirty.AgentEvicted("agent-2")
	dirty.AgentEvicted("agent-3")

	flusher := NewFlusher(registry, dirty, store, time.Hour, discardLogger(), 4, nil, store, "replica-1", "")
	flusher.flushOnce(context.Background())

	if !store.wasEvicted("agent-1") {
		t.Error("agent-1 (before the failing row) was not soft-deleted")
	}
	if store.wasEvicted("agent-2") {
		t.Error("agent-2 should have failed, not been recorded as evicted")
	}
	if !store.wasEvicted("agent-3") {
		t.Error("agent-3 (after the failing row) was not soft-deleted — one bad row must not stop the rest of its chunk")
	}
}

// TestFlusherSoftDeletesEvictedAgentAgainstRealPostgres covers the whole
// real path end to end: fleet.Registry evicts an agent via Sweep, the
// DirtyTracker (as the registry's Events) marks it dirty, and Flusher
// soft-deletes its row in a real Postgres — not the fake store the other
// Flusher tests use, and not deleted outright.
func TestFlusherSoftDeletesEvictedAgentAgainstRealPostgres(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	dirty := NewDirtyTracker()
	registry := fleet.New(fleet.Config{HeartbeatInterval: time.Second, StaleMissedHeartbeats: 1}, discardLogger(), dirty)

	uid := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	registry.Report(&protobufs.AgentToServer{InstanceUid: uid}, fleet.ConnMeta{})
	id, err := fleet.InstanceUID(uid)
	if err != nil {
		t.Fatal(err)
	}

	flusher := NewFlusher(registry, dirty, store, time.Hour, discardLogger(), 4, nil, store, "replica-1", "grex-test-pod")
	flusher.flushOnce(ctx) // agent exists, saved for real before eviction

	if _, ok, err := store.GetAgent(ctx, id); err != nil || !ok {
		t.Fatalf("GetAgent before eviction: ok=%v err=%v, want present", ok, err)
	}

	evicted := registry.Sweep(time.Now().Add(10 * time.Second))
	if len(evicted) != 1 || evicted[0] != id {
		t.Fatalf("Sweep evicted = %v, want [%s]", evicted, id)
	}

	flusher.flushOnce(ctx)

	var evictedAt *time.Time
	err = store.pool.QueryRow(ctx, `SELECT evicted_at FROM agents WHERE instance_uid = $1`, id).Scan(&evictedAt)
	if err != nil {
		t.Fatalf("query evicted_at: %v", err)
	}
	if evictedAt == nil {
		t.Fatal("evicted_at is NULL, want it set")
	}

	// Row must still exist (soft delete, not delete) and stay reachable
	// through GetAgent, which does not filter on evicted_at (that's the
	// out-of-scope API/UI concern, not this goal's).
	if _, ok, err := store.GetAgent(ctx, id); err != nil || !ok {
		t.Fatalf("GetAgent after eviction: ok=%v err=%v, want still present (soft delete, not removed)", ok, err)
	}
}

// TestFlusherRunFlushesOnTick covers Run itself, not just flushOnce: a real
// ticker firing on a short interval, until ctx is cancelled.
func TestFlusherRunFlushesOnTick(t *testing.T) {
	dirty := NewDirtyTracker()
	store := &fakeStateStore{}
	registry := fleet.New(fleet.Config{HeartbeatInterval: time.Minute, StaleMissedHeartbeats: 3}, discardLogger(), dirty)

	uid := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	registry.Report(&protobufs.AgentToServer{InstanceUid: uid}, fleet.ConnMeta{})
	id, err := fleet.InstanceUID(uid)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	flusher := NewFlusher(registry, dirty, store, 20*time.Millisecond, discardLogger(), 4, nil, &fakeConnectionStore{}, "replica-1", "")
	done := make(chan struct{})
	go func() {
		flusher.Run(ctx)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !store.hasAgent(id) {
		time.Sleep(10 * time.Millisecond)
	}
	if !store.hasAgent(id) {
		t.Fatal("Run did not flush the dirty agent within the deadline")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

// TestFlusherLogsStoreErrors covers flushOnce's error-logging branches for
// SaveAgent, SoftDeleteAgent, and UpsertAgentConnection failures: it must
// log and continue, never panic or stop processing the rest of the dirty
// set.
func TestFlusherLogsStoreErrors(t *testing.T) {
	dirty := NewDirtyTracker()
	registry := fleet.New(fleet.Config{HeartbeatInterval: time.Minute, StaleMissedHeartbeats: 3}, discardLogger(), dirty)

	uid := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	registry.Report(&protobufs.AgentToServer{InstanceUid: uid}, fleet.ConnMeta{})
	dirty.AgentEvicted("gone") // not in the registry, exercises the SoftDeleteAgent path

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	flusher := NewFlusher(registry, dirty, erroringStateStore{}, time.Hour, log, 4, nil, erroringConnectionStore{}, "replica-1", "")
	flusher.flushOnce(context.Background()) // must not panic

	out := buf.String()
	if !strings.Contains(out, "persistence flush failed") || !strings.Contains(out, "save failed") {
		t.Errorf("log missing SaveAgent failure: %s", out)
	}
	if !strings.Contains(out, "persistence soft-delete failed") || !strings.Contains(out, "soft delete failed") {
		t.Errorf("log missing SoftDeleteAgent failure: %s", out)
	}
	if !strings.Contains(out, "persistence agent_connections upsert failed") || !strings.Contains(out, "upsert failed") {
		t.Errorf("log missing UpsertAgentConnection failure: %s", out)
	}
}

// blockingFlusherStore blocks SaveAgent for one specific instance_uid until
// its context is done; every other instance_uid saves immediately and
// records when it completed. Used to prove one stuck agent's write doesn't
// force the rest of the same flushOnce batch to queue behind it.
type blockingFlusherStore struct {
	stuckID string

	mu        sync.Mutex
	completed map[string]time.Time
}

func newBlockingFlusherStore(stuckID string) *blockingFlusherStore {
	return &blockingFlusherStore{stuckID: stuckID, completed: make(map[string]time.Time)}
}

func (b *blockingFlusherStore) SaveAgent(ctx context.Context, agent fleet.Agent) error {
	if agent.InstanceUID == b.stuckID {
		<-ctx.Done()
		return ctx.Err()
	}
	b.mu.Lock()
	b.completed[agent.InstanceUID] = time.Now()
	b.mu.Unlock()
	return nil
}

func (b *blockingFlusherStore) SaveSession(context.Context, fleet.Agent) error {
	panic("not used by Flusher")
}

func (b *blockingFlusherStore) SoftDeleteAgent(context.Context, string, time.Time) error {
	panic("not used by this test")
}

func (b *blockingFlusherStore) GetAgent(context.Context, string) (fleet.Agent, bool, error) {
	panic("not used by Flusher")
}

func (b *blockingFlusherStore) ListAgents(context.Context) ([]fleet.Agent, error) {
	panic("not used by Flusher")
}

func (b *blockingFlusherStore) DeleteAgent(context.Context, string) error {
	panic("not used by Flusher")
}

var _ StateStore = (*blockingFlusherStore)(nil)

// TestFlusherStuckAgentDoesNotBlockOthers is the concrete Flusher-level
// version of runConcurrent's own property test: a single stuck SaveAgent
// call must not delay the other agents in the same flushOnce batch.
func TestFlusherStuckAgentDoesNotBlockOthers(t *testing.T) {
	dirty := NewDirtyTracker()
	registry := fleet.New(fleet.Config{HeartbeatInterval: time.Minute, StaleMissedHeartbeats: 3}, discardLogger(), dirty)

	stuckID, stuckRaw := testInstanceUID(t, 1)
	registry.Report(&protobufs.AgentToServer{InstanceUid: stuckRaw}, fleet.ConnMeta{})
	var fastIDs []string
	for n := byte(2); n <= 6; n++ {
		id, raw := testInstanceUID(t, n)
		registry.Report(&protobufs.AgentToServer{InstanceUid: raw}, fleet.ConnMeta{})
		fastIDs = append(fastIDs, id)
	}

	store := newBlockingFlusherStore(stuckID)
	timeout := 200 * time.Millisecond
	flusher := NewFlusher(registry, dirty, store, timeout, discardLogger(), 4, nil, &fakeConnectionStore{}, "replica-1", "")

	start := time.Now()
	flusher.flushOnce(context.Background())
	elapsed := time.Since(start)

	if elapsed < timeout {
		t.Fatalf("flushOnce returned before the stuck write's %v timeout: elapsed=%v", timeout, elapsed)
	}
	for _, id := range fastIDs {
		completedAt, ok := store.completed[id]
		if !ok {
			t.Fatalf("agent %s was never saved", id)
		}
		if completedAt.Sub(start) > timeout/2 {
			t.Errorf("agent %s completed at +%v after start, want well before the stuck agent's %v timeout (it shouldn't have queued behind it)",
				id, completedAt.Sub(start), timeout)
		}
	}
}

// TestFlusherRecordsWriteMetricsForBothOps covers both flushOnce paths:
// SaveAgent (an agent still in the registry) and SoftDeleteAgent (one
// evicted before this flush ran).
func TestFlusherRecordsWriteMetricsForBothOps(t *testing.T) {
	dirty := NewDirtyTracker()
	store := &fakeStateStore{}
	registry := fleet.New(fleet.Config{HeartbeatInterval: time.Minute, StaleMissedHeartbeats: 3}, discardLogger(), dirty)

	_, raw := testInstanceUID(t, 1)
	registry.Report(&protobufs.AgentToServer{InstanceUid: raw}, fleet.ConnMeta{})
	dirty.AgentEvicted("gone")

	metrics := newFakeWriteMetrics()
	flusher := NewFlusher(registry, dirty, store, time.Hour, discardLogger(), 4, metrics, &fakeConnectionStore{}, "replica-1", "")
	flusher.flushOnce(context.Background())

	if got := metrics.durationCount("save_agent"); got != 1 {
		t.Errorf("durationCount(save_agent) = %d, want 1", got)
	}
	if got := metrics.durationCount("soft_delete_agent"); got != 1 {
		t.Errorf("durationCount(soft_delete_agent) = %d, want 1", got)
	}
	if got := metrics.durationCount("upsert_agent_connection"); got != 1 {
		t.Errorf("durationCount(upsert_agent_connection) = %d, want 1", got)
	}
}

// TestNewFlusherSetsWriteTimeoutOnce covers construction, not a flush: the
// configured timeout must be recorded once per op immediately, not lazily
// on first write.
func TestNewFlusherSetsWriteTimeoutOnce(t *testing.T) {
	metrics := newFakeWriteMetrics()
	registry := fleet.New(fleet.Config{HeartbeatInterval: time.Minute, StaleMissedHeartbeats: 3}, discardLogger(), nil)
	NewFlusher(registry, NewDirtyTracker(), &fakeStateStore{}, 7*time.Second, discardLogger(), 4, metrics, &fakeConnectionStore{}, "replica-1", "")

	if got := metrics.timeouts["save_agent"]; got != 7*time.Second {
		t.Errorf("timeouts[save_agent] = %v, want 7s", got)
	}
	if got := metrics.timeouts["soft_delete_agent"]; got != 7*time.Second {
		t.Errorf("timeouts[soft_delete_agent] = %v, want 7s", got)
	}
	if got := metrics.timeouts["upsert_agent_connection"]; got != 7*time.Second {
		t.Errorf("timeouts[upsert_agent_connection] = %v, want 7s", got)
	}
}

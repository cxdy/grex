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

	flusher := NewFlusher(registry, dirty, store, time.Hour, discardLogger(), 4, nil)
	flusher.flushOnce(context.Background())

	id, err := fleet.InstanceUID(uid)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.agents[id]; !ok {
		t.Fatalf("flush did not save agent %s; saved: %v", id, store.agents)
	}
}

func TestFlusherSoftDeletesAgentNoLongerInRegistry(t *testing.T) {
	dirty := NewDirtyTracker()
	store := &fakeStateStore{}
	registry := fleet.New(fleet.Config{HeartbeatInterval: time.Minute, StaleMissedHeartbeats: 3}, discardLogger(), nil)

	dirty.AgentEvicted("gone")
	flusher := NewFlusher(registry, dirty, store, time.Hour, discardLogger(), 4, nil)
	flusher.flushOnce(context.Background())

	if len(store.agents) != 0 {
		t.Fatalf("flush saved a full agent for one not in the registry: %v", store.agents)
	}
	if _, ok := store.evicted["gone"]; !ok {
		t.Fatal("flush did not soft-delete the evicted agent")
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

	flusher := NewFlusher(registry, dirty, store, time.Hour, discardLogger(), 4, nil)
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
	flusher := NewFlusher(registry, dirty, store, 20*time.Millisecond, discardLogger(), 4, nil)
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
// both SaveAgent and SoftDeleteAgent failures: it must log and continue,
// never panic or stop processing the rest of the dirty set.
func TestFlusherLogsStoreErrors(t *testing.T) {
	dirty := NewDirtyTracker()
	registry := fleet.New(fleet.Config{HeartbeatInterval: time.Minute, StaleMissedHeartbeats: 3}, discardLogger(), dirty)

	uid := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	registry.Report(&protobufs.AgentToServer{InstanceUid: uid}, fleet.ConnMeta{})
	dirty.AgentEvicted("gone") // not in the registry, exercises the SoftDeleteAgent path

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	flusher := NewFlusher(registry, dirty, erroringStateStore{}, time.Hour, log, 4, nil)
	flusher.flushOnce(context.Background()) // must not panic

	out := buf.String()
	if !strings.Contains(out, "persistence flush failed") || !strings.Contains(out, "save failed") {
		t.Errorf("log missing SaveAgent failure: %s", out)
	}
	if !strings.Contains(out, "persistence soft-delete failed") || !strings.Contains(out, "soft delete failed") {
		t.Errorf("log missing SoftDeleteAgent failure: %s", out)
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
	flusher := NewFlusher(registry, dirty, store, timeout, discardLogger(), 4, nil)

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
	flusher := NewFlusher(registry, dirty, store, time.Hour, discardLogger(), 4, metrics)
	flusher.flushOnce(context.Background())

	if got := metrics.durationCount("save_agent"); got != 1 {
		t.Errorf("durationCount(save_agent) = %d, want 1", got)
	}
	if got := metrics.durationCount("soft_delete_agent"); got != 1 {
		t.Errorf("durationCount(soft_delete_agent) = %d, want 1", got)
	}
}

// TestNewFlusherSetsWriteTimeoutOnce covers construction, not a flush: the
// configured timeout must be recorded once per op immediately, not lazily
// on first write.
func TestNewFlusherSetsWriteTimeoutOnce(t *testing.T) {
	metrics := newFakeWriteMetrics()
	registry := fleet.New(fleet.Config{HeartbeatInterval: time.Minute, StaleMissedHeartbeats: 3}, discardLogger(), nil)
	NewFlusher(registry, NewDirtyTracker(), &fakeStateStore{}, 7*time.Second, discardLogger(), 4, metrics)

	if got := metrics.timeouts["save_agent"]; got != 7*time.Second {
		t.Errorf("timeouts[save_agent] = %v, want 7s", got)
	}
	if got := metrics.timeouts["soft_delete_agent"]; got != 7*time.Second {
		t.Errorf("timeouts[soft_delete_agent] = %v, want 7s", got)
	}
}

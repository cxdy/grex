package persistence

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dennisme/grex/internal/fleet"
	"github.com/open-telemetry/opamp-go/protobufs"
)

// TestSessionSnapshotterWritesEveryRegisteredAgent covers the case
// DirtyTracker can't: a quiet, healthy agent sending heartbeats without a
// reportable field change never marks itself dirty again after its first
// report (see Report()'s event calls), so Flusher alone would never
// re-flush it. SessionSnapshotter's wholesale per-tick pass over every
// registered agent — regardless of dirty state — is what keeps
// agent_session current for that agent.
func TestSessionSnapshotterWritesEveryRegisteredAgent(t *testing.T) {
	store := &fakeStateStore{}
	registry := fleet.New(fleet.Config{HeartbeatInterval: time.Minute, StaleMissedHeartbeats: 3}, discardLogger(), nil)

	uid := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	registry.Report(&protobufs.AgentToServer{InstanceUid: uid}, fleet.ConnMeta{})

	snapshotter := NewSessionSnapshotter(registry, store, time.Hour, discardLogger(), 4, nil)
	snapshotter.snapshotOnce(context.Background())

	id, err := fleet.InstanceUID(uid)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.GetAgent(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("GetAgent: ok=%v err=%v", ok, err)
	}
	if got.SessionUpdatedAt.IsZero() {
		t.Error("SessionUpdatedAt is zero, want the snapshot to have written it")
	}
	if !got.Connected {
		t.Error("Connected = false, want true (the registry entry is connected)")
	}
}

// TestSessionSnapshotterSkipsNothingNotDirty proves the snapshot doesn't
// depend on DirtyTracker at all: an agent that was never marked dirty (no
// DirtyTracker even wired to this registry) still gets snapshotted, since
// SessionSnapshotter reads directly from the registry, not a dirty set.
func TestSessionSnapshotterSkipsNothingNotDirty(t *testing.T) {
	store := &fakeStateStore{}
	registry := fleet.New(fleet.Config{HeartbeatInterval: time.Minute, StaleMissedHeartbeats: 3}, discardLogger(), nil)

	uidA := []byte{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}
	uidB := []byte{2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2}
	registry.Report(&protobufs.AgentToServer{InstanceUid: uidA}, fleet.ConnMeta{})
	registry.Report(&protobufs.AgentToServer{InstanceUid: uidB}, fleet.ConnMeta{})

	snapshotter := NewSessionSnapshotter(registry, store, time.Hour, discardLogger(), 4, nil)
	snapshotter.snapshotOnce(context.Background())

	for _, uid := range [][]byte{uidA, uidB} {
		id, err := fleet.InstanceUID(uid)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := store.GetAgent(context.Background(), id); !ok {
			t.Errorf("agent %s not snapshotted", id)
		}
	}
}

// blockingSessionStore blocks SaveSession for one specific instance_uid
// until its context is done; every other instance_uid saves immediately
// and records when it completed.
type blockingSessionStore struct {
	stuckID string

	mu        sync.Mutex
	completed map[string]time.Time
}

func newBlockingSessionStore(stuckID string) *blockingSessionStore {
	return &blockingSessionStore{stuckID: stuckID, completed: make(map[string]time.Time)}
}

func (b *blockingSessionStore) SaveSession(ctx context.Context, agent fleet.Agent) error {
	if agent.InstanceUID == b.stuckID {
		<-ctx.Done()
		return ctx.Err()
	}
	b.mu.Lock()
	b.completed[agent.InstanceUID] = time.Now()
	b.mu.Unlock()
	return nil
}

func (b *blockingSessionStore) SaveAgent(context.Context, fleet.Agent) error {
	panic("not used by SessionSnapshotter")
}

func (b *blockingSessionStore) SoftDeleteAgent(context.Context, string, time.Time) error {
	panic("not used by SessionSnapshotter")
}

func (b *blockingSessionStore) GetAgent(context.Context, string) (fleet.Agent, bool, error) {
	panic("not used by SessionSnapshotter")
}

func (b *blockingSessionStore) ListAgents(context.Context) ([]fleet.Agent, error) {
	panic("not used by SessionSnapshotter")
}

func (b *blockingSessionStore) DeleteAgent(context.Context, string) error {
	panic("not used by SessionSnapshotter")
}

var _ StateStore = (*blockingSessionStore)(nil)

// TestSessionSnapshotterStuckAgentDoesNotBlockOthers is SessionSnapshotter's
// version of Flusher's identical property: one stuck SaveSession call must
// not delay the other agents in the same snapshotOnce batch.
func TestSessionSnapshotterStuckAgentDoesNotBlockOthers(t *testing.T) {
	registry := fleet.New(fleet.Config{HeartbeatInterval: time.Minute, StaleMissedHeartbeats: 3}, discardLogger(), nil)

	stuckID, stuckRaw := testInstanceUID(t, 1)
	registry.Report(&protobufs.AgentToServer{InstanceUid: stuckRaw}, fleet.ConnMeta{})
	var fastIDs []string
	for n := byte(2); n <= 6; n++ {
		id, raw := testInstanceUID(t, n)
		registry.Report(&protobufs.AgentToServer{InstanceUid: raw}, fleet.ConnMeta{})
		fastIDs = append(fastIDs, id)
	}

	store := newBlockingSessionStore(stuckID)
	timeout := 200 * time.Millisecond
	snapshotter := NewSessionSnapshotter(registry, store, timeout, discardLogger(), 4, nil)

	start := time.Now()
	snapshotter.snapshotOnce(context.Background())
	elapsed := time.Since(start)

	if elapsed < timeout {
		t.Fatalf("snapshotOnce returned before the stuck write's %v timeout: elapsed=%v", timeout, elapsed)
	}
	for _, id := range fastIDs {
		completedAt, ok := store.completed[id]
		if !ok {
			t.Fatalf("agent %s was never snapshotted", id)
		}
		if completedAt.Sub(start) > timeout/2 {
			t.Errorf("agent %s completed at +%v after start, want well before the stuck agent's %v timeout",
				id, completedAt.Sub(start), timeout)
		}
	}
}

// TestSessionSnapshotterRunDoesFinalSnapshotOnCancel is
// TestFlusherRunDoesFinalFlushOnCancel's counterpart for
// SessionSnapshotter's own Run loop.
func TestSessionSnapshotterRunDoesFinalSnapshotOnCancel(t *testing.T) {
	store := &fakeStateStore{}
	registry := fleet.New(fleet.Config{HeartbeatInterval: time.Minute, StaleMissedHeartbeats: 3}, discardLogger(), nil)

	uid := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	registry.Report(&protobufs.AgentToServer{InstanceUid: uid}, fleet.ConnMeta{})
	id, err := fleet.InstanceUID(uid)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	snapshotter := NewSessionSnapshotter(registry, store, time.Hour, discardLogger(), 4, nil)
	done := make(chan struct{})
	go func() {
		snapshotter.Run(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}

	if _, ok, _ := store.GetAgent(context.Background(), id); !ok {
		t.Fatal("Run did not do a final snapshot on cancellation")
	}
}

// TestSessionSnapshotterBatchesSaveSession covers docs/spec/design.md's
// Scaling gaps items 3-4: when the store supports BatchStateStore, every
// registered agent's session write goes through one chunked pgx.Batch
// round trip instead of one SaveSession call per agent.
// fakeBatchStore.SaveSession panics — if snapshotOnce ever fell back to the
// per-row path here, this test would panic instead of just failing an
// assertion.
func TestSessionSnapshotterBatchesSaveSession(t *testing.T) {
	store := &fakeBatchStore{}
	registry := fleet.New(fleet.Config{HeartbeatInterval: time.Minute, StaleMissedHeartbeats: 3}, discardLogger(), nil)

	var ids []string
	for n := byte(1); n <= 3; n++ {
		id, raw := testInstanceUID(t, n)
		registry.Report(&protobufs.AgentToServer{InstanceUid: raw}, fleet.ConnMeta{})
		ids = append(ids, id)
	}

	snapshotter := NewSessionSnapshotter(registry, store, time.Hour, discardLogger(), 4, nil)
	snapshotter.snapshotOnce(context.Background())

	for _, id := range ids {
		if !store.savedSession(id) {
			t.Errorf("%s was not snapshotted", id)
		}
	}
	if got := store.sendBatchCalls(); got != 1 {
		t.Errorf("sendBatchCalls = %d, want 1 (all three fit in one chunk)", got)
	}
}

// TestSessionSnapshotterBatchChunkErrorDoesNotStopRestOfChunk is
// SessionSnapshotter's version of Flusher's identical proof: one bad row's
// error must not prevent the rest of that chunk's results from being read
// and applied.
func TestSessionSnapshotterBatchChunkErrorDoesNotStopRestOfChunk(t *testing.T) {
	failingID, failingRaw := testInstanceUID(t, 2)
	store := &fakeBatchStore{errFor: map[string]error{failingID: errors.New("constraint violation")}}
	registry := fleet.New(fleet.Config{HeartbeatInterval: time.Minute, StaleMissedHeartbeats: 3}, discardLogger(), nil)

	var okIDs []string
	for n := byte(1); n <= 3; n++ {
		id, raw := testInstanceUID(t, n)
		if n == 2 {
			registry.Report(&protobufs.AgentToServer{InstanceUid: failingRaw}, fleet.ConnMeta{})
			continue
		}
		registry.Report(&protobufs.AgentToServer{InstanceUid: raw}, fleet.ConnMeta{})
		okIDs = append(okIDs, id)
	}

	snapshotter := NewSessionSnapshotter(registry, store, time.Hour, discardLogger(), 4, nil)
	snapshotter.snapshotOnce(context.Background())

	for _, id := range okIDs {
		if !store.savedSession(id) {
			t.Errorf("%s was not snapshotted despite a different row in its chunk failing", id)
		}
	}
	if store.savedSession(failingID) {
		t.Errorf("%s should have failed, not been recorded as saved", failingID)
	}
}

// TestSessionSnapshotterRecordsWriteMetrics covers the metrics wiring.
func TestSessionSnapshotterRecordsWriteMetrics(t *testing.T) {
	store := &fakeStateStore{}
	registry := fleet.New(fleet.Config{HeartbeatInterval: time.Minute, StaleMissedHeartbeats: 3}, discardLogger(), nil)
	_, raw := testInstanceUID(t, 1)
	registry.Report(&protobufs.AgentToServer{InstanceUid: raw}, fleet.ConnMeta{})

	metrics := newFakeWriteMetrics()
	snapshotter := NewSessionSnapshotter(registry, store, time.Hour, discardLogger(), 4, metrics)
	snapshotter.snapshotOnce(context.Background())

	if got := metrics.durationCount("save_session"); got != 1 {
		t.Errorf("durationCount(save_session) = %d, want 1", got)
	}
}

// TestNewSessionSnapshotterSetsWriteTimeoutOnce covers construction: the
// configured timeout is recorded once immediately, not lazily on first
// write.
func TestNewSessionSnapshotterSetsWriteTimeoutOnce(t *testing.T) {
	metrics := newFakeWriteMetrics()
	registry := fleet.New(fleet.Config{HeartbeatInterval: time.Minute, StaleMissedHeartbeats: 3}, discardLogger(), nil)
	NewSessionSnapshotter(registry, &fakeStateStore{}, 7*time.Second, discardLogger(), 4, metrics)

	if got := metrics.timeouts["save_session"]; got != 7*time.Second {
		t.Errorf("timeouts[save_session] = %v, want 7s", got)
	}
}

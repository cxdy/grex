package persistence

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/dennisme/grex/internal/fleet"
	"github.com/open-telemetry/opamp-go/protobufs"
)

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

	flusher := NewFlusher(registry, dirty, store, time.Hour, discardLogger())
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
	flusher := NewFlusher(registry, dirty, store, time.Hour, discardLogger())
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

	flusher := NewFlusher(registry, dirty, store, time.Hour, discardLogger())
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
	flusher := NewFlusher(registry, dirty, store, 20*time.Millisecond, discardLogger())
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
	flusher := NewFlusher(registry, dirty, erroringStateStore{}, time.Hour, log)
	flusher.flushOnce(context.Background()) // must not panic

	out := buf.String()
	if !strings.Contains(out, "persistence flush failed") || !strings.Contains(out, "save failed") {
		t.Errorf("log missing SaveAgent failure: %s", out)
	}
	if !strings.Contains(out, "persistence soft-delete failed") || !strings.Contains(out, "soft delete failed") {
		t.Errorf("log missing SoftDeleteAgent failure: %s", out)
	}
}

package persistence

import (
	"context"
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

	snapshotter := NewSessionSnapshotter(registry, store, time.Hour, discardLogger())
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

	snapshotter := NewSessionSnapshotter(registry, store, time.Hour, discardLogger())
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

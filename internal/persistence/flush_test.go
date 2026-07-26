package persistence

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/dennisme/grex/internal/fleet"
	"github.com/open-telemetry/opamp-go/protobufs"
)

func TestDirtyTrackerDrain(t *testing.T) {
	d := NewDirtyTracker()
	if got := d.Drain(); got != nil {
		t.Fatalf("Drain on empty tracker = %v, want nil", got)
	}

	d.AgentConnected("agent-1")
	d.ReportReceived("agent-2", "status")
	d.AgentConnected("agent-1") // duplicate, still one entry

	got := d.Drain()
	want := map[string]bool{"agent-1": true, "agent-2": true}
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

func TestFlusherSkipsAgentNoLongerInRegistry(t *testing.T) {
	dirty := NewDirtyTracker()
	store := &fakeStateStore{}
	registry := fleet.New(fleet.Config{HeartbeatInterval: time.Minute, StaleMissedHeartbeats: 3}, discardLogger(), nil)

	dirty.AgentEvicted("gone")
	flusher := NewFlusher(registry, dirty, store, time.Hour, discardLogger())
	flusher.flushOnce(context.Background())

	if len(store.agents) != 0 {
		t.Fatalf("flush saved something for an agent not in the registry: %v", store.agents)
	}
}

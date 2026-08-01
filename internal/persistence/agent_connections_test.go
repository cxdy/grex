package persistence

import (
	"context"
	"testing"
	"time"
)

func TestUpsertAgentConnectionStoresReplicaLabel(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	err := store.UpsertAgentConnection(ctx, AgentConnection{
		InstanceUID:  "agent-1",
		ReplicaID:    "3f9e2b1a-uuid",
		ReplicaLabel: "grex-7d9f8c6b5-xk2pl",
		ConnectedAt:  now,
		LastSeen:     now,
	})
	if err != nil {
		t.Fatalf("UpsertAgentConnection: %v", err)
	}

	got, ok, err := store.GetAgentConnection(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetAgentConnection: %v", err)
	}
	if !ok {
		t.Fatal("GetAgentConnection: want ok = true")
	}
	if got.ReplicaLabel != "grex-7d9f8c6b5-xk2pl" {
		t.Errorf("ReplicaLabel = %q, want grex-7d9f8c6b5-xk2pl", got.ReplicaLabel)
	}

	// ReplicaLabel is debug-only: it must not affect routing/uniqueness, so
	// a second upsert for the same instance_uid still overwrites in place
	// (same as replica_id moving owners).
	if err := store.UpsertAgentConnection(ctx, AgentConnection{
		InstanceUID: "agent-1", ReplicaID: "3f9e2b1a-uuid", ReplicaLabel: "", ConnectedAt: now, LastSeen: now,
	}); err != nil {
		t.Fatalf("UpsertAgentConnection (empty label): %v", err)
	}
	got, _, err = store.GetAgentConnection(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetAgentConnection: %v", err)
	}
	if got.ReplicaLabel != "" {
		t.Errorf("ReplicaLabel = %q, want empty after overwrite", got.ReplicaLabel)
	}
}

// TestUpsertAgentConnectionPreservesConnectedAtForSameReplica covers a
// repeat upsert from the same owning replica (a Flusher refreshing
// last_seen on every flush tick while an agent stays connected): connected_
// at must stay the original value, not reset to "now" on every refresh —
// only an actual ownership change (a different ReplicaID) should move it.
func TestUpsertAgentConnectionPreservesConnectedAtForSameReplica(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Microsecond)
	t1 := t0.Add(time.Minute)

	if err := store.UpsertAgentConnection(ctx, AgentConnection{
		InstanceUID: "agent-1", ReplicaID: "grex-1", ConnectedAt: t0, LastSeen: t0,
	}); err != nil {
		t.Fatalf("UpsertAgentConnection: %v", err)
	}
	// Same replica upserts again later (a refresh tick), passing "now" as
	// ConnectedAt just like a Flusher would — connected_at must not move.
	if err := store.UpsertAgentConnection(ctx, AgentConnection{
		InstanceUID: "agent-1", ReplicaID: "grex-1", ConnectedAt: t1, LastSeen: t1,
	}); err != nil {
		t.Fatalf("UpsertAgentConnection (refresh): %v", err)
	}

	got, ok, err := store.GetAgentConnection(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetAgentConnection: %v", err)
	}
	if !ok {
		t.Fatal("GetAgentConnection: want ok = true")
	}
	if !got.ConnectedAt.Equal(t0) {
		t.Errorf("ConnectedAt = %v, want %v (unchanged, same replica)", got.ConnectedAt, t0)
	}
	if !got.LastSeen.Equal(t1) {
		t.Errorf("LastSeen = %v, want %v (refreshed)", got.LastSeen, t1)
	}
}

func TestUpsertAndGetAgentConnection(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	err := store.UpsertAgentConnection(ctx, AgentConnection{
		InstanceUID: "agent-1",
		ReplicaID:   "grex-1",
		ConnectedAt: now,
		LastSeen:    now,
	})
	if err != nil {
		t.Fatalf("UpsertAgentConnection: %v", err)
	}

	got, ok, err := store.GetAgentConnection(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetAgentConnection: %v", err)
	}
	if !ok {
		t.Fatal("GetAgentConnection: want ok = true")
	}
	if got.ReplicaID != "grex-1" || !got.ConnectedAt.Equal(now) || !got.LastSeen.Equal(now) {
		t.Errorf("GetAgentConnection = %+v, want replica grex-1 at %v", got, now)
	}
}

func TestUpsertAgentConnectionMovesOwner(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Microsecond)
	t1 := t0.Add(time.Minute)

	if err := store.UpsertAgentConnection(ctx, AgentConnection{
		InstanceUID: "agent-1", ReplicaID: "grex-1", ConnectedAt: t0, LastSeen: t0,
	}); err != nil {
		t.Fatalf("UpsertAgentConnection: %v", err)
	}
	// Same instance_uid reconnects to a different replica — this is the
	// exact HA handoff case (see docs/spec/design.md's Dispatch routing
	// section): the owner must move, not accumulate a second row.
	if err := store.UpsertAgentConnection(ctx, AgentConnection{
		InstanceUID: "agent-1", ReplicaID: "grex-2", ConnectedAt: t1, LastSeen: t1,
	}); err != nil {
		t.Fatalf("UpsertAgentConnection (move): %v", err)
	}

	got, ok, err := store.GetAgentConnection(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetAgentConnection: %v", err)
	}
	if !ok {
		t.Fatal("GetAgentConnection: want ok = true")
	}
	if got.ReplicaID != "grex-2" {
		t.Errorf("ReplicaID = %q, want grex-2 (the new owner)", got.ReplicaID)
	}

	conns, err := store.ListAgentConnections(ctx)
	if err != nil {
		t.Fatalf("ListAgentConnections: %v", err)
	}
	if len(conns) != 1 {
		t.Fatalf("ListAgentConnections returned %d rows, want 1 (upsert, not insert)", len(conns))
	}
}

func TestGetAgentConnectionNotFound(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, ok, err := store.GetAgentConnection(ctx, "no-such-agent")
	if err != nil {
		t.Fatalf("GetAgentConnection: %v", err)
	}
	if ok {
		t.Fatal("GetAgentConnection: want ok = false for a nonexistent instance_uid")
	}
}

func TestDeleteAgentConnection(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	if err := store.UpsertAgentConnection(ctx, AgentConnection{
		InstanceUID: "agent-1", ReplicaID: "grex-1", ConnectedAt: now, LastSeen: now,
	}); err != nil {
		t.Fatalf("UpsertAgentConnection: %v", err)
	}
	if err := store.DeleteAgentConnection(ctx, "agent-1"); err != nil {
		t.Fatalf("DeleteAgentConnection: %v", err)
	}

	_, ok, err := store.GetAgentConnection(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetAgentConnection: %v", err)
	}
	if ok {
		t.Fatal("GetAgentConnection: want ok = false after DeleteAgentConnection")
	}
}

func TestDeleteAgentConnectionMissingIsNotAnError(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if err := store.DeleteAgentConnection(ctx, "no-such-agent"); err != nil {
		t.Fatalf("DeleteAgentConnection on a missing row: %v", err)
	}
}

func TestAgentConnectionContextCanceled(t *testing.T) {
	store := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := store.UpsertAgentConnection(ctx, AgentConnection{InstanceUID: "agent-1"}); err == nil {
		t.Fatal("UpsertAgentConnection: want error for a cancelled context")
	}
	if _, _, err := store.GetAgentConnection(ctx, "agent-1"); err == nil {
		t.Fatal("GetAgentConnection: want error for a cancelled context")
	}
	if _, err := store.ListAgentConnections(ctx); err == nil {
		t.Fatal("ListAgentConnections: want error for a cancelled context")
	}
	if err := store.DeleteAgentConnection(ctx, "agent-1"); err == nil {
		t.Fatal("DeleteAgentConnection: want error for a cancelled context")
	}
}

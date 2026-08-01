package persistence

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/dennisme/grex/internal/fleet"
)

// fakeStateStore is a minimal in-memory stand-in used only to prove
// StateStore's shape is implementable. It is not a real backend and must
// never be wired into grex's runtime. Guarded by a mutex since Flusher's
// own tests exercise it from a background goroutine (Run) alongside the
// test goroutine.
type fakeStateStore struct {
	mu      sync.Mutex
	agents  map[string]fleet.Agent
	evicted map[string]time.Time
}

var _ StateStore = (*fakeStateStore)(nil)

func (f *fakeStateStore) SaveAgent(_ context.Context, agent fleet.Agent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.agents == nil {
		f.agents = make(map[string]fleet.Agent)
	}
	f.agents[agent.InstanceUID] = agent
	return nil
}

func (f *fakeStateStore) SaveSession(_ context.Context, agent fleet.Agent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.agents == nil {
		f.agents = make(map[string]fleet.Agent)
	}
	existing := f.agents[agent.InstanceUID]
	existing.Connected = agent.Connected
	existing.Conn = agent.Conn
	existing.DescriptionReported = agent.DescriptionReported
	existing.SequenceNum = agent.SequenceNum
	existing.SessionUpdatedAt = agent.LastSeen
	if existing.InstanceUID == "" {
		existing.InstanceUID = agent.InstanceUID
	}
	f.agents[agent.InstanceUID] = existing
	return nil
}

func (f *fakeStateStore) GetAgent(_ context.Context, instanceUID string) (fleet.Agent, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	agent, ok := f.agents[instanceUID]
	return agent, ok, nil
}

func (f *fakeStateStore) ListAgents(_ context.Context) ([]fleet.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := make([]fleet.Agent, 0, len(f.agents))
	for _, agent := range f.agents {
		list = append(list, agent)
	}
	return list, nil
}

func (f *fakeStateStore) DeleteAgent(_ context.Context, instanceUID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.agents, instanceUID)
	return nil
}

func (f *fakeStateStore) SoftDeleteAgent(_ context.Context, instanceUID string, evictedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.evicted == nil {
		f.evicted = make(map[string]time.Time)
	}
	if _, ok := f.evicted[instanceUID]; ok {
		return nil // idempotent: already soft-deleted, evictedAt doesn't move
	}
	f.evicted[instanceUID] = evictedAt
	return nil
}

// hasAgent reports whether instanceUID has been saved, safe for concurrent
// use (see fakeStateStore's doc comment).
func (f *fakeStateStore) hasAgent(instanceUID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.agents[instanceUID]
	return ok
}

func TestStateStoreShape(t *testing.T) {
	ctx := context.Background()
	store := &fakeStateStore{}

	if err := store.SaveAgent(ctx, fleet.Agent{InstanceUID: "agent-1"}); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
	agent, ok, err := store.GetAgent(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if !ok || agent.InstanceUID != "agent-1" {
		t.Fatalf("GetAgent = %+v, %v, want agent-1, true", agent, ok)
	}
	if list, err := store.ListAgents(ctx); err != nil || len(list) != 1 {
		t.Fatalf("ListAgents = %v, %v, want 1 agent", list, err)
	}
	if err := store.DeleteAgent(ctx, "agent-1"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	if _, ok, err := store.GetAgent(ctx, "agent-1"); err != nil || ok {
		t.Fatalf("GetAgent after delete = ok:%v err:%v, want ok:false", ok, err)
	}

	t0 := time.Now()
	if err := store.SoftDeleteAgent(ctx, "agent-2", t0); err != nil {
		t.Fatalf("SoftDeleteAgent: %v", err)
	}
	if err := store.SoftDeleteAgent(ctx, "agent-2", t0.Add(time.Hour)); err != nil {
		t.Fatalf("SoftDeleteAgent (again): %v", err)
	}
	if got := store.evicted["agent-2"]; !got.Equal(t0) {
		t.Errorf("evictedAt = %v, want %v (idempotent, first call wins)", got, t0)
	}
}

// fakeConnectionStore is a minimal in-memory stand-in for ConnectionStore,
// used by Flusher's tests to assert which instance_uid/replica_id pairs it
// upserted without hitting real Postgres.
type fakeConnectionStore struct {
	mu    sync.Mutex
	conns map[string]AgentConnection
}

var _ ConnectionStore = (*fakeConnectionStore)(nil)

func (f *fakeConnectionStore) UpsertAgentConnection(_ context.Context, conn AgentConnection) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.conns == nil {
		f.conns = make(map[string]AgentConnection)
	}
	f.conns[conn.InstanceUID] = conn
	return nil
}

func (f *fakeConnectionStore) GetAgentConnection(_ context.Context, instanceUID string) (AgentConnection, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	conn, ok := f.conns[instanceUID]
	return conn, ok, nil
}

func (f *fakeConnectionStore) ListAgentConnections(_ context.Context) ([]AgentConnection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := make([]AgentConnection, 0, len(f.conns))
	for _, conn := range f.conns {
		list = append(list, conn)
	}
	return list, nil
}

func (f *fakeConnectionStore) DeleteAgentConnection(_ context.Context, instanceUID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.conns, instanceUID)
	return nil
}

// hasConnection reports whether instanceUID has been upserted, safe for
// concurrent use (mirrors fakeStateStore.hasAgent).
func (f *fakeConnectionStore) hasConnection(instanceUID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.conns[instanceUID]
	return ok
}

// JobQueue and PermissionStore are implemented by PostgresStore and tested
// against real Postgres in jobs_test.go and permissions_test.go.
// ConnectionStore is tested the same way in agent_connections_test.go, and
// its Flusher-side caller is tested against fakeConnectionStore in
// flush_test.go.

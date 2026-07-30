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

// fakeJobQueue is a minimal in-memory stand-in used only to prove JobQueue's
// shape is implementable. It is not a real dispatcher.
type fakeJobQueue struct {
	targets map[string][]JobTarget
}

var _ JobQueue = (*fakeJobQueue)(nil)

func (f *fakeJobQueue) InsertJob(_ context.Context, job Job, targets []JobTarget) error {
	if f.targets == nil {
		f.targets = make(map[string][]JobTarget)
	}
	f.targets[job.ID] = targets
	return nil
}

func (f *fakeJobQueue) ListJobTargets(_ context.Context, jobID string) ([]JobTarget, error) {
	return f.targets[jobID], nil
}

func TestJobQueueShape(t *testing.T) {
	ctx := context.Background()
	queue := &fakeJobQueue{}

	job := Job{ID: "job-1", Filter: "service.name=otelcol-contrib", Action: "restart"}
	targets := []JobTarget{{JobID: "job-1", InstanceUID: "agent-1", Status: "pending"}}
	if err := queue.InsertJob(ctx, job, targets); err != nil {
		t.Fatalf("InsertJob: %v", err)
	}
	got, err := queue.ListJobTargets(ctx, "job-1")
	if err != nil {
		t.Fatalf("ListJobTargets: %v", err)
	}
	if len(got) != 1 || got[0].InstanceUID != "agent-1" {
		t.Fatalf("ListJobTargets = %+v, want one target for agent-1", got)
	}
}

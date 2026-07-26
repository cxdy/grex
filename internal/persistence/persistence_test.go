package persistence

import (
	"context"
	"testing"

	"github.com/dennisme/grex/internal/fleet"
)

// fakeStateStore is a minimal in-memory stand-in used only to prove
// StateStore's shape is implementable. It is not a real backend and must
// never be wired into grex's runtime.
type fakeStateStore struct {
	agents map[string]fleet.Agent
}

var _ StateStore = (*fakeStateStore)(nil)

func (f *fakeStateStore) SaveAgent(_ context.Context, agent fleet.Agent) error {
	if f.agents == nil {
		f.agents = make(map[string]fleet.Agent)
	}
	f.agents[agent.InstanceUID] = agent
	return nil
}

func (f *fakeStateStore) GetAgent(_ context.Context, instanceUID string) (fleet.Agent, bool, error) {
	agent, ok := f.agents[instanceUID]
	return agent, ok, nil
}

func (f *fakeStateStore) ListAgents(_ context.Context) ([]fleet.Agent, error) {
	list := make([]fleet.Agent, 0, len(f.agents))
	for _, agent := range f.agents {
		list = append(list, agent)
	}
	return list, nil
}

func (f *fakeStateStore) DeleteAgent(_ context.Context, instanceUID string) error {
	delete(f.agents, instanceUID)
	return nil
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

// Package persistence defines the interfaces a future durable backend for
// fleet state and job dispatch will implement, per docs/spec/design.md's
// Post-1.0 roadmap ("state database and sharding", "Jobs: schema and
// execution"). Nothing implements or calls these interfaces yet;
// internal/fleet.Registry remains the sole source of fleet state in 1.0.
package persistence

import (
	"context"

	"github.com/dennisme/grex/internal/fleet"
)

// StateStore is the future durable counterpart to fleet.Registry: a backend
// that can persist agent state and reload it across a grex restart.
type StateStore interface {
	SaveAgent(ctx context.Context, agent fleet.Agent) error
	GetAgent(ctx context.Context, instanceUID string) (fleet.Agent, bool, error)
	ListAgents(ctx context.Context) ([]fleet.Agent, error)
	DeleteAgent(ctx context.Context, instanceUID string) error
}

// Job is one user-submitted mutation intent: a target filter (the same
// filter language GET /api/agents uses) and an action to perform. It
// expands to one JobTarget per matched agent.
type Job struct {
	ID     string
	Filter string
	Action string
}

// JobTarget is one agent's dispatch attempt within a Job.
type JobTarget struct {
	JobID       string
	InstanceUID string
	Status      string
}

// JobQueue is the future dispatch surface for mutation jobs, shaped after
// River's per-target Insert model: one JobTarget becomes one queued dispatch
// attempt, retried independently of the others.
type JobQueue interface {
	InsertJob(ctx context.Context, job Job, targets []JobTarget) error
	ListJobTargets(ctx context.Context, jobID string) ([]JobTarget, error)
}

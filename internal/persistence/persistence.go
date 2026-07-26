// Package persistence is the durable counterpart to internal/fleet.Registry:
// PostgresStore implements StateStore, and Flusher keeps it current from a
// Registry's Events notifications. internal/fleet.Registry remains the
// runtime source of truth for live fleet state; nothing reads from
// StateStore anywhere in grex's own runtime yet (see docs/spec/design.md's
// Post-1.0 roadmap). JobQueue is still just an interface shape, per the
// "Jobs: schema and execution" section — nothing implements or calls it.
package persistence

import (
	"context"
	"time"

	"github.com/dennisme/grex/internal/fleet"
)

// StateStore is the durable counterpart to fleet.Registry: a backend that
// can persist agent state and reload it across a grex restart.
type StateStore interface {
	SaveAgent(ctx context.Context, agent fleet.Agent) error
	GetAgent(ctx context.Context, instanceUID string) (fleet.Agent, bool, error)
	ListAgents(ctx context.Context) ([]fleet.Agent, error)
	DeleteAgent(ctx context.Context, instanceUID string) error
	// SoftDeleteAgent marks an agent evicted from fleet.Registry as gone,
	// without removing its row: evictedAt is recorded so a caller can still
	// see "last seen, agent gone" history for fleet.soft_delete_duration
	// before a periodic purge job removes it outright. Idempotent: calling
	// it again for an already-soft-deleted agent leaves its evictedAt
	// unchanged.
	SoftDeleteAgent(ctx context.Context, instanceUID string, evictedAt time.Time) error
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

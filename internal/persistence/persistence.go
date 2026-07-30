// Package persistence is the durable counterpart to internal/fleet.Registry:
// PostgresStore implements StateStore, Flusher keeps the agents/
// agent_effective_config tables current from a Registry's dirty-tracked
// Events notifications, and SessionSnapshotter independently keeps
// agent_session current on its own wholesale cadence (see docs/spec/
// design.md's Agent state schema). internal/fleet.Registry remains the
// runtime source of truth for live fleet state; internal/api and
// internal/ui fall back to StateStore for an agent this replica doesn't
// hold locally. JobQueue is still just an interface shape, per the
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
	// SaveSession writes only agent_session (connected, remote_addr,
	// tls_subject, via_gateway, transport, description_reported,
	// sequence_num), independent of the agents table. Used by
	// SessionSnapshotter's wholesale per-tick pass over every registered
	// agent, deliberately not routed through SaveAgent: agent_session needs
	// refreshing on every tick regardless of whether identity/health data
	// changed, and paying SaveAgent's full JSONB-column rewrite cost for that
	// would scale badly with fleet size. Guarded the same way as SaveAgent,
	// keyed on agent.LastSeen.
	SaveSession(ctx context.Context, agent fleet.Agent) error
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

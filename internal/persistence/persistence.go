// Package persistence is the durable counterpart to internal/fleet.Registry:
// PostgresStore implements StateStore, Flusher keeps the agents/
// agent_effective_config tables current from a Registry's dirty-tracked
// Events notifications, and SessionSnapshotter independently keeps
// agent_session current on its own wholesale cadence (see docs/spec/
// design.md's Agent state schema). internal/fleet.Registry remains the
// runtime source of truth for live fleet state; internal/api and
// internal/ui fall back to StateStore for an agent this replica doesn't
// hold locally.
//
// PostgresStore also implements PermissionStore, ConnectionStore, and
// JobQueue below — schema and CRUD only. Nothing in grex calls any of these
// yet: the role lookup, the OpAMP connect/disconnect wiring, and the
// River-backed dispatch/arm logic are all separate, not-yet-built follow-up
// work (see docs/spec/design.md's Permission table schema and "Jobs: schema
// and execution" sections).
package persistence

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

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

// BatchStateStore is an optional capability: a StateStore that can queue
// several SaveSession/SoftDeleteAgent-equivalent writes onto one pgx.Batch
// round trip instead of one round trip per agent (see docs/spec/design.md's
// Scaling gaps items 3-4). PostgresStore satisfies it. Flusher and
// SessionSnapshotter type-assert their configured store against this and
// use the chunked-batch path only when it succeeds — a store that doesn't
// implement it (test fakes, chiefly) just means those callers fall back to
// their original one-write-per-agent path, unchanged.
//
// Deliberately not folded into StateStore itself: StateStore is also used
// by internal/api and internal/ui as a storage-agnostic read fallback, and
// those packages have no reason to depend on pgx.
type BatchStateStore interface {
	QueueSaveSession(batch *pgx.Batch, agent fleet.Agent)
	QueueSoftDeleteAgent(batch *pgx.Batch, instanceUID string, evictedAt time.Time)
	SendBatch(ctx context.Context, batch *pgx.Batch) pgx.BatchResults
}

// RoleMapping is one row of the flat identity-to-role table, per
// docs/spec/design.md's Permission table schema. Role set stays exactly
// "viewer"/"admin"; both still read-only until Jobs is the first place they
// actually differ in behavior.
type RoleMapping struct {
	ID            int64
	IdentityKind  string // "spiffe" | "oidc_group"
	IdentityValue string
	Match         string // "exact" | "prefix"
	Role          string // "viewer" | "admin"
	TenantID      string // unused until multi-tenancy exists
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// PermissionStore is the durable counterpart to today's static
// auth.role_mapping config. Matching an identity against these rows (exact
// vs prefix, resolving which role applies) is a separate, not-yet-built
// concern layered on top of this CRUD surface.
type PermissionStore interface {
	CreateRoleMapping(ctx context.Context, m RoleMapping) (RoleMapping, error)
	GetRoleMapping(ctx context.Context, id int64) (RoleMapping, bool, error)
	ListRoleMappings(ctx context.Context) ([]RoleMapping, error)
}

// AgentConnection records which grex replica currently holds a given
// agent's live OpAMP socket, per docs/spec/design.md's "Dispatch routing:
// agent_connections and cross-replica handoff". Registering/deregistering
// this from the actual OpAMP connect/disconnect path, and the lease-expiry
// sweep that ages out a crashed replica's stale rows, are both separate,
// not-yet-built follow-up work.
type AgentConnection struct {
	InstanceUID string
	ReplicaID   string
	// ReplicaLabel is a human-readable identity (pod name/hostname) for
	// debugging only. It is never used for routing or uniqueness — see
	// ReplicaID for why pod/host names are the wrong key for that.
	ReplicaLabel string
	ConnectedAt  time.Time
	LastSeen     time.Time
}

// ConnectionStore is the CRUD surface a future job dispatcher uses to find
// which replica currently owns a target agent's socket.
type ConnectionStore interface {
	UpsertAgentConnection(ctx context.Context, conn AgentConnection) error
	GetAgentConnection(ctx context.Context, instanceUID string) (AgentConnection, bool, error)
	ListAgentConnections(ctx context.Context) ([]AgentConnection, error)
	DeleteAgentConnection(ctx context.Context, instanceUID string) error
}

// BatchConnectionStore is ConnectionStore's equivalent of BatchStateStore:
// an optional capability for queueing UpsertAgentConnection onto a shared
// pgx.Batch. See BatchStateStore's doc comment. Carries its own SendBatch
// (duplicated from BatchStateStore's) rather than assuming a caller's store
// and connStore share one underlying connection pool — true of every
// PostgresStore today, but not a coupling this interface should bake in.
type BatchConnectionStore interface {
	QueueUpsertAgentConnection(batch *pgx.Batch, conn AgentConnection)
	SendBatch(ctx context.Context, batch *pgx.Batch) pgx.BatchResults
}

// Job is one user-submitted mutation intent, per docs/spec/design.md's
// "Jobs: schema and execution": a target filter (the same filter language
// GET /api/agents uses), an action to perform, and action-specific config
// (restart's reconnect timeout/backoff cap, for one). CreateJob always
// creates it in StatusPlanned with no JobTargets yet — those are
// materialized later, at arm time, not at creation (see
// JobQueue.CreateJobTargets). TargetMode is likewise an arm-time choice
// (recompute vs freeze), so it stays nil until the not-yet-built arm step
// sets it.
type Job struct {
	ID           string
	Filter       string
	Action       string
	ActionConfig json.RawMessage // action-specific knobs; {} if none
	Status       string          // planned | armed | cancelled | dispatched
	TargetMode   *string         // recompute | freeze; nil until armed
	SubmittedBy  string
	CreatedAt    time.Time
	ArmedAt      *time.Time
	DispatchAt   *time.Time
	CancelledAt  *time.Time
}

// Job.Status values.
const (
	JobStatusPlanned    = "planned"
	JobStatusArmed      = "armed"
	JobStatusCancelled  = "cancelled"
	JobStatusDispatched = "dispatched"
)

// Job.TargetMode values.
const (
	JobTargetModeRecompute = "recompute"
	JobTargetModeFreeze    = "freeze"
)

// JobTarget is one agent's dispatch attempt within a Job, keyed on
// InstanceUID rather than a foreign key into agents: this row is a
// historical dispatch record that must survive the agent's eventual
// hard-delete (purge), not disappear with it.
type JobTarget struct {
	ID          int64
	JobID       string
	InstanceUID string
	Status      string // pending | sent | send_failed | applied | failed | rejected
	// Reason is set only when Status is JobTargetStatusRejected — why an
	// arm-time gate excluded this target (e.g. not supervisor_managed), per
	// docs/spec/design.md's "Decided: per-target rejection with a reason".
	Reason       *string
	DispatchedAt *time.Time
	CompletedAt  *time.Time
}

// JobTarget.Status values.
const (
	JobTargetStatusPending    = "pending"
	JobTargetStatusSent       = "sent"
	JobTargetStatusSendFailed = "send_failed"
	JobTargetStatusApplied    = "applied"
	JobTargetStatusFailed     = "failed"
	JobTargetStatusRejected   = "rejected"
)

// NewJobTarget is one row for CreateJobTargets to insert: an accepted
// target (Status: JobTargetStatusPending, Reason nil) or a rejected one
// (Status: JobTargetStatusRejected, Reason set).
type NewJobTarget struct {
	InstanceUID string
	Status      string
	Reason      *string
}

// JobQueue is the CRUD surface for jobs/job_targets. It deliberately does
// not include arm/cancel/dispatch: those need the 5-minute cancellable-delay
// scheduler and the River worker wiring described in "Jobs: schema and
// execution", neither built yet. CreateJobTargets is separate from
// CreateJob (rather than one call that inserts a job with its targets)
// because job_targets is materialized once, later, at arm time — not at
// creation, when a job is still just a previewable "planned" intent.
type JobQueue interface {
	CreateJob(ctx context.Context, job Job) (Job, error)
	GetJob(ctx context.Context, id string) (Job, bool, error)
	ListJobs(ctx context.Context) ([]Job, error)
	CreateJobTargets(ctx context.Context, jobID string, targets []NewJobTarget) ([]JobTarget, error)
	ListJobTargets(ctx context.Context, jobID string) ([]JobTarget, error)
}

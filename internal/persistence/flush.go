package persistence

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dennisme/grex/internal/fleet"
)

// DirtyTracker implements fleet.Events, recording which agents changed
// since the last Drain. It performs no I/O itself; a Flusher decides what
// to do with the drained set. Combine with other fleet.Events
// implementations (e.g. metrics) via fleet.MultiEvents.
type DirtyTracker struct {
	mu    sync.Mutex
	dirty map[string]struct{}
}

var _ fleet.Events = (*DirtyTracker)(nil)

// NewDirtyTracker returns an empty tracker.
func NewDirtyTracker() *DirtyTracker {
	return &DirtyTracker{dirty: make(map[string]struct{})}
}

func (d *DirtyTracker) mark(instanceUID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dirty[instanceUID] = struct{}{}
}

// AgentConnected implements fleet.Events.
func (d *DirtyTracker) AgentConnected(instanceUID string) { d.mark(instanceUID) }

// AgentDisconnected implements fleet.Events.
func (d *DirtyTracker) AgentDisconnected(instanceUID string) { d.mark(instanceUID) }

// AgentEvicted implements fleet.Events. The evicted agent is no longer in
// the registry by the time a Flusher drains this; Flusher soft-deletes its
// durable row rather than saving it (there's nothing left in the registry
// to save).
func (d *DirtyTracker) AgentEvicted(instanceUID string) { d.mark(instanceUID) }

// ReportReceived implements fleet.Events.
func (d *DirtyTracker) ReportReceived(instanceUID, _ string) { d.mark(instanceUID) }

// MissingAttribute implements fleet.Events.
func (d *DirtyTracker) MissingAttribute(instanceUID, _ string) { d.mark(instanceUID) }

// ReservedAttributeConflict implements fleet.Events.
func (d *DirtyTracker) ReservedAttributeConflict(instanceUID, _ string) { d.mark(instanceUID) }

// Drain returns every instance_uid marked dirty since the last Drain and
// clears the set.
func (d *DirtyTracker) Drain() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.dirty) == 0 {
		return nil
	}
	out := make([]string, 0, len(d.dirty))
	for id := range d.dirty {
		out = append(out, id)
	}
	clear(d.dirty)
	return out
}

// Flusher periodically persists agents a DirtyTracker has marked changed.
// It runs on its own ticker, deliberately separate from fleet.Registry's
// Sweep ticker: liveness/eviction and durability are different concerns and
// must not share a schedule even if their intervals end up similar.
type Flusher struct {
	registry      *fleet.Registry
	dirty         *DirtyTracker
	store         StateStore
	interval      time.Duration
	log           *slog.Logger
	maxConcurrent int
	metrics       WriteMetrics
	connStore     ConnectionStore
	replicaID     string
	replicaLabel  string
}

// NewFlusher builds a Flusher. registry is read via Get, never written.
// maxConcurrent bounds how many SaveAgent/SoftDeleteAgent calls run at once
// within a single flushOnce (see runConcurrent) — a stuck write only
// occupies one slot instead of blocking every agent queued after it.
// interval doubles as each write's timeout (see writeWithTimeout): a write
// can't outlive its own next retry opportunity. metrics may be nil.
//
// replicaID identifies this grex process for agent_connections (see
// docs/spec/design.md's Dispatch routing section) — a UUID generated once
// at startup, not a pod name or hostname (those churn on restart and aren't
// guaranteed unique across clusters sharing one Postgres). replicaLabel is
// a human-readable pod name/hostname for debugging only, never used for
// routing or uniqueness.
func NewFlusher(registry *fleet.Registry, dirty *DirtyTracker, store StateStore, interval time.Duration, log *slog.Logger, maxConcurrent int, metrics WriteMetrics, connStore ConnectionStore, replicaID, replicaLabel string) *Flusher {
	if metrics != nil {
		metrics.SetWriteTimeout("save_agent", interval)
		metrics.SetWriteTimeout("soft_delete_agent", interval)
		metrics.SetWriteTimeout("upsert_agent_connection", interval)
	}
	return &Flusher{
		registry: registry, dirty: dirty, store: store, interval: interval, log: log,
		maxConcurrent: maxConcurrent, metrics: metrics,
		connStore: connStore, replicaID: replicaID, replicaLabel: replicaLabel,
	}
}

// Run flushes on every interval tick until ctx is done.
func (f *Flusher) Run(ctx context.Context) {
	ticker := time.NewTicker(f.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			f.flushOnce(ctx)
		}
	}
}

// flushOnce drains the dirty set and saves each agent still present in the
// registry, up to maxConcurrent at once (see runConcurrent). Whatever
// doesn't make it into this flush before a crash — or times out, see
// writeWithTimeout — is simply lost from the database: agents already
// re-report everything on reconnect (ReportFullState), so durability here
// only needs to survive seconds of staleness, not be per-message-perfect.
//
// An id no longer in the registry was evicted between being marked dirty
// and this flush; there's nothing left to save, so it's soft-deleted
// instead. The eviction timestamp is approximated as "now" (the actual
// eviction moment isn't carried by fleet.Events), which is fine here: the
// retention window is measured in days, not the seconds of imprecision
// this introduces.
func (f *Flusher) flushOnce(ctx context.Context) {
	ids := f.dirty.Drain()

	// SaveAgent is its own multi-statement transaction per agent (agents
	// row plus a variable number of agent_effective_config rows) — not a
	// single statement, so it doesn't fit the same chunked-pgx.Batch
	// treatment as the two writes below without a bigger rewrite of how
	// effective_config is stored. Stays one round trip per agent, same as
	// before. SoftDeleteAgent (evicted agents) and UpsertAgentConnection
	// (connected agents) are both single statements and do batch — see
	// flushSoftDeletes/flushAgentConnections.
	var evictedIDs []string
	var connectedAgents []fleet.Agent
	tasks := make([]func(), 0, len(ids))
	for _, id := range ids {
		id := id
		agent, ok := f.registry.Get(id)
		if !ok {
			evictedIDs = append(evictedIDs, id)
			continue
		}
		if agent.Connected {
			connectedAgents = append(connectedAgents, agent)
		}
		tasks = append(tasks, func() {
			err := writeWithTimeout(ctx, f.interval, f.metrics, "save_agent", func(ctx context.Context) error {
				return f.store.SaveAgent(ctx, agent)
			})
			if err != nil {
				f.log.Error("persistence flush failed", "instance_uid", id, "error", err)
			}
		})
	}
	runConcurrent(tasks, f.maxConcurrent)

	f.flushSoftDeletes(ctx, evictedIDs)
	// Upsert-only, deliberately: only ever refresh agent_connections while
	// this replica still holds a live connection for an agent. Never
	// delete on disconnect here — this replica's own flush tick can run
	// after the agent has already reconnected to a different replica, and
	// a delete at that point would erase the other replica's fresher
	// ownership row instead of this replica's own stale one. A row for an
	// agent that's gone dark everywhere is left to age out via last_seen,
	// per docs/spec/design.md's Dispatch routing section ("or lets a
	// lease expire") — the lease-expiry sweep that reads last_seen is
	// separate, not yet built, follow-up work.
	f.flushAgentConnections(ctx, connectedAgents)
}

// flushSoftDeletes persists ids no longer in the registry (evicted between
// being marked dirty and this flush). Batches via pgx.Batch, chunked at
// defaultBatchSize, when f.store supports BatchStateStore; falls back to
// one SoftDeleteAgent round trip per id otherwise (test fakes, chiefly —
// no behavior change for those).
func (f *Flusher) flushSoftDeletes(ctx context.Context, ids []string) {
	if len(ids) == 0 {
		return
	}
	batchStore, ok := f.store.(BatchStateStore)
	if !ok {
		tasks := make([]func(), 0, len(ids))
		for _, id := range ids {
			id := id
			tasks = append(tasks, func() {
				err := writeWithTimeout(ctx, f.interval, f.metrics, "soft_delete_agent", func(ctx context.Context) error {
					return f.store.SoftDeleteAgent(ctx, id, time.Now())
				})
				if err != nil {
					f.log.Error("persistence soft-delete failed", "instance_uid", id, "error", err)
				}
			})
		}
		runConcurrent(tasks, f.maxConcurrent)
		return
	}

	chunks := chunk(ids, defaultBatchSize)
	tasks := make([]func(), 0, len(chunks))
	for _, ids := range chunks {
		ids := ids
		tasks = append(tasks, func() {
			now := time.Now()
			batch := &pgx.Batch{}
			for _, id := range ids {
				batchStore.QueueSoftDeleteAgent(batch, id, now)
			}
			_ = writeWithTimeout(ctx, f.interval, f.metrics, "soft_delete_agent", func(ctx context.Context) error {
				results := batchStore.SendBatch(ctx, batch)
				defer func() { _ = results.Close() }()
				for _, id := range ids {
					if _, err := results.Exec(); err != nil {
						f.log.Error("persistence soft-delete failed", "instance_uid", id, "error", err)
					}
				}
				return nil
			})
		})
	}
	runConcurrent(tasks, f.maxConcurrent)
}

// flushAgentConnections upserts agent_connections for every currently
// connected dirty agent. Batches when f.connStore supports
// BatchConnectionStore, same fallback shape as flushSoftDeletes.
func (f *Flusher) flushAgentConnections(ctx context.Context, agents []fleet.Agent) {
	if len(agents) == 0 {
		return
	}
	batchConnStore, ok := f.connStore.(BatchConnectionStore)
	if !ok {
		tasks := make([]func(), 0, len(agents))
		for _, agent := range agents {
			agent := agent
			tasks = append(tasks, func() {
				now := time.Now()
				err := writeWithTimeout(ctx, f.interval, f.metrics, "upsert_agent_connection", func(ctx context.Context) error {
					return f.connStore.UpsertAgentConnection(ctx, AgentConnection{
						InstanceUID:  agent.InstanceUID,
						ReplicaID:    f.replicaID,
						ReplicaLabel: f.replicaLabel,
						ConnectedAt:  now,
						LastSeen:     now,
					})
				})
				if err != nil {
					f.log.Error("persistence agent_connections upsert failed", "instance_uid", agent.InstanceUID, "error", err)
				}
			})
		}
		runConcurrent(tasks, f.maxConcurrent)
		return
	}

	ids := make([]string, len(agents))
	for i, agent := range agents {
		ids[i] = agent.InstanceUID
	}
	chunks := chunk(ids, defaultBatchSize)
	tasks := make([]func(), 0, len(chunks))
	for _, ids := range chunks {
		ids := ids
		tasks = append(tasks, func() {
			now := time.Now()
			batch := &pgx.Batch{}
			for _, id := range ids {
				batchConnStore.QueueUpsertAgentConnection(batch, AgentConnection{
					InstanceUID:  id,
					ReplicaID:    f.replicaID,
					ReplicaLabel: f.replicaLabel,
					ConnectedAt:  now,
					LastSeen:     now,
				})
			}
			_ = writeWithTimeout(ctx, f.interval, f.metrics, "upsert_agent_connection", func(ctx context.Context) error {
				results := batchConnStore.SendBatch(ctx, batch)
				defer func() { _ = results.Close() }()
				for _, id := range ids {
					if _, err := results.Exec(); err != nil {
						f.log.Error("persistence agent_connections upsert failed", "instance_uid", id, "error", err)
					}
				}
				return nil
			})
		})
	}
	runConcurrent(tasks, f.maxConcurrent)
}

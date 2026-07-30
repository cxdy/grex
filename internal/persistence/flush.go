package persistence

import (
	"context"
	"log/slog"
	"sync"
	"time"

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
}

// NewFlusher builds a Flusher. registry is read via Get, never written.
// maxConcurrent bounds how many SaveAgent/SoftDeleteAgent calls run at once
// within a single flushOnce (see runConcurrent) — a stuck write only
// occupies one slot instead of blocking every agent queued after it.
// interval doubles as each write's timeout (see writeWithTimeout): a write
// can't outlive its own next retry opportunity. metrics may be nil.
func NewFlusher(registry *fleet.Registry, dirty *DirtyTracker, store StateStore, interval time.Duration, log *slog.Logger, maxConcurrent int, metrics WriteMetrics) *Flusher {
	if metrics != nil {
		metrics.SetWriteTimeout("save_agent", interval)
		metrics.SetWriteTimeout("soft_delete_agent", interval)
	}
	return &Flusher{
		registry: registry, dirty: dirty, store: store, interval: interval, log: log,
		maxConcurrent: maxConcurrent, metrics: metrics,
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
	tasks := make([]func(), 0, len(ids))
	for _, id := range ids {
		id := id
		tasks = append(tasks, func() {
			agent, ok := f.registry.Get(id)
			if !ok {
				err := writeWithTimeout(ctx, f.interval, f.metrics, "soft_delete_agent", func(ctx context.Context) error {
					return f.store.SoftDeleteAgent(ctx, id, time.Now())
				})
				if err != nil {
					f.log.Error("persistence soft-delete failed", "instance_uid", id, "error", err)
				}
				return
			}
			err := writeWithTimeout(ctx, f.interval, f.metrics, "save_agent", func(ctx context.Context) error {
				return f.store.SaveAgent(ctx, agent)
			})
			if err != nil {
				f.log.Error("persistence flush failed", "instance_uid", id, "error", err)
			}
		})
	}
	runConcurrent(tasks, f.maxConcurrent)
}

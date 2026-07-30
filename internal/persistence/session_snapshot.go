package persistence

import (
	"context"
	"log/slog"
	"time"

	"github.com/dennisme/grex/internal/fleet"
)

// SessionSnapshotter periodically writes every currently registered agent's
// session state (agent_session) via StateStore.SaveSession, independent of
// DirtyTracker. Report() only marks an agent dirty when a reportable field
// (description/health/packages) changes (see Registry.Report), so a quiet,
// healthy agent sending lightweight heartbeats would otherwise never
// re-flush its session data — see docs/spec/design.md's Agent state
// schema, "agent_session ... can be snapshotted wholesale each tick rather
// than tracked in the dirty set." Runs on its own ticker, deliberately
// separate from Flusher's: the two write different tables on different
// cadences for different reasons (see Flusher's own doc comment on why it
// stays separate from Registry.Sweep, same reasoning applies here).
type SessionSnapshotter struct {
	registry      *fleet.Registry
	store         StateStore
	interval      time.Duration
	log           *slog.Logger
	maxConcurrent int
	metrics       WriteMetrics
}

// NewSessionSnapshotter builds a SessionSnapshotter. registry is read via
// List, never written. maxConcurrent bounds how many SaveSession calls run
// at once within a single snapshotOnce (see runConcurrent). interval
// doubles as each write's timeout (see writeWithTimeout). metrics may be
// nil.
func NewSessionSnapshotter(registry *fleet.Registry, store StateStore, interval time.Duration, log *slog.Logger, maxConcurrent int, metrics WriteMetrics) *SessionSnapshotter {
	if metrics != nil {
		metrics.SetWriteTimeout("save_session", interval)
	}
	return &SessionSnapshotter{
		registry: registry, store: store, interval: interval, log: log,
		maxConcurrent: maxConcurrent, metrics: metrics,
	}
}

// Run snapshots on every interval tick until ctx is done.
func (s *SessionSnapshotter) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.snapshotOnce(ctx)
		}
	}
}

// snapshotOnce writes every registered agent's session state, regardless of
// dirty status, up to maxConcurrent at once (see runConcurrent) — a single
// stuck SaveSession call only occupies one slot rather than blocking every
// other agent in this tick.
func (s *SessionSnapshotter) snapshotOnce(ctx context.Context) {
	agents := s.registry.List()
	tasks := make([]func(), 0, len(agents))
	for _, agent := range agents {
		agent := agent
		tasks = append(tasks, func() {
			err := writeWithTimeout(ctx, s.interval, s.metrics, "save_session", func(ctx context.Context) error {
				return s.store.SaveSession(ctx, agent)
			})
			if err != nil {
				s.log.Error("persistence session snapshot failed", "instance_uid", agent.InstanceUID, "error", err)
			}
		})
	}
	runConcurrent(tasks, s.maxConcurrent)
}

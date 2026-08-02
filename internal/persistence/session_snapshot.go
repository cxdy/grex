package persistence

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

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

// Run snapshots on every interval tick until ctx is done, then does one
// more snapshot before returning — see Flusher.Run's identical reasoning:
// whatever changed just before a shutdown's drain window ends still gets
// persisted. ctx is already cancelled by then, so a fresh, independently
// bounded context replaces it for that one last attempt.
func (s *SessionSnapshotter) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			finalCtx, cancel := context.WithTimeout(context.Background(), s.interval)
			s.snapshotOnce(finalCtx)
			cancel()
			return
		case <-ticker.C:
			s.snapshotOnce(ctx)
		}
	}
}

// snapshotOnce writes every registered agent's session state, regardless of
// dirty status. When s.store supports BatchStateStore (see
// docs/spec/design.md's Scaling gaps items 3-4), agents are chunked at
// defaultBatchSize and each chunk goes through one pgx.Batch round trip;
// otherwise falls back to one SaveSession round trip per agent (test
// fakes, chiefly — no behavior change for those). Either way, up to
// maxConcurrent chunks/agents run at once (see runConcurrent) — a single
// stuck write only occupies one slot, and with batching, only delays the
// rest of its own chunk's results, not any other chunk's.
func (s *SessionSnapshotter) snapshotOnce(ctx context.Context) {
	agents := s.registry.List()
	if len(agents) == 0 {
		return
	}

	batchStore, ok := s.store.(BatchStateStore)
	if !ok {
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
		return
	}

	byID := make(map[string]fleet.Agent, len(agents))
	ids := make([]string, len(agents))
	for i, agent := range agents {
		byID[agent.InstanceUID] = agent
		ids[i] = agent.InstanceUID
	}
	chunks := chunk(ids, defaultBatchSize)
	tasks := make([]func(), 0, len(chunks))
	for _, ids := range chunks {
		ids := ids
		tasks = append(tasks, func() {
			batch := &pgx.Batch{}
			for _, id := range ids {
				batchStore.QueueSaveSession(batch, byID[id])
			}
			_ = writeWithTimeout(ctx, s.interval, s.metrics, "save_session", func(ctx context.Context) error {
				results := batchStore.SendBatch(ctx, batch)
				defer func() { _ = results.Close() }()
				for _, id := range ids {
					if _, err := results.Exec(); err != nil {
						s.log.Error("persistence session snapshot failed", "instance_uid", id, "error", err)
					}
				}
				return nil
			})
		})
	}
	runConcurrent(tasks, s.maxConcurrent)
}

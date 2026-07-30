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
	registry *fleet.Registry
	store    StateStore
	interval time.Duration
	log      *slog.Logger
}

// NewSessionSnapshotter builds a SessionSnapshotter. registry is read via
// List, never written.
func NewSessionSnapshotter(registry *fleet.Registry, store StateStore, interval time.Duration, log *slog.Logger) *SessionSnapshotter {
	return &SessionSnapshotter{registry: registry, store: store, interval: interval, log: log}
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
// dirty status.
func (s *SessionSnapshotter) snapshotOnce(ctx context.Context) {
	for _, agent := range s.registry.List() {
		if err := s.store.SaveSession(ctx, agent); err != nil {
			s.log.Error("persistence session snapshot failed", "instance_uid", agent.InstanceUID, "error", err)
		}
	}
}

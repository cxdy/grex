package persistence

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dennisme/grex/internal/fleet"
)

// SessionSnapshotter keeps every registered agent's session state
// (agent_session) fresh enough to be trusted as a liveness signal, writing
// through StateStore.SaveSession independent of DirtyTracker. Report() only
// marks an agent dirty when a reportable field (description/health/packages)
// changes (see Registry.Report), and opamp-go's heartbeat carries none of
// them, so a quiet, healthy agent would otherwise never re-flush its session
// data — and api.StaleConnected, which decides whether an agent owned by
// another replica still shows as connected, reads exactly that row's
// updated_at. Runs on its own ticker, deliberately separate from Flusher's:
// the two write different tables on different cadences for different
// reasons (see Flusher's own doc comment on why it stays separate from
// Registry.Sweep, same reasoning applies here).
//
// An agent is written when it has no stored row yet, or when the row's
// stored timestamp has aged to the keepalive point — not on every tick.
// Everything else a session row holds (connected, remote_addr, tls_subject,
// via_gateway, transport, description_reported) only changes through events
// that already mark the agent dirty, and SaveAgent writes agent_session as
// part of that flush, so those changes land through Flusher without waiting
// for a keepalive. sequence_num is the one column allowed to sit stale
// between keepalives; it is display/debugging only and never read back (see
// the agents migration).
type SessionSnapshotter struct {
	registry      *fleet.Registry
	store         StateStore
	interval      time.Duration
	log           *slog.Logger
	maxConcurrent int
	metrics       WriteMetrics
	keepalive     time.Duration
	now           func() time.Time

	mu      sync.Mutex
	written map[string]sessionWrite
	gen     uint64
}

// sessionWrite is what this snapshotter last wrote for one agent: lastSeen
// is the timestamp that landed in agent_session.updated_at, which is what
// decides when the row next comes due. gen is the snapshot pass that last
// saw this agent in the registry, so entries for agents that have since been
// evicted can be dropped instead of accumulating forever.
type sessionWrite struct {
	lastSeen time.Time
	gen      uint64
}

// NewSessionSnapshotter builds a SessionSnapshotter. registry is read via
// List and DisconnectThreshold, never written. maxConcurrent bounds how many
// SaveSession calls run at once within a single snapshotOnce (see
// runConcurrent). interval doubles as each write's timeout (see
// writeWithTimeout). metrics may be nil.
func NewSessionSnapshotter(registry *fleet.Registry, store StateStore, interval time.Duration, log *slog.Logger, maxConcurrent int, metrics WriteMetrics) *SessionSnapshotter {
	if metrics != nil {
		metrics.SetWriteTimeout("save_session", interval)
	}
	return &SessionSnapshotter{
		registry: registry, store: store, interval: interval, log: log,
		maxConcurrent: maxConcurrent, metrics: metrics,
		keepalive: keepaliveInterval(registry.DisconnectThreshold(), interval),
		now:       time.Now,
		written:   make(map[string]sessionWrite),
	}
}

// keepaliveInterval is how old a stored session row is allowed to get before
// it is rewritten, derived from the fleet's own disconnect threshold rather
// than configured separately: an operator who tuned it independently could
// silently push stored rows past the threshold api.StaleConnected compares
// against, which reads as the whole fleet disconnecting.
//
// Two-thirds of the threshold by default, leaving a third as margin. A row
// can only come due at a tick boundary, so its worst-case age is the
// keepalive plus one more tick — hence the clamp, which matters when the
// threshold is small enough for the tick to eat the margin. A threshold at
// or below the tick leaves no room at all and degrades to writing every
// agent every tick.
func keepaliveInterval(disconnectAfter, tick time.Duration) time.Duration {
	keepalive := disconnectAfter * 2 / 3
	if limit := disconnectAfter - tick; keepalive > limit {
		keepalive = limit
	}
	if keepalive < 0 {
		keepalive = 0
	}
	return keepalive
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

// snapshotOnce writes the registered agents whose session rows have come
// due (see due), regardless of dirty status. When s.store supports
// BatchStateStore (see docs/spec/design.md's Scaling gaps item 3), agents
// are chunked at defaultBatchSize and each chunk goes through one pgx.Batch
// round trip; otherwise falls back to one SaveSession round trip per agent
// (test fakes, chiefly — no behavior change for those). Either way, up to
// maxConcurrent chunks/agents run at once (see runConcurrent) — a single
// stuck write only occupies one slot, and with batching, only delays the
// rest of its own chunk's results, not any other chunk's.
func (s *SessionSnapshotter) snapshotOnce(ctx context.Context) {
	agents := s.due()
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
					return
				}
				s.recordWritten(agent)
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
						continue
					}
					s.recordWritten(byID[id])
				}
				return nil
			})
		})
	}
	runConcurrent(tasks, s.maxConcurrent)
}

// due returns the registered agents whose session rows need writing this
// tick: those with nothing stored yet, and those whose stored timestamp has
// reached the keepalive point. It also refreshes the generation on every
// agent it saw and drops tracking for the ones it didn't, which is how
// evicted agents leave the map — nothing else removes them.
func (s *SessionSnapshotter) due() []fleet.Agent {
	agents := s.registry.List()
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.gen++
	due := make([]fleet.Agent, 0, len(agents))
	for _, agent := range agents {
		w, tracked := s.written[agent.InstanceUID]
		if tracked {
			w.gen = s.gen
			s.written[agent.InstanceUID] = w
			if now.Sub(w.lastSeen) < s.keepalive {
				continue
			}
		}
		due = append(due, agent)
	}
	for id, w := range s.written {
		if w.gen != s.gen {
			delete(s.written, id)
		}
	}
	return due
}

// recordWritten notes what landed in agent_session.updated_at for one agent,
// which is what due measures the next write against. Called only after a
// write succeeds: a failed one leaves the agent due again immediately rather
// than waiting out a keepalive it never actually served.
func (s *SessionSnapshotter) recordWritten(agent fleet.Agent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.written[agent.InstanceUID] = sessionWrite{lastSeen: agent.LastSeen, gen: s.gen}
}

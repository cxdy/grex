package persistence

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

const purgeJobID = "purge-evicted-agents"

// PurgeEvictedAgentsArgs is the periodic purge job's arguments. Before is
// computed once, when the scheduler fires (now minus
// fleet.soft_delete_duration), not recomputed inside Work — a job that sits
// in the queue a while still purges as of when it was scheduled.
type PurgeEvictedAgentsArgs struct {
	Before time.Time `json:"before"`
}

// Kind implements river.JobArgs.
func (PurgeEvictedAgentsArgs) Kind() string { return "purge_evicted_agents" }

// PurgeMetrics receives the count of rows a purge run actually removed.
// internal/metrics.Events satisfies this.
type PurgeMetrics interface {
	AgentsPurged(n int)
}

// PurgeWorker deletes agents rows soft-deleted before its job's cutoff.
// agent_session/agent_effective_config cascade via their existing foreign
// keys, so deleting from agents is enough.
type PurgeWorker struct {
	river.WorkerDefaults[PurgeEvictedAgentsArgs]
	pool    *pgxpool.Pool
	metrics PurgeMetrics
}

// Work implements river.Worker[PurgeEvictedAgentsArgs]. The generic
// *river.Job[T] parameter isn't a style choice, it's River's own interface
// shape — Worker[T] requires exactly this signature. Don't reach for
// generics elsewhere in this codebase; this is the one place a dependency
// requires it.
func (w *PurgeWorker) Work(ctx context.Context, job *river.Job[PurgeEvictedAgentsArgs]) error {
	tag, err := w.pool.Exec(ctx,
		`DELETE FROM agents WHERE evicted_at IS NOT NULL AND evicted_at < $1`, job.Args.Before)
	if err != nil {
		return fmt.Errorf("purge evicted agents: %w", err)
	}
	if n := tag.RowsAffected(); n > 0 && w.metrics != nil {
		w.metrics.AgentsPurged(int(n))
	}
	return nil
}

// NewPurgeClient builds a River client with one periodic job — purge agents
// soft-deleted more than softDeleteDuration ago, running hourly. The caller
// starts and stops it (client.Start(ctx) / client.Stop(ctx)); it shares
// pool with the rest of persistence, no separate connection needed.
// metrics may be nil. log is River's own logger (routed through grex's
// configured logger rather than River's stdout default) so its output
// matches the rest of grex's log format.
func NewPurgeClient(pool *pgxpool.Pool, softDeleteDuration time.Duration, metrics PurgeMetrics, log *slog.Logger) (*river.Client[pgx.Tx], error) {
	workers := river.NewWorkers()
	river.AddWorker(workers, &PurgeWorker{pool: pool, metrics: metrics})

	periodicJob := river.NewPeriodicJob(
		river.PeriodicInterval(time.Hour),
		func() (river.JobArgs, *river.InsertOpts) {
			return PurgeEvictedAgentsArgs{Before: time.Now().Add(-softDeleteDuration)}, nil
		},
		// RunOnStart: a purge on every grex startup catches up anything that
		// accumulated while the process was down, rather than waiting up to
		// an hour after every restart before the first purge runs.
		&river.PeriodicJobOpts{ID: purgeJobID, RunOnStart: true},
	)

	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues:       map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 1}},
		Workers:      workers,
		PeriodicJobs: []*river.PeriodicJob{periodicJob},
		Logger:       log,
	})
	if err != nil {
		return nil, fmt.Errorf("new river client: %w", err)
	}
	return client, nil
}

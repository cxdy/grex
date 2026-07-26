package persistence

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

// countingMetrics records AgentsPurged calls for assertions.
type countingMetrics struct {
	mu    sync.Mutex
	total int
}

func (m *countingMetrics) AgentsPurged(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.total += n
}

func (m *countingMetrics) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.total
}

func TestPurgeWorkerDeletesOldEvictedAgentsOnly(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	if err := store.SaveAgent(ctx, testAgent("old-evicted", now)); err != nil {
		t.Fatalf("SaveAgent old-evicted: %v", err)
	}
	if err := store.SoftDeleteAgent(ctx, "old-evicted", now.Add(-10*24*time.Hour)); err != nil {
		t.Fatalf("SoftDeleteAgent old-evicted: %v", err)
	}
	if err := store.SaveAgent(ctx, testAgent("recently-evicted", now)); err != nil {
		t.Fatalf("SaveAgent recently-evicted: %v", err)
	}
	if err := store.SoftDeleteAgent(ctx, "recently-evicted", now.Add(-1*time.Hour)); err != nil {
		t.Fatalf("SoftDeleteAgent recently-evicted: %v", err)
	}
	if err := store.SaveAgent(ctx, testAgent("still-live", now)); err != nil {
		t.Fatalf("SaveAgent still-live: %v", err)
	}

	metrics := &countingMetrics{}
	worker := &PurgeWorker{pool: store.pool, metrics: metrics}
	// Cutoff of 7 days ago: old-evicted (10d) is past it, recently-evicted
	// (1h) and still-live (never evicted) are not.
	job := &river.Job[PurgeEvictedAgentsArgs]{Args: PurgeEvictedAgentsArgs{Before: now.Add(-7 * 24 * time.Hour)}}
	if err := worker.Work(ctx, job); err != nil {
		t.Fatalf("Work: %v", err)
	}

	if _, ok, err := store.GetAgent(ctx, "old-evicted"); err != nil || ok {
		t.Errorf("old-evicted: ok=%v err=%v, want purged (ok=false)", ok, err)
	}
	if _, ok, err := store.GetAgent(ctx, "recently-evicted"); err != nil || !ok {
		t.Errorf("recently-evicted: ok=%v err=%v, want still present", ok, err)
	}
	if _, ok, err := store.GetAgent(ctx, "still-live"); err != nil || !ok {
		t.Errorf("still-live: ok=%v err=%v, want still present", ok, err)
	}
	if got := metrics.count(); got != 1 {
		t.Errorf("AgentsPurged total = %d, want 1", got)
	}
}

func TestPurgeWorkerContextCanceled(t *testing.T) {
	store := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	worker := &PurgeWorker{pool: store.pool}
	job := &river.Job[PurgeEvictedAgentsArgs]{Args: PurgeEvictedAgentsArgs{Before: time.Now()}}
	err := worker.Work(ctx, job)
	if err == nil {
		t.Fatal("want error for a cancelled context")
	}
	if !strings.Contains(err.Error(), "purge evicted agents") {
		t.Errorf("error = %q, want it to mention purge evicted agents", err.Error())
	}
}

func TestNewPurgeClientRunsAndPurgesViaInsert(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	if err := store.SaveAgent(ctx, testAgent("agent-1", now)); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
	if err := store.SoftDeleteAgent(ctx, "agent-1", now.Add(-30*24*time.Hour)); err != nil {
		t.Fatalf("SoftDeleteAgent: %v", err)
	}

	// River's own tables (river_job, etc) aren't part of our migrations
	// (see cmd/river-migrate); a real client needs them present.
	migrator, err := rivermigrate.New(riverpgxv5.New(store.pool), nil)
	if err != nil {
		t.Fatalf("rivermigrate.New: %v", err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		t.Fatalf("migrate river schema: %v", err)
	}

	metrics := &countingMetrics{}
	var logBuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logBuf, nil))
	client, err := NewPurgeClient(store.pool, 7*24*time.Hour, metrics, log)
	if err != nil {
		t.Fatalf("NewPurgeClient: %v", err)
	}
	if err := client.Start(ctx); err != nil {
		t.Fatalf("client.Start: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Stop(context.Background()); err != nil {
			t.Errorf("client.Stop: %v", err)
		}
		// River's periodic-job-enqueuer maintenance service can log one
		// benign "context canceled" error if its own background loop races
		// Stop's internal shutdown — expected and asserted for specifically,
		// not just discarded, per the pristine-test-output rule. Anything
		// else logged at error level is a real failure.
		for _, line := range strings.Split(strings.TrimSpace(logBuf.String()), "\n") {
			if line == "" {
				continue
			}
			if !strings.Contains(line, "level=ERROR") {
				continue
			}
			if strings.Contains(line, "PeriodicJobEnqueuer") && strings.Contains(line, "context canceled") {
				continue
			}
			t.Errorf("unexpected error log from river client: %s", line)
		}
	})

	// Insert directly instead of waiting for the hourly periodic schedule.
	if _, err := client.Insert(ctx, PurgeEvictedAgentsArgs{Before: now.Add(-7 * 24 * time.Hour)}, nil); err != nil {
		t.Fatalf("client.Insert: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok, err := store.GetAgent(ctx, "agent-1"); err == nil && !ok {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("purge job did not remove agent-1 within the deadline")
}

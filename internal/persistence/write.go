package persistence

import (
	"context"
	"sync"
	"time"
)

// WriteMetrics receives timing for each persistence write attempt and the
// configured timeout it's bounded by, so an operator can compare the two
// directly (average/percentile duration approaching or crossing the
// timeout line signals real trouble — DB, network, or load — before
// writes actually start failing). internal/metrics.Events satisfies this.
type WriteMetrics interface {
	// ObserveWriteDuration records how long one write attempt took, by op
	// (e.g. "save_agent", "soft_delete_agent", "save_session"). Recorded on
	// every attempt, success or timeout, so a rise in timeouts shows up as
	// tail latency rather than being silently dropped.
	ObserveWriteDuration(op string, d time.Duration)
	// SetWriteTimeout records the configured timeout for op. Called once at
	// construction, not per write — it's a static comparison line, not a
	// per-attempt observation.
	SetWriteTimeout(op string, timeout time.Duration)
}

// writeWithTimeout bounds one persistence write to timeout: a write can't
// outlive its own next retry opportunity (Flusher/SessionSnapshotter both
// re-attempt using current state on their next tick regardless, so nothing
// is lost by cutting a stuck write off rather than leaving it to hang
// indefinitely — see Flusher's and SessionSnapshotter's own doc comments
// on why durability here tolerates seconds of staleness, not per-write
// perfection). metrics may be nil.
func writeWithTimeout(ctx context.Context, timeout time.Duration, metrics WriteMetrics, op string, do func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	err := do(ctx)
	if metrics != nil {
		metrics.ObserveWriteDuration(op, time.Since(start))
	}
	return err
}

// runConcurrent runs every task, bounded by at most maxConcurrent running
// at once, and waits for all of them to finish before returning. A single
// slow or stuck task only occupies one of the maxConcurrent slots — it
// does not block tasks queued after it the way a plain sequential loop
// would. No generics: this codebase reaches for them only where a
// dependency requires it (see river.Worker[T]), so callers build a slice
// of closures instead.
func runConcurrent(tasks []func(), maxConcurrent int) {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	for _, task := range tasks {
		wg.Add(1)
		sem <- struct{}{}
		go func(task func()) {
			defer wg.Done()
			defer func() { <-sem }()
			task()
		}(task)
	}
	wg.Wait()
}

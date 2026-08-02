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

// defaultBatchSize is how many statements Flusher/SessionSnapshotter queue
// onto one pgx.Batch round trip when their store supports it (see
// BatchStateStore/BatchConnectionStore, docs/spec/design.md's Scaling gaps
// items 3-4). A starting point, not tuned against real load yet — a stuck
// row inside one chunk's batch only delays that chunk's own results, so
// this is also the upper bound on that blast radius.
const defaultBatchSize = 500

// chunk splits ids into groups of at most size, preserving order; the last
// group may be smaller. Used to turn a one-round-trip-per-agent write into
// one pgx.Batch round trip per chunk (see Flusher/SessionSnapshotter),
// bounding a chunk's own blast radius: a stuck row inside a chunk's batch
// only delays that chunk's remaining results, not any other chunk's — a
// smaller-radius version of runConcurrent's per-task isolation, not the
// same guarantee. size <= 0 degrades to one id per chunk (today's
// per-agent behavior) rather than panicking or looping forever.
func chunk(ids []string, size int) [][]string {
	if len(ids) == 0 {
		return nil
	}
	if size <= 0 {
		size = 1
	}
	chunks := make([][]string, 0, (len(ids)+size-1)/size)
	for i := 0; i < len(ids); i += size {
		chunks = append(chunks, ids[i:min(i+size, len(ids))])
	}
	return chunks
}

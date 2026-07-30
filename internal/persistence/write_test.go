package persistence

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeWriteMetrics records calls for assertion; safe for concurrent use
// since writeWithTimeout may be invoked from multiple goroutines under
// runConcurrent.
type fakeWriteMetrics struct {
	mu        sync.Mutex
	durations []struct {
		op string
		d  time.Duration
	}
	timeouts map[string]time.Duration
}

func newFakeWriteMetrics() *fakeWriteMetrics {
	return &fakeWriteMetrics{timeouts: make(map[string]time.Duration)}
}

func (f *fakeWriteMetrics) ObserveWriteDuration(op string, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.durations = append(f.durations, struct {
		op string
		d  time.Duration
	}{op, d})
}

func (f *fakeWriteMetrics) SetWriteTimeout(op string, timeout time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.timeouts[op] = timeout
}

func (f *fakeWriteMetrics) durationCount(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, d := range f.durations {
		if d.op == op {
			n++
		}
	}
	return n
}

func TestWriteWithTimeoutRecordsDurationOnSuccess(t *testing.T) {
	metrics := newFakeWriteMetrics()
	err := writeWithTimeout(context.Background(), time.Second, metrics, "save_agent", func(context.Context) error {
		return nil
	})
	if err != nil {
		t.Fatalf("writeWithTimeout: %v", err)
	}
	if got := metrics.durationCount("save_agent"); got != 1 {
		t.Fatalf("durationCount(save_agent) = %d, want 1", got)
	}
}

func TestWriteWithTimeoutCancelsSlowCall(t *testing.T) {
	metrics := newFakeWriteMetrics()
	started := time.Now()
	err := writeWithTimeout(context.Background(), 50*time.Millisecond, metrics, "save_agent", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	elapsed := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("elapsed = %v, want bounded near the 50ms timeout, not left hanging", elapsed)
	}
	if got := metrics.durationCount("save_agent"); got != 1 {
		t.Fatalf("durationCount(save_agent) = %d, want 1 (recorded even on timeout)", got)
	}
}

func TestWriteWithTimeoutNilMetricsIsSafe(t *testing.T) {
	err := writeWithTimeout(context.Background(), time.Second, nil, "save_agent", func(context.Context) error {
		return nil
	})
	if err != nil {
		t.Fatalf("writeWithTimeout: %v", err)
	}
}

// TestRunConcurrentStuckTaskDoesNotBlockOthers is the core property this
// whole change exists for: one slow/stuck task must not force every other
// task in the same batch to wait behind it.
func TestRunConcurrentStuckTaskDoesNotBlockOthers(t *testing.T) {
	const stuckDelay = 200 * time.Millisecond
	var fastCompletedAt [5]time.Time
	tasks := make([]func(), 0, 6)
	tasks = append(tasks, func() {
		time.Sleep(stuckDelay)
	})
	for i := range fastCompletedAt {
		i := i
		tasks = append(tasks, func() {
			fastCompletedAt[i] = time.Now()
		})
	}

	start := time.Now()
	runConcurrent(tasks, 4)
	elapsed := time.Since(start)

	if elapsed < stuckDelay {
		t.Fatalf("runConcurrent returned before the stuck task finished: elapsed=%v", elapsed)
	}
	for i, completedAt := range fastCompletedAt {
		if completedAt.IsZero() {
			t.Fatalf("fast task %d never ran", i)
		}
		if completedAt.Sub(start) > stuckDelay/2 {
			t.Errorf("fast task %d completed at %v after start, want well before the %v stuck delay (it shouldn't have queued behind the stuck task)",
				i, completedAt.Sub(start), stuckDelay)
		}
	}
}

func TestRunConcurrentBoundsMaxInFlight(t *testing.T) {
	const maxConcurrent = 3
	var current, maxObserved int32
	tasks := make([]func(), 0, 20)
	for i := 0; i < 20; i++ {
		tasks = append(tasks, func() {
			n := atomic.AddInt32(&current, 1)
			for {
				m := atomic.LoadInt32(&maxObserved)
				if n <= m || atomic.CompareAndSwapInt32(&maxObserved, m, n) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			atomic.AddInt32(&current, -1)
		})
	}
	runConcurrent(tasks, maxConcurrent)
	if maxObserved > maxConcurrent {
		t.Errorf("max concurrent in-flight = %d, want <= %d", maxObserved, maxConcurrent)
	}
	if maxObserved < maxConcurrent {
		t.Errorf("max concurrent in-flight = %d, want to actually reach %d (concurrency not exercised)", maxObserved, maxConcurrent)
	}
}

func TestRunConcurrentEmpty(t *testing.T) {
	runConcurrent(nil, 4) // must not panic or hang
}

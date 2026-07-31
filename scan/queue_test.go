package scan

import (
	"sync"
	"sync/atomic"
	"testing"
)

// newQueueRunner builds a runner with only the queue wired — no database — for
// exercising the worker with plain closures.
func newQueueRunner() *Runner {
	r := &Runner{wake: make(chan struct{}, 1)}
	go r.worker()
	return r
}

// TestQueueDedupCollapsesDuplicates: a key already running or pending is not queued
// again, so a restart storm or a double-click cannot stack redundant runs.
func TestQueueDedupCollapsesDuplicates(t *testing.T) {
	r := newQueueRunner()

	started := make(chan struct{})
	release := make(chan struct{})
	var blockerRuns, otherRuns int32

	r.enqueue(job{jobRefreshAll, "block", "blocker", func() {
		atomic.AddInt32(&blockerRuns, 1)
		close(started)
		<-release
	}})
	<-started // the blocker is now the current job

	// Same key as the running job — deduped.
	r.enqueue(job{jobRefreshAll, "block", "blocker", func() { atomic.AddInt32(&blockerRuns, 1) }})
	// A distinct job enqueued twice — the duplicate collapses onto the first.
	other := job{jobRefreshArtist, "other", "other", func() { atomic.AddInt32(&otherRuns, 1) }}
	r.enqueue(other)
	r.enqueue(other)

	close(release)
	r.waitIdle(t)

	if got := atomic.LoadInt32(&blockerRuns); got != 1 {
		t.Errorf("blocker ran %d times, want 1", got)
	}
	if got := atomic.LoadInt32(&otherRuns); got != 1 {
		t.Errorf("other ran %d times, want 1 (duplicate should collapse)", got)
	}
}

// TestQueueRunsSerially: the worker never runs two jobs at once.
func TestQueueRunsSerially(t *testing.T) {
	r := newQueueRunner()

	var inFlight, maxInFlight int32
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		key := string(rune('a' + i))
		r.enqueue(job{jobRefreshArtist, key, key, func() {
			defer wg.Done()
			n := atomic.AddInt32(&inFlight, 1)
			for {
				m := atomic.LoadInt32(&maxInFlight)
				if n <= m || atomic.CompareAndSwapInt32(&maxInFlight, m, n) {
					break
				}
			}
			atomic.AddInt32(&inFlight, -1)
		}})
	}
	wg.Wait()
	r.waitIdle(t)

	if maxInFlight != 1 {
		t.Errorf("max concurrent jobs = %d, want 1 (queue must serialise)", maxInFlight)
	}
}

// TestQueuePrioritisesFileJobs: a file-writing job enqueued behind a pending metadata
// job still runs first, so a user's scan is not stuck behind a long refresh.
func TestQueuePrioritisesFileJobs(t *testing.T) {
	r := newQueueRunner()

	started := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	var order []string
	rec := func(k string) func() {
		return func() {
			mu.Lock()
			order = append(order, k)
			mu.Unlock()
		}
	}

	// Occupy the worker so the next two jobs are ordered while both are pending.
	r.enqueue(job{jobRefreshAll, "blocker", "blocker", func() { close(started); <-release }})
	<-started

	r.enqueue(job{jobRefreshArtist, "meta", "meta", rec("meta")}) // metadata: lower priority
	r.enqueue(job{jobScanAll, "scan", "scan", rec("scan")})       // file-writing: jumps ahead

	close(release)
	r.waitIdle(t)

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "scan" || order[1] != "meta" {
		t.Errorf("run order = %v, want [scan meta] — a file job should precede a pending refresh", order)
	}
}

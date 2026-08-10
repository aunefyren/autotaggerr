package process

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

	r.enqueue(job{jobRefreshArtist, "meta", "meta", rec("meta")})       // metadata: lower priority
	r.enqueue(job{jobProcessAll, "process", "process", rec("process")}) // file-writing: jumps ahead

	close(release)
	r.waitIdle(t)

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "process" || order[1] != "meta" {
		t.Errorf("run order = %v, want [process meta] — a file job should precede a pending refresh", order)
	}
}

// TestJobDoesNotInheritPreviousProgress: the live progress atomics are written by
// scans alone, so a job that reports none of its own must not be described with the
// last scan's. Left unreset, a status poll during a metadata refresh showed the
// finished scan's full bar, its closing phase and the artist it ended on, all under
// the refresh's name — while the same refresh's event row in the feed showed its real
// position.
func TestJobDoesNotInheritPreviousProgress(t *testing.T) {
	r := newQueueRunner()

	// The state a completed scan leaves behind.
	r.progTotal.Store(17858)
	r.progDone.Store(17858)
	r.setPhase(PhaseCollection)
	r.setCurrent("Some Artist")

	var during Summary
	r.enqueue(job{jobRefreshAll, "refresh", "Metadata refresh", func() { during = r.Status() }})
	r.waitIdle(t)

	if !during.Running {
		t.Fatal("status taken inside a job should report running")
	}
	if during.Total != 0 || during.Done != 0 || during.Phase != "" || during.Current != "" {
		t.Errorf("progress during a refresh = %d/%d phase=%q current=%q, want all empty — not the previous scan's",
			during.Done, during.Total, during.Phase, during.Current)
	}
}

// TestScanJobReportsItsOwnProgress is the other half: clearing the atomics between
// jobs must not cost a running scan the bar it publishes into them.
func TestScanJobReportsItsOwnProgress(t *testing.T) {
	r := newQueueRunner()

	var during Summary
	r.enqueue(job{jobProcessAll, "process", "Process all libraries", func() {
		r.progTotal.Store(200)
		r.progDone.Store(75)
		r.setPhase(PhaseScanning)
		r.setCurrent("Some Artist")
		during = r.Status()
	}})
	r.waitIdle(t)

	if during.Total != 200 || during.Done != 75 || during.Phase != PhaseScanning || during.Current != "Some Artist" {
		t.Errorf("progress during a scan = %d/%d phase=%q current=%q, want 75/200 phase=%q current=%q",
			during.Done, during.Total, during.Phase, during.Current, PhaseScanning, "Some Artist")
	}
}

// TestShutdownDropsPendingAndWaitsForRunning is the shape of a deliberate stop: the
// job already executing is allowed to finish (it has written files and opened an
// event), and the ones behind it — which have started nothing — are dropped rather
// than holding the process open for hours.
func TestShutdownDropsPendingAndWaitsForRunning(t *testing.T) {
	r := newQueueRunner()

	started := make(chan struct{})
	release := make(chan struct{})
	var runningFinished, pendingRan int32

	r.enqueue(job{jobRefreshAll, "running", "the running job", func() {
		close(started)
		<-release
		atomic.AddInt32(&runningFinished, 1)
	}})
	<-started

	r.enqueue(job{jobRefreshArtist, "pending", "the pending job", func() {
		atomic.AddInt32(&pendingRan, 1)
	}})

	done := make(chan error, 1)
	go func() { done <- r.Shutdown(context.Background()) }()

	// Shutdown must still be waiting: the first job has not finished.
	select {
	case err := <-done:
		t.Fatalf("Shutdown returned %v while a job was still running", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return after the running job finished")
	}

	if atomic.LoadInt32(&runningFinished) != 1 {
		t.Error("the in-flight job was not allowed to finish")
	}
	if got := atomic.LoadInt32(&pendingRan); got != 0 {
		t.Errorf("a pending job ran %d time(s) during shutdown, want 0", got)
	}
}

// TestShutdownRefusesNewWork: a cron that fires mid-shutdown, or a request that got
// in just before the server closed, must not queue work the process is about to drop.
func TestShutdownRefusesNewWork(t *testing.T) {
	r := newQueueRunner()

	if err := r.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown on an idle runner: %v", err)
	}

	var ran int32
	r.enqueue(job{jobRefreshAll, "late", "a late job", func() { atomic.AddInt32(&ran, 1) }})

	r.queueMu.Lock()
	queued := len(r.queue)
	r.queueMu.Unlock()
	if queued != 0 {
		t.Errorf("%d job(s) queued after shutdown, want 0", queued)
	}
	time.Sleep(20 * time.Millisecond)
	if atomic.LoadInt32(&ran) != 0 {
		t.Error("a job enqueued after shutdown ran anyway")
	}
}

// TestShutdownGivesUpOnADeadline: a job that outlasts the grace period must not hold
// the process forever. The caller logs it and exits; the orphaned event is closed on
// the next boot by events.ReconcileRunning.
func TestShutdownGivesUpOnADeadline(t *testing.T) {
	r := newQueueRunner()

	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	r.enqueue(job{jobRefreshAll, "slow", "a slow job", func() {
		close(started)
		<-release
	}})
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := r.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Shutdown = %v, want a deadline error", err)
	}
}

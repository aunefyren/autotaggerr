package process

import (
	"github.com/aunefyren/autotaggerr/logger"
)

// The job queue makes background work serial and visible. Before it, a scan or re-tag
// requested while another was running was dropped on the floor (the atomic guard),
// and an event left "running" by a crash stayed running forever. Now every background
// verb — scans, re-tags, and metadata refreshes — is a job on one queue drained by a
// single worker, so exactly one runs at a time and the rest are shown as pending
// rather than lost.
//
// Serialising everything is also what lets the metadata runner drop its cooperative
// "yield to file work" dance: nothing overlaps any more, so there is nothing to yield
// to (and the scan's own inline refresh can no longer deadlock waiting for the scan
// that is running it). See NewRunner, where yieldTo is left nil.

// jobKind identifies a queued job for dedup and display.
type jobKind string

const (
	jobProcessAll       jobKind = "process_all"
	jobProcessLibrary   jobKind = "process_library"
	jobProcessArtist    jobKind = "process_artist"
	jobRetagAll         jobKind = "retag_all"
	jobRetagLibrary     jobKind = "retag_library"
	jobRetagArtist      jobKind = "retag_artist"
	jobForceRecorrelate jobKind = "force_recorrelate"
	jobRefreshAll       jobKind = "refresh_all"
	jobRefreshVerify    jobKind = "refresh_verify"
	jobRefreshArtist    jobKind = "refresh_artist"
	jobRefreshLibrary   jobKind = "refresh_library"
	jobRepairArtist     jobKind = "repair_artist"
)

// fileWriting reports whether a kind rewrites audio files. File-writing jobs are
// ordered ahead of pending metadata-only jobs, so a scan a user asked for is not stuck
// behind a hours-long refresh — but a job already running is never preempted.
func (k jobKind) fileWriting() bool {
	switch k {
	case jobProcessAll, jobProcessLibrary, jobProcessArtist, jobRetagAll, jobRetagLibrary, jobRetagArtist, jobForceRecorrelate:
		return true
	}
	return false
}

// metadataRefresh reports whether a kind is a metadata pass. Those count entities on
// the mirror runner rather than files in the scan's own counters, which is where
// Status() reads their progress from.
func (k jobKind) metadataRefresh() bool {
	switch k {
	case jobRefreshAll, jobRefreshVerify, jobRefreshArtist, jobRefreshLibrary:
		return true
	}
	return false
}

// job is one unit of queued work: a stable identity for dedup/priority/display, plus
// the closure that performs it.
type job struct {
	kind  jobKind
	key   string // dedup identity; enqueuing a key already queued or running is a no-op
	title string // human label for the queue view
	run   func()
}

// JobView is the API-facing shape of a queued or running job.
type JobView struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
}

func (j job) view() JobView { return JobView{Kind: string(j.kind), Title: j.title} }

// enqueue adds a job unless an identical one (same key) is already running or pending;
// duplicates collapse onto the existing one, so a restart storm or a double-click
// cannot stack redundant runs. File-writing jobs slot ahead of pending metadata jobs.
func (r *Runner) enqueue(j job) {
	// A job accepted during shutdown would be dropped seconds later by Shutdown, and
	// in the meantime would show up as pending. Refuse it where the refusal can be
	// logged with the job's name.
	if r.stopping.Load() {
		logger.Log.Infof("shutting down; not queuing job: %s", j.title)
		return
	}
	r.queueMu.Lock()
	if r.current != nil && r.current.key == j.key {
		r.queueMu.Unlock()
		logger.Log.Debugf("job %q already running; not queued again", j.title)
		return
	}
	for _, q := range r.queue {
		if q.key == j.key {
			r.queueMu.Unlock()
			logger.Log.Debugf("job %q already queued; not queued again", j.title)
			return
		}
	}
	if j.kind.fileWriting() {
		i := 0
		for i < len(r.queue) && r.queue[i].kind.fileWriting() {
			i++
		}
		r.queue = append(r.queue[:i], append([]job{j}, r.queue[i:]...)...)
	} else {
		r.queue = append(r.queue, j)
	}
	views := r.queueViewsLocked()
	r.queueMu.Unlock()

	r.setStatus(func(s *Summary) { s.Queue = views })
	logger.Log.Infof("queued job: %s", j.title)
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// queueViewsLocked snapshots the pending queue. The caller must hold queueMu.
func (r *Runner) queueViewsLocked() []JobView {
	if len(r.queue) == 0 {
		return nil
	}
	out := make([]JobView, len(r.queue))
	for i, q := range r.queue {
		out[i] = q.view()
	}
	return out
}

// worker drains the queue one job at a time. It holds jobMu for the whole of each job,
// which is the same lock the synchronous re-tag paths TryLock — so a background job and
// an interactive re-tag can never write the same file at once.
func (r *Runner) worker() {
	for {
		// Checked before a job is pulled rather than after: a job started here would
		// hold shutdown open for its whole duration, and nothing has been promised
		// about it yet.
		if r.stopping.Load() {
			return
		}
		r.queueMu.Lock()
		if len(r.queue) == 0 {
			r.queueMu.Unlock()
			<-r.wake
			continue
		}
		j := r.queue[0]
		r.queue = r.queue[1:]
		r.current = &j
		views := r.queueViewsLocked()
		r.queueMu.Unlock()

		cur := j.view()
		r.jobMu.Lock()
		r.running.Store(true)
		// Clear the previous job's progress before this one is visible as running, so
		// no window exists where the status reports the last scan's bar under this
		// job's name.
		r.resetProgress()
		r.setStatus(func(s *Summary) { s.CurrentJob = &cur; s.Queue = views })

		runJob(j)

		r.running.Store(false)
		r.jobMu.Unlock()

		r.queueMu.Lock()
		r.current = nil
		views = r.queueViewsLocked()
		r.queueMu.Unlock()
		r.setStatus(func(s *Summary) { s.CurrentJob = nil; s.Queue = views })
	}
}

// runJob executes a job, containing a panic so one bad run cannot take down the worker
// and freeze every job queued behind it.
func runJob(j job) {
	defer func() {
		if rec := recover(); rec != nil {
			logger.Log.Errorf("job %q panicked: %v", j.title, rec)
		}
	}()
	j.run()
}

package artwork

import (
	"context"
	"time"

	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
)

// The queue is what lets one runner serve two very differently shaped requests: a
// collection-wide pass that may run for an hour, and the handful of images a newly
// created artist or album needs right now.
//
// # Targeted work jumps ahead
//
// Without that, adding an artist while a first pass grinds through three thousand
// cold covers puts their twelve images somewhere twenty minutes into the queue —
// which is exactly the wait this package exists to remove. Pending targets are
// therefore checked *between images* of a running pass and drained first, not merely
// picked up when it ends. It is the same reasoning process.Runner's queue uses when
// it puts file-writing work ahead of pending metadata work: a long background job
// must not starve a short one with a user behind it.
//
// # Notifications coalesce
//
// A rebuild that creates two hundred release-groups notifies two hundred times. Each
// notification merges into one pending target set rather than queueing a job, so the
// worker sees one piece of work with two hundred MBIDs in it. That coalescing lives
// here rather than in the caller because every caller would otherwise need its own
// copy of it — the hook sites are spread across collection's create paths and none of
// them knows what the others are doing.

// Warm queues artwork for entities that just entered the collection. It returns
// immediately: the caller is a database write path, and *Add artist* must not take
// forty seconds because eighty covers were fetched inside it.
//
// Safe to call with nothing in it, and safe to call from anywhere — a nil runner is
// a no-op, so a build that has not wired one up does not need nil checks at every
// call site.
//
// Two slices rather than a Targets value, because this is the method
// collection.ArtworkWarmer names and that interface must not drag this package's
// types into the one that calls it.
//
// A single artist arrives here with no release-groups, because at the moment they are
// added they have none — those come later through the discography sync, which
// notifies on its own creates. The hooks compose rather than one needing to know
// about the other.
func (r *Runner) Warm(artists, groups []string) {
	if r == nil {
		return
	}
	// The disabled switch governs *unattended* work, and this is the other half of it
	// — the cron job is gated where it is installed, and without this a user who
	// turned artwork fetching off would still see it happen every time an album
	// arrived. Read per call rather than captured at construction so the setting
	// applies live, which is the tier it is declared at.
	if files.ConfigFile.AutotaggerrArtworkDisabled {
		return
	}
	targets := Targets{Artists: artists, Groups: groups}
	if targets.Empty() {
		return
	}

	r.queueMu.Lock()
	r.hooks.merge(targets)
	r.queueMu.Unlock()

	r.nudge()
}

// RefreshCollection queues a pass over everything the collection holds. force ignores
// cached copies, and is only ever set by the manual button — nothing unattended
// forces, which is the rule the metadata verb already holds to.
//
// Deliberately *not* gated on the disabled switch, unlike Warm. That switch turns off
// the work Autotaggerr does on its own; pressing the button is someone asking for this
// pass now, and a control that silently did nothing because of a setting on another
// page would be the worse failure. The cron job is gated where it is installed.
func (r *Runner) RefreshCollection(force bool) {
	if r == nil {
		return
	}

	r.queueMu.Lock()
	r.full = true
	// A queued pass that anyone asked to force stays forced. Two presses where one
	// ticked the box must not resolve to the cheap reading of the expensive request.
	r.force = r.force || force
	r.queueMu.Unlock()

	r.nudge()
}

// nudge wakes the worker without blocking. The channel is buffered to one, so a
// burst of notifications collapses into a single wake-up — the work itself has
// already been merged.
func (r *Runner) nudge() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// takeHooks removes and returns whatever row creation has notified about.
func (r *Runner) takeHooks() Targets {
	r.queueMu.Lock()
	defer r.queueMu.Unlock()
	pending := r.hooks
	r.hooks = Targets{}
	return pending
}

// takeFull removes and returns the queued collection-wide request, if any.
func (r *Runner) takeFull() (queued, force bool) {
	r.queueMu.Lock()
	defer r.queueMu.Unlock()
	queued, force = r.full, r.force
	r.full, r.force = false, false
	return queued, force
}

// worker drains the queue for the life of the process. Targeted work first, then the
// collection-wide pass — and the pass itself re-checks for targeted work between
// images, so something enqueued during a long pass does not wait for it to end.
func (r *Runner) worker() {
	for range r.wake {
		for {
			if pending := r.takeHooks(); !pending.Empty() {
				r.run(pending, false, false)
				continue
			}
			if queued, force := r.takeFull(); queued {
				targets, err := CollectionTargets(r.db)
				if err != nil {
					logger.Log.Warnf("could not enumerate artwork targets: %s", err.Error())
					continue
				}
				r.run(targets, force, true)
				continue
			}
			break
		}
	}
}

// run executes one target set.
//
// scheduled says this is the collection-wide pass, which always records an Activity
// event. A targeted warm records one **only if it actually fetched an image**:
// adding twenty artists should not put twenty rows in the feed, and a warm that found
// everything already cached did nothing worth reading about. Real work and real
// failures still surface.
func (r *Runner) run(targets Targets, force, scheduled bool) {
	r.running.Store(true)
	defer r.running.Store(false)

	ctx, cancel := context.WithCancel(context.Background())
	r.cancelMu.Lock()
	r.cancel = cancel
	r.cancelMu.Unlock()
	defer func() {
		cancel()
		r.cancelMu.Lock()
		r.cancel = nil
		r.cancelMu.Unlock()
	}()

	providers := r.resolveProviders()
	units := plan(providers, targets)
	if len(units) == 0 {
		return
	}

	title := "Artwork refresh"
	if force {
		title = "Full artwork refresh"
	}

	started := time.Now()
	r.setStatus(func(s *Summary) {
		*s = Summary{Running: true, StartedAt: &started, Title: title, Total: len(units)}
	})

	// The event is opened up front only for the scheduled pass, so the feed shows a
	// long job while it runs rather than only once it finishes. A targeted warm
	// cannot know yet whether it will be worth a row, so it opens one afterwards.
	var ev *models.Event
	var stopProgress func()
	if scheduled {
		ev = events.Begin(r.db, models.EventTypeArtwork, title)
		stopProgress = events.StartProgress(r.db, ev, r.Progress)
	}

	res, cancelled := r.execute(ctx, providers, units, force)

	if stopProgress != nil {
		stopProgress()
	}

	finished := time.Now()
	r.statusMu.Lock()
	r.summary.Running = false
	r.summary.FinishedAt = &finished
	summary := r.summary
	r.statusMu.Unlock()

	if !scheduled {
		if res.Fetched == 0 && res.Errors == 0 {
			return
		}
		ev = events.Begin(r.db, models.EventTypeArtwork, title)
	}
	r.record(ev, started, finished, res, cancelled, summary.Total, summary.Done)
}

// execute warms every unit, draining newly-notified targets first at each boundary.
func (r *Runner) execute(ctx context.Context, providers modules.ArtworkProviders, units []unit, force bool) (Result, bool) {
	res := Result{detailLimit: r.detailRetention}

	for _, u := range units {
		if ctx.Err() != nil {
			return res, true
		}
		// New rows jump the queue. Checked per image rather than per pass, because
		// the pass is the thing that would otherwise make them wait.
		r.drainPending(ctx, providers, &res)
		if ctx.Err() != nil {
			return res, true
		}

		r.warmOne(providers, u, force, &res, true)
		r.setStatus(func(s *Summary) { s.Done++ })
	}

	// Anything that arrived during the final image still belongs to this pass.
	r.drainPending(ctx, providers, &res)
	return res, false
}

// drainPending warms whatever row creation notified about since the last check,
// folding it into the running pass's counters.
//
// Never forced, whatever the surrounding pass is doing: these are entities that were
// created seconds ago and cannot have a stale cached copy, so re-downloading them
// would spend a transfer to replace an image with itself.
func (r *Runner) drainPending(ctx context.Context, providers modules.ArtworkProviders, res *Result) {
	pending := r.takeHooks()
	if pending.Empty() {
		return
	}
	extra := plan(providers, pending)
	if len(extra) == 0 {
		return
	}

	r.setStatus(func(s *Summary) { s.Total += len(extra) })
	for _, u := range extra {
		if ctx.Err() != nil {
			return
		}
		r.warmOne(providers, u, false, res, true)
		r.setStatus(func(s *Summary) { s.Done++ })
	}
}

// Progress reads the live counters for the event-progress flusher.
func (r *Runner) Progress() events.Progress {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	return events.Progress{Total: r.summary.Total, Done: r.summary.Done}
}

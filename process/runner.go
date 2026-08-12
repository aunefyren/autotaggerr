// Package process owns the *Process* verb — walk the library, resolve each file, tag it
// — and the two other verbs that write files, *Tag files* and the re-tag half of a drift
// sync. It wraps the component pipeline (components.ScanLibrary) behind a single serial
// job queue (see queue.go) and a last-run summary, so the scheduled job, the startup run,
// and the API all share one runner and one queue.
//
// It is not the *Scan* verb, despite the ScanLibrary/scanRunner names inherited from
// before the verbs were split: re-deriving the collection from the index is
// collection.Rebuild. See docs/scanning.md for the four verbs and why none cascades.
package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/components"
	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/metadata"
	"github.com/aunefyren/autotaggerr/migration"
	"github.com/aunefyren/autotaggerr/mirror"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// defaultWorkers is the worker count a non-positive concurrency setting falls back
// to, matching the config loader's default.
const defaultWorkers = 4

// retentionOrDefault keeps a non-positive setting from disabling retention entirely.
// Zero would mean "keep nothing", which is never what an unset or mistyped value
// meant, and would silently empty the feed after the first run.
func retentionOrDefault(configured, fallback int) int {
	if configured < 1 {
		return fallback
	}
	return configured
}

// maxErrorFilesRecorded bounds the error-file list stored on a scan event.
const maxErrorFilesRecorded = 100

// Scan phases, reported in Summary.Phase so a running scan is legible. A scan reads
// the metadata cache, walks the libraries, then acts on what changed — these name
// each stage in order.
const (
	PhaseCounting   = "counting"   // walking every root to size the run
	PhaseRefresh    = "refresh"    // re-reading metadata due for a refresh
	PhaseScanning   = "scanning"   // walking libraries and tagging files
	PhaseDrift      = "drift"      // re-tagging files of upstream-changed releases
	PhasePlex       = "plex"       // telling Plex to refresh changed albums
	PhaseMigrations = "migrations" // applying MusicBrainz redirects/deletions
	PhaseCollection = "collection" // re-deriving the collection and mirroring the manager
)

// Summary is the status of the current or most recent scan, plus the job queue.
type Summary struct {
	// Running is whether any background job is executing; CurrentJob names it and Queue
	// lists what is waiting behind it. The scan counters and progress below describe the
	// current job only when it is a scan.
	Running     bool       `json:"running"`
	CurrentJob  *JobView   `json:"current_job,omitempty"`
	Queue       []JobView  `json:"queue,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	Processed   int        `json:"processed"`
	Unchanged   int        `json:"unchanged"`
	Changed     int        `json:"changed"`
	TagsWritten int        `json:"tags_written"`
	Errors      int        `json:"errors"`
	LastError   string     `json:"last_error,omitempty"`

	// Live progress, populated by Status() from the run's atomics while a scan is in
	// flight. Total is every supported file the scan will visit (counted up front);
	// Done climbs as files are processed; Phase names the current stage; Current is
	// the artist folder being worked on. They are the progress bar the feed draws.
	Total   int    `json:"total,omitempty"`
	Done    int    `json:"done,omitempty"`
	Phase   string `json:"phase,omitempty"`
	Current string `json:"current,omitempty"`

	// Indexed is how many files are in `library_items` right now. It is not about the
	// current run at all: it is the precondition the three cheap verbs share, and the
	// UI reads it to say "run Process first" on a button rather than letting someone
	// press Scan or Tag files on an empty index and get an honest zero back.
	//
	// Counted per call rather than tracked, because a run is not the only writer —
	// pruning, a library being removed and a manual attach all move it — and a stale
	// count would disable a button that works.
	Indexed int `json:"indexed"`
}

// Runner owns scan execution and status. A single Runner instance is shared by
// main (cron + startup) and the API.
type Runner struct {
	db      *gorm.DB
	plex    *modules.PlexClient
	version string
	// concurrency is atomic because the settings page can change it while the runner
	// is alive: a scan reads it when it starts a walk, and the API writes it from a
	// request goroutine.
	concurrency atomic.Int64

	// meta is the MusicBrainz metadata source the runner passes to the collection
	// derivations it drives (SyncArtist). Defaulted to the real source in NewRunner;
	// a test could swap it for a fake without threading it through the constructor.
	meta metadata.MetadataSource

	// refresh is the metadata verb. The scan owns file writes and delegates every
	// MusicBrainz read to it, so "refresh this artist" and the scan's own refresh
	// stage cannot drift apart in what they fetch.
	refresh *mirror.Runner

	// Activity retention: how many runs the feed keeps, and how many per-file detail
	// rows one event stores. Resolved from config in NewRunner and shared with the
	// mirror runner, which prunes the same tables. Not atomic like concurrency —
	// these are read once when a run opens its collector, so a settings change
	// applies from the next run rather than mid-walk, which is the only reading under
	// which "showing 500 of 3120" stays true for the rows already collected.
	eventRetention  int
	detailRetention int

	running atomic.Bool // a job is currently executing
	// stopping is set by Shutdown. From then on the queue accepts nothing and the
	// worker starts no further job — what is already running is left to finish.
	stopping atomic.Bool
	jobMu    sync.Mutex // held for the duration of each job; TryLock'd by interactive re-tags

	// The job queue: a single worker (started in NewRunner) drains `queue` one at a
	// time, so every background verb is serial and visible. `current` is the executing
	// job; `wake` nudges the worker when something is enqueued.
	queueMu sync.Mutex
	queue   []job
	current *job
	wake    chan struct{}

	statusMu sync.Mutex
	summary  Summary

	// Live scan progress, kept in atomics so the per-file callback never contends on
	// statusMu across a pool of workers. Status() overlays these onto the summary
	// while a scan runs, and the event-progress flusher reads them through
	// progressSnapshot; the run resets them at its start.
	progTotal   atomic.Int64
	progDone    atomic.Int64
	progPhase   atomic.Pointer[string]
	progCurrent atomic.Pointer[string]
}

// NewRunner builds a runner and starts its queue worker. plex may be nil (Plex
// refresh is then skipped).
//
// The metadata runner is constructed here rather than passed in. It is wired with a
// nil yieldTo: the queue serialises every background job, so a metadata pass never
// runs alongside file work and has nothing to yield to. Wiring it to yield here would
// also deadlock a scan's own inline refresh — the scan holds the running flag the
// yield waits on. Callers that need the refresh verb directly take it back via
// Refresher.
func NewRunner(db *gorm.DB, plex *modules.PlexClient, cfg models.ConfigStruct) *Runner {
	r := &Runner{
		db:              db,
		plex:            plex,
		version:         cfg.AutotaggerrVersion,
		meta:            modules.NewMetadataSource(),
		wake:            make(chan struct{}, 1),
		eventRetention:  retentionOrDefault(cfg.AutotaggerrEventRetention, models.DefaultEventRetention),
		detailRetention: retentionOrDefault(cfg.AutotaggerrEventDetailRetention, models.DefaultEventDetailRetention),
	}
	r.SetConcurrency(cfg.AutotaggerrProcessConcurrency)
	r.refresh = mirror.NewRunner(db, nil, cfg)
	go r.worker()
	return r
}

// Refresher exposes the metadata runner so the cron job and the API can drive the
// refresh verb without going through a scan.
func (r *Runner) Refresher() *mirror.Runner { return r.refresh }

// SetConcurrency changes how many files a scan processes in parallel. A running scan
// keeps the worker count it started with — the pool is sized when the walk begins —
// so the new value applies from the next one. A non-positive value falls back to the
// same default the config loader uses, so a bad input never wedges the pool at zero.
func (r *Runner) SetConcurrency(workers int) {
	if workers < 1 {
		workers = defaultWorkers
	}
	r.concurrency.Store(int64(workers))
}

// Concurrency reports the current worker count.
func (r *Runner) Concurrency() int { return int(r.concurrency.Load()) }

// Status returns a copy of the current/last scan summary plus the queue. Running is
// taken from the atomic so it reflects both queue jobs and the interactive re-tags
// that never touch the summary; while a job runs the live progress of whatever is
// running is folded in, so a scan shows a moving bar without the per-file path taking
// the status lock.
//
// Which progress that is depends on the job. Only a scan writes the atomics; a
// metadata refresh counts entities on the mirror runner, so its progress is read from
// there — the same counters that runner flushes onto its event row, so the status
// banner and the feed cannot disagree about a pass they both describe.
func (r *Runner) Status() Summary {
	r.statusMu.Lock()
	s := r.summary
	r.statusMu.Unlock()
	s.Running = r.running.Load()
	if s.Running {
		p := r.progressSnapshot()
		if r.refresh != nil && s.CurrentJob != nil && jobKind(s.CurrentJob.Kind).metadataRefresh() {
			p = r.refresh.Progress()
		}
		s.Total, s.Done, s.Phase, s.Current = p.Total, p.Done, p.Phase, p.Current
	}
	if r.db != nil {
		var indexed int64
		if err := r.db.Model(&models.LibraryItem{}).Count(&indexed).Error; err != nil {
			// Status must always answer; a failed count leaves Indexed at zero, which
			// the UI reads as "run Process first" — the safe way to be wrong, since it
			// explains rather than blocks anything that was going to work.
			logger.Log.Warnf("failed to count indexed files for status: %s", err.Error())
		}
		s.Indexed = int(indexed)
	}
	return s
}

// resetProgress clears the live progress atomics. Every job starts by calling it, so
// no run can inherit the one before it: the atomics are written by scans alone, and a
// status poll during a metadata refresh or an interactive re-tag would otherwise
// report the last scan's final numbers — a full bar, its closing phase and the artist
// it ended on — as though they belonged to the job actually running.
func (r *Runner) resetProgress() {
	r.progTotal.Store(0)
	r.progDone.Store(0)
	r.setPhase("")
	r.setCurrent("")
}

// setPhase records the stage a running scan is in, for Summary.Phase and the event
// progress row.
func (r *Runner) setPhase(phase string) { r.progPhase.Store(&phase) }

// setCurrent records what the scan is working on right now (an artist folder). Under
// concurrent workers this is the most recently started file's artist — a liveness
// indicator, not a strict cursor.
func (r *Runner) setCurrent(current string) { r.progCurrent.Store(&current) }

// progressSnapshot reads the live progress atomics into an events.Progress. It is the
// single source both Status() and the event-progress flusher read, so the feed and
// the status endpoint can never disagree.
func (r *Runner) progressSnapshot() events.Progress {
	phase, current := "", ""
	if p := r.progPhase.Load(); p != nil {
		phase = *p
	}
	if c := r.progCurrent.Load(); c != nil {
		current = *c
	}
	return events.Progress{
		Total:   int(r.progTotal.Load()),
		Done:    int(r.progDone.Load()),
		Phase:   phase,
		Current: current,
	}
}

// artistFromPath is the artist folder a file sits under, given the library root the
// path convention (<root>/<ARTIST>/<ALBUM>/...) is anchored on. Empty when the path
// is not under root or has no artist segment.
func artistFromPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return ""
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) == 0 || parts[0] == "" || parts[0] == "." || parts[0] == ".." {
		return ""
	}
	return parts[0]
}

// Running reports whether a scan is in progress.
func (r *Runner) Running() bool { return r.running.Load() }

// Wait blocks until the queue has drained and no job is executing. It is meant for
// graceful shutdown and for tests: a background job that outlives its caller keeps
// writing to a database whose temp directory may already be gone, which surfaces as
// unrelated "readonly database" noise. It does not stop new work from being enqueued —
// it simply waits for what is already queued or running to finish.
func (r *Runner) Wait() {
	for {
		r.queueMu.Lock()
		idle := r.current == nil && len(r.queue) == 0
		r.queueMu.Unlock()
		if idle && !r.running.Load() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// Shutdown stops the queue and waits for the job in flight, returning ctx's error if
// that job outlasts the deadline.
//
// It is not Wait. Wait drains the whole queue, which at shutdown is the wrong
// promise: a full scan sitting behind the running job would hold the process open for
// hours after someone asked it to stop. So the pending jobs are dropped — none of
// them has started, none has an event, and every verb here is re-runnable by design —
// and only the one already executing is given time to finish, because *that* one has
// written files and opened an event.
//
// Nothing is cancelled mid-job: the runner has no cancellation to thread through a
// tag write, and interrupting one is how a file ends up half-written. A job that
// outlasts the deadline is left to the process exiting under it, which is the crash
// case the caller already survives — events.ReconcileRunning closes its event on the
// next boot.
func (r *Runner) Shutdown(ctx context.Context) error {
	r.stopping.Store(true)

	r.queueMu.Lock()
	dropped := len(r.queue)
	r.queue = nil
	views := r.queueViewsLocked()
	r.queueMu.Unlock()
	if dropped > 0 {
		logger.Log.Infof("shutting down: dropped %d queued job(s) that had not started", dropped)
		r.setStatus(func(s *Summary) { s.Queue = views })
	}

	// Wake the worker so it observes `stopping` and parks instead of sitting on an
	// empty queue.
	select {
	case r.wake <- struct{}{}:
	default:
	}

	for {
		if !r.running.Load() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// Target is one unit of scan work: a library, and optionally the folders inside it
// to walk. Empty Roots means the whole library.
type Target struct {
	Library models.Library
	Roots   []string
}

// full reports whether this target covers its library in its entirety — the only
// case that may claim the library was scanned.
func (t Target) full() bool { return len(t.Roots) == 0 }

// Scope is what a run covers: which targets, what to call it in the Activity feed,
// and any extra detail worth recording about why the run was narrowed.
//
// Every scan goes through a Scope, whole-library runs included, so a partial scan is
// not a second code path with its own bugs — it is the same run with fewer folders in
// it. Scopes narrower than an artist (a release-group, a single album folder) need
// only a new constructor, not new machinery.
type Scope struct {
	Title   string
	Targets []Target
	Detail  map[string]any
	// Force re-processes every file in the scope, bypassing the unchanged-on-disk
	// skip. It is set only by the manager repair verb (ForceRecorrelate*), where the
	// whole point is to pull a changed Lidarr release selection down onto files whose
	// bytes never moved.
	Force bool
}

// ErrNothingToProcess reports a scope that resolved to no folders on disk. It is a
// refusal, not a failure: running it would report "0 files processed" and look like
// the scan silently did nothing.
var ErrNothingToProcess = errors.New("nothing to process: no indexed files found — process a library first")

// LibraryScope covers whole libraries, which is what the scheduled job, the startup
// run, and a per-library scan all want.
func LibraryScope(libraries []models.Library) Scope {
	title := "Processing all libraries"
	if len(libraries) == 1 {
		title = "Processing " + libraries[0].Name
	}
	targets := make([]Target, 0, len(libraries))
	for _, library := range libraries {
		targets = append(targets, Target{Library: library})
	}
	return Scope{Title: title, Targets: targets}
}

// ArtistScope covers just one artist's folders, derived from where their indexed
// files actually sit (see collection.ArtistTargets). It returns ErrNothingToProcess when
// the artist has no files yet — there is no folder to walk, and the artist's own name
// is not a reliable guess at what the folder is called.
func (r *Runner) ArtistScope(artistMBID string) (Scope, error) {
	var artist models.CollectionArtist
	if err := r.db.Where("mb_id = ?", artistMBID).First(&artist).Error; err != nil {
		return Scope{}, err
	}

	found, err := collection.ArtistTargets(r.db, artistMBID)
	if err != nil {
		return Scope{}, err
	}
	if len(found) == 0 {
		return Scope{}, ErrNothingToProcess
	}

	return buildScope("Processing "+artist.Name,
		map[string]any{"artist": artist.Name, "artist_mb_id": artistMBID}, found), nil
}

// ReleaseGroupScope covers the album folders of one release-group, for the narrowest
// force re-correlate. It returns ErrNothingToProcess when nothing is owned of the group,
// and the DB error when the release-group is unknown.
func (r *Runner) ReleaseGroupScope(rgMBID string) (Scope, error) {
	var rg models.CollectionReleaseGroup
	if err := r.db.Where("mb_id = ?", rgMBID).First(&rg).Error; err != nil {
		return Scope{}, err
	}
	found, err := collection.ReleaseGroupTargets(r.db, rgMBID)
	if err != nil {
		return Scope{}, err
	}
	if len(found) == 0 {
		return Scope{}, ErrNothingToProcess
	}
	title := rg.Title
	if title == "" {
		title = rgMBID
	}
	return buildScope("Processing "+title,
		map[string]any{"release_group": title, "release_group_mb_id": rgMBID}, found), nil
}

// buildScope groups per-folder targets by library into scan Targets and records the
// walked folders in Detail. Shared by every partial scope (artist, release-group) so
// they resolve targets identically: one Target per library, since each walk correlates
// against its own library root.
func buildScope(title string, detail map[string]any, found []collection.ArtistTarget) Scope {
	byLibrary := map[uuid.UUID]int{}
	targets := make([]Target, 0, 1)
	folders := make([]string, 0, len(found))
	for _, f := range found {
		folders = append(folders, f.Path)
		if idx, ok := byLibrary[f.Library.ID]; ok {
			targets[idx].Roots = append(targets[idx].Roots, f.Path)
			continue
		}
		byLibrary[f.Library.ID] = len(targets)
		targets = append(targets, Target{Library: f.Library, Roots: []string{f.Path}})
	}
	if detail == nil {
		detail = map[string]any{}
	}
	detail["folders"] = folders
	return Scope{Title: title, Targets: targets, Detail: detail}
}

// RunAll queues a scan of every enabled library. Deduped against an identical scan
// already queued or running.
func (r *Runner) RunAll() {
	r.enqueue(job{jobProcessAll, "process_all", "Process all libraries", r.runAllNow})
}

// runAllNow is the executor: it loads the libraries at run time (not enqueue time, so a
// library added while queued is included) and scans them.
func (r *Runner) runAllNow() {
	var libraries []models.Library
	if err := r.db.Where("enabled = ?", true).Order("name").Find(&libraries).Error; err != nil {
		logger.Log.Error("failed to load libraries from database. error: " + err.Error())
		return
	}
	if len(libraries) == 0 {
		logger.Log.Info("no enabled libraries configured; nothing to scan")
		return
	}
	r.runScope(LibraryScope(libraries))
}

// RunLibrary queues a scan of one library by ID. It returns an error only when the
// library cannot be loaded now; the scan itself runs later on the queue worker.
func (r *Runner) RunLibrary(id uuid.UUID) error {
	var library models.Library
	if err := r.db.First(&library, "id = ?", id).Error; err != nil {
		return err
	}
	r.enqueue(job{jobProcessLibrary, "process_library:" + id.String(), "Processing " + library.Name, func() {
		r.runScope(LibraryScope([]models.Library{library}))
	}})
	return nil
}

// RunArtist queues a scan of one artist's folders. It returns an error when the scope
// cannot be resolved (unknown artist, no files on disk); the scan itself runs later.
func (r *Runner) RunArtist(artistMBID string) error {
	scope, err := r.ArtistScope(artistMBID)
	if err != nil {
		return err
	}
	r.Run(scope)
	return nil
}

// Run queues a pre-resolved scope. The API resolves the scope first (so "this artist
// has no files" is answered immediately) and hands the result here; the dedup key comes
// from the scope title, so a second identical request collapses onto the first.
func (r *Runner) Run(scope Scope) {
	key := "process_artist:" + scope.Title
	if mbid, ok := scope.Detail["artist_mb_id"].(string); ok && mbid != "" {
		key = "process_artist:" + mbid
	}
	r.enqueue(job{jobProcessArtist, key, scope.Title, func() { r.runScope(scope) }})
}

// ForceRecorrelateArtist repairs an artist whose files diverged from what their
// manager now says — the case a plain scan cannot fix. It resolves the artist's
// folders, marks the scope Force (so every file is re-processed even though nothing
// changed on disk), and enqueues it. The executor busts the Lidarr caches and drops
// the Pinned flag on the artist's Lidarr-governed files first, so the re-walk asks
// Lidarr fresh and writes its current release instead of reusing a stale correlation
// or a hand-attached pin.
//
// It returns an error only when the scope cannot be resolved now (unknown artist, no
// files on disk); the work itself runs later on the queue worker.
func (r *Runner) ForceRecorrelateArtist(artistMBID string) error {
	scope, err := r.ArtistScope(artistMBID)
	if err != nil {
		return err
	}
	name, _ := scope.Detail["artist"].(string)
	scope.Title = forceTitle(name, "artist")
	r.enqueueForceRecorrelate("force_recorrelate:"+artistMBID, scope)
	return nil
}

// ForceRecorrelateReleaseGroup is ForceRecorrelateArtist narrowed to one album — the
// least disruptive repair when only a single release drifted. Same mechanics: bust the
// caches, drop Lidarr-governed pins in scope, force a re-walk.
func (r *Runner) ForceRecorrelateReleaseGroup(rgMBID string) error {
	scope, err := r.ReleaseGroupScope(rgMBID)
	if err != nil {
		return err
	}
	title, _ := scope.Detail["release_group"].(string)
	scope.Title = forceTitle(title, "release group")
	r.enqueueForceRecorrelate("force_recorrelate_rg:"+rgMBID, scope)
	return nil
}

// ForceRecorrelateLibrary re-correlates an entire library — the widest repair, for when
// a Lidarr instance was re-pointed and everything under it needs to follow. Roots are
// empty (the whole library), so prepareForceRecorrelate clears every Lidarr-governed pin
// in it.
func (r *Runner) ForceRecorrelateLibrary(libraryID uuid.UUID) error {
	var library models.Library
	if err := r.db.First(&library, "id = ?", libraryID).Error; err != nil {
		return err
	}
	scope := LibraryScope([]models.Library{library})
	scope.Title = "Re-correlate " + library.Name
	r.enqueueForceRecorrelate("force_recorrelate_lib:"+libraryID.String(), scope)
	return nil
}

// forceTitle labels a re-correlate run "Re-correlate <name>", or a generic fallback when
// the name is unknown.
func forceTitle(name, kind string) string {
	if name != "" {
		return "Re-correlate " + name
	}
	return "Re-correlate " + kind
}

// enqueueForceRecorrelate queues a forced re-walk of a scope: prepareForceRecorrelate
// busts the Lidarr caches and drops Lidarr-governed pins in scope, then runScope runs
// with Force set so shouldSkip is bypassed. Shared by all three force verbs.
func (r *Runner) enqueueForceRecorrelate(key string, scope Scope) {
	scope.Force = true
	r.enqueue(job{jobForceRecorrelate, key, scope.Title, func() {
		r.prepareForceRecorrelate(scope)
		r.runScope(scope)
	}})
}

// prepareForceRecorrelate readies a forced scope so the re-walk resolves identity
// from the manager rather than from anything stale. It invalidates the Lidarr caches
// (whole-cache; the next lookup re-fetches the current selection) and clears the
// Pinned flag on every Lidarr-governed file in scope, because a pin makes the pipeline
// reuse a hand-chosen correlation instead of asking the manager. Native libraries are
// left untouched: their pins are the only identity they have, and there is no other
// authority to defer to.
func (r *Runner) prepareForceRecorrelate(scope Scope) {
	modules.LidarrInvalidateCaches()

	for _, target := range scope.Targets {
		manager, _, err := components.BuildForLibrary(r.db, target.Library)
		if err != nil {
			logger.Log.Warnf("force re-correlate: cannot resolve manager for library %q: %s", target.Library.Name, err.Error())
			continue
		}
		if manager.Type() != models.ManagerTypeLidarr {
			continue
		}

		var pinned []models.LibraryItem
		if err := r.db.Where("library_id = ? AND pinned = ?", target.Library.ID, true).Find(&pinned).Error; err != nil {
			logger.Log.Warnf("force re-correlate: failed to load pinned items for %q: %s", target.Library.Name, err.Error())
			continue
		}

		ids := make([]uuid.UUID, 0, len(pinned))
		for _, item := range pinned {
			if pathInScope(item.Path, target.Roots) {
				ids = append(ids, item.ID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		if err := r.db.Model(&models.LibraryItem{}).Where("id IN ?", ids).Update("pinned", false).Error; err != nil {
			logger.Log.Warnf("force re-correlate: failed to clear %d pin(s) in %q: %s", len(ids), target.Library.Name, err.Error())
			continue
		}
		logger.Log.Infof("force re-correlate: cleared %d manual pin(s) in %q so Lidarr can re-correlate them", len(ids), target.Library.Name)
	}
}

// pathInScope reports whether a file path falls under one of the scope roots. Empty
// roots means the whole library is in scope, so everything qualifies. A root only
// matches at a path boundary, so "/music/Ye" never swallows "/music/Yellowcard".
func pathInScope(path string, roots []string) bool {
	if len(roots) == 0 {
		return true
	}
	clean := filepath.Clean(path)
	for _, root := range roots {
		root = filepath.Clean(root)
		if clean == root || strings.HasPrefix(clean, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// scopeFilter answers whether an indexed file falls inside a run's scope.
//
// A scan is defined by the folders it walks, but two of its stages do not walk
// anything: the refresh stage picks releases off the metadata cache by TTL, and the
// drift stage re-tags every indexed file of a release that changed. Neither has a
// folder in hand, so neither consulted the scope at all — which is how pressing
// *Scan* on one artist could spend the whole MusicBrainz budget on the collection
// and then rewrite files belonging to artists nobody asked about.
type scopeFilter struct {
	// roots maps a library to the folders in scope within it. Nil means the run was
	// not narrowed and everything is in scope; a library present with no roots means
	// the whole of it is.
	roots map[uuid.UUID][]string
}

// newScopeFilter builds the filter for a scope. A scope where no target names any
// roots — a full scan, or a whole-library one — admits everything, which leaves the
// scheduled run behaving exactly as before: it is the pass that is *supposed* to
// carry the collection's drift, and narrowing it would leave releases nothing ever
// refreshed.
func newScopeFilter(scope Scope) scopeFilter {
	narrowed := false
	for _, target := range scope.Targets {
		if !target.full() {
			narrowed = true
			break
		}
	}
	if !narrowed {
		return scopeFilter{}
	}

	roots := make(map[uuid.UUID][]string, len(scope.Targets))
	for _, target := range scope.Targets {
		roots[target.Library.ID] = append(roots[target.Library.ID], target.Roots...)
	}
	return scopeFilter{roots: roots}
}

// all reports whether the filter admits every file, which lets callers skip the
// index queries entirely.
func (f scopeFilter) all() bool { return f.roots == nil }

// admits reports whether one indexed file is inside the scope. A file in a library
// this run does not touch is out; within a library pathInScope decides, and a
// library recorded with no roots is in scope in its entirety.
func (f scopeFilter) admits(item models.LibraryItem) bool {
	if f.all() {
		return true
	}
	roots, ok := f.roots[item.LibraryID]
	if !ok {
		return false
	}
	return pathInScope(item.Path, roots)
}

// keep returns the items inside the scope. The unnarrowed case returns the slice
// untouched, so a full scan pays nothing for this.
func (f scopeFilter) keep(items []models.LibraryItem) []models.LibraryItem {
	if f.all() {
		return items
	}
	kept := make([]models.LibraryItem, 0, len(items))
	for _, item := range items {
		if f.admits(item) {
			kept = append(kept, item)
		}
	}
	return kept
}

// releasesInScope is the set of MusicBrainz release IDs the scope's own files point
// at — what the refresh stage is allowed to re-read on a narrowed run. Derived from
// the index rather than from the collection tables, because a scope is a set of
// folders and only library_items knows which release a folder's files resolved to.
func (r *Runner) releasesInScope(filter scopeFilter) (map[string]bool, error) {
	if filter.all() {
		return nil, nil
	}

	inScope := map[string]bool{}
	for libraryID := range filter.roots {
		var items []models.LibraryItem
		if err := r.db.Select("path", "library_id", "mb_release_id").
			Where("library_id = ? AND mb_release_id <> ''", libraryID).
			Find(&items).Error; err != nil {
			return nil, err
		}
		for _, item := range items {
			if filter.admits(item) {
				inScope[item.MBReleaseID] = true
			}
		}
	}
	return inScope, nil
}

// narrowDue drops releases outside the scope from the due-for-refresh list, so a
// per-artist scan re-reads that artist's expired releases and leaves the rest for a
// run that covers them. The list comes from the whole release cache, and on a cold
// cache every entry on it costs a second of the global rate limit — which made the
// cheapest-looking button in the app quietly the most expensive.
//
// A failure to resolve the scope falls back to the full list rather than to nothing:
// refreshing too much wastes rate limit, refreshing nothing silently stops the drift
// detection this stage exists for. The files that refresh leads to are gated
// separately, by the same filter, so the fallback cannot widen what gets written.
func (r *Runner) narrowDue(due []string, filter scopeFilter) []string {
	if filter.all() {
		return due
	}
	inScope, err := r.releasesInScope(filter)
	if err != nil {
		logger.Log.Warnf("failed to resolve the releases in scope, refreshing everything due: %s", err.Error())
		return due
	}

	kept := make([]string, 0, len(due))
	for _, mbID := range due {
		if inScope[mbID] {
			kept = append(kept, mbID)
		}
	}
	if dropped := len(due) - len(kept); dropped > 0 {
		logger.Log.Infof("metadata refresh narrowed to this run's scope: %d of %d due release(s) re-read, %d left to a wider run",
			len(kept), len(due), dropped)
	}
	return kept
}

// scopeIsFull reports whether a run covered at least one library in its entirety,
// which is what earns the end-of-run manager mirror (see syncManagers). It reuses
// Target.full, the same predicate that decides whether a library may claim it was
// scanned — a run narrow enough not to update a library's scan time is narrow enough
// not to be worth a whole-collection mirror either.
func scopeIsFull(scope Scope) bool {
	for _, target := range scope.Targets {
		if target.full() {
			return true
		}
	}
	return false
}

// runScope executes a scan of a scope. It is only ever called by the queue worker,
// which holds jobMu and the running flag for its duration — so this body takes no guard
// of its own.
func (r *Runner) runScope(scope Scope) {
	start := time.Now()
	// Reset the scan counters and progress for this run without disturbing the
	// queue view (CurrentJob / Queue) the worker set.
	r.setStatus(func(s *Summary) {
		s.StartedAt = &start
		s.FinishedAt = nil
		s.Processed, s.Unchanged, s.Changed, s.TagsWritten, s.Errors = 0, 0, 0, 0, 0
		s.LastError = ""
	})
	logger.Log.Info("library process task starting...")

	event := events.Begin(r.db, models.EventTypeProcess, scope.Title)

	// Reset the live progress atomics and start the flusher that writes them onto the
	// running event, so the Activity feed can draw a bar. Stopped before Finish, whose
	// Save must not race the flusher for the row.
	r.resetProgress()
	r.setPhase(PhaseCounting)
	stopProgress := events.StartProgress(r.db, event, r.progressSnapshot)

	// Per-run MusicBrainz accounting. Only the `fetches` figure pays the rate
	// limiter, so this is what makes "why did this scan take seven hours" answerable
	// from the Activity feed instead of by guesswork.
	modules.MusicbrainzResetStats()

	refreshSet := modules.NewAlbumRefreshSet(nil)
	detail := components.NewDetailCollector(r.detailRetention)
	var processed, unchanged, tagsWritten, removed int
	var errorFiles []string
	libraryNames := make([]string, 0, len(scope.Targets))

	// Size the progress bar up front by counting the files the scan will visit. The
	// same walk WalkAndProcess does per root, summed across every target, so the bar
	// tracks the whole scan rather than resetting per library. One extra disk walk,
	// against the far heavier tag reads the scan is about to do.
	//
	// It records its own event because it is a disk walk that can take minutes on a
	// cold library, and it used to happen inside the refresh phase with the bar at
	// 0 of 0 — a run's first minutes reported as nothing at all.
	totalFiles := r.countFiles(scope, event)
	r.progTotal.Store(int64(totalFiles))

	// What this run is allowed to touch through the index rather than through the
	// walk. Unnarrowed for a full or whole-library scan; see scopeFilter.
	filter := newScopeFilter(scope)

	// Refresh stage. A scan reads files through the *cache*, so without this it
	// would tag from a week-old copy and never notice a release that changed
	// upstream. Only expired entries are re-read, which is what keeps a nightly
	// scan from re-fetching the whole collection — and on a narrowed run, only the
	// expired entries this scope's own files point at, so a one-artist button costs
	// one artist's worth of rate limit.
	//
	// Run inline: this holds the file-writing guard already, and a scan waiting on
	// a scheduled refresh it is perfectly able to run alongside would be absurd.
	//
	// It records its own event under this run. It is minutes of rate-limited work,
	// often the first minutes, and reporting it as a `details.refresh` blob on the run
	// meant the stage a user was actually waiting on had no row to open.
	r.setPhase(PhaseRefresh)
	due := r.narrowDue(modules.MusicbrainzDueForRefresh(), filter)
	refreshResult := r.refresh.RunStage(context.Background(), mirror.DueScope(due), event)

	fullScan := scopeIsFull(scope)

	// The tagging stage: everything this run writes to disk, under one event. The walk
	// below finds files changed on disk; the drift pass after it rewrites files whose
	// release changed upstream. They ran as two events for a while, which put the
	// walk's counters beside a row whose only content was a list of release MBIDs the
	// metadata stage had already listed. One activity, two phases in its detail.
	r.setPhase(PhaseScanning)
	tagEvent := events.BeginChild(r.db, event, models.EventTypeTagFiles, taggingActivityTitle)
	for _, target := range scope.Targets {
		library := target.Library
		libraryNames = append(libraryNames, library.Name)
		logger.Log.Info("processing library: " + library.Path)

		// Advance the live counter per file and name the artist folder being worked
		// on. Runs on scan worker goroutines, so it only touches the lock-free atomics.
		root := library.Path
		onFile := func(path string) {
			r.progDone.Add(1)
			if a := artistFromPath(root, path); a != "" {
				r.setCurrent(a)
			}
		}
		c, u, tw, errs, err := components.ScanLibraryRoots(r.db, library, target.Roots, r.plex, refreshSet, detail, r.version, r.Concurrency(), scope.Force, onFile)
		if err != nil {
			logger.Log.Error("failed to process library '" + library.Path + "'. error: " + err.Error())
			r.setStatus(func(s *Summary) { s.LastError = err.Error() })
			continue
		}
		processed += c
		unchanged += u
		tagsWritten += tw
		errorFiles = append(errorFiles, errs...)

		// Only after the walk succeeded: a walk that failed partway is exactly the case
		// where "the file is not there" means "we could not look", and pruning on it
		// would delete rows for files that are present. A failure here is logged and
		// the run carries on — a stale index row is a wrong count, not a lost file.
		n, err := pruneMissingItems(r.db, library, target.Roots)
		if err != nil {
			logger.Log.Warnf("failed to prune missing files in %q: %s", library.Name, err.Error())
		}
		removed += n

		// Only a whole-library run may claim the library was scanned. A scan of one
		// artist's folder says nothing about the rest of the library, and letting it
		// bump the timestamp would make a library look freshly scanned while most of
		// it had not been read for weeks.
		if target.full() {
			now := time.Now()
			if err := r.db.Model(&models.Library{}).Where("id = ?", library.ID).Update("last_scan", now).Error; err != nil {
				logger.Log.Warnf("failed to record last scan time for library %s: %s", library.ID, err.Error())
			}
		}
		logger.Log.Info("processed library: " + library.Path)
	}

	// Drift re-tag. shouldSkip drops files whose size and mtime are unchanged, which is
	// every file of a release that changed only *upstream* — so the walk above cannot
	// have caught them. This is where the refresh stage's findings are acted on, and it
	// is here rather than in the refresh verb because writing files is the scan's job,
	// not the refresh's.
	//
	// It writes only inside the scope. A release is not confined to one folder — the
	// same edition can be held twice, in two libraries — so the filter is applied per
	// file, not per release.
	//
	// Its rows join the walk's on the same tagging event, phase-tagged so the detail
	// list keeps "found on disk" and "changed upstream" apart.
	drift := releaseRefresh{}
	if len(refreshResult.ChangedReleases) > 0 {
		r.setPhase(PhaseDrift)
		logger.Log.Infof("%d release(s) changed upstream; re-tagging their files", len(refreshResult.ChangedReleases))
		driftDetail := components.NewDetailCollector(r.detailRetention)
		drift = r.retagReleases(refreshResult.ChangedReleases, refreshSet, driftDetail, filter)
		tagsWritten += drift.retagged
		errorFiles = append(errorFiles, drift.errorFiles...)
		detail.Adopt(driftDetail, models.EventItemPhaseDrift)
	}

	// The tagging stage is closed out before the stages that follow it, so its row
	// carries the file counters and the per-file detail rather than the run — the run's
	// own event used to be these counters, which is why it read as a tagging event with
	// the other stages missing.
	r.finishTagging(tagEvent, taggingResult{
		processed:   processed,
		unchanged:   unchanged,
		tagsWritten: tagsWritten,
		removed:     removed,
		errorFiles:  errorFiles,
		drift:       drift,
		libraries:   libraryNames,
	}, detail)

	r.setPhase(PhasePlex)
	r.flushPlex(refreshSet, event)

	end := time.Now()
	changed := processed - unchanged
	logger.Log.Infof("library process task finished. %d files processed. %d files not processed because of errors. %d files changed. %d tags written",
		processed, len(errorFiles), changed, tagsWritten)
	if len(errorFiles) > 0 {
		logger.Log.Warnf("files that failed to be processed:\n%s", strings.Join(errorFiles, "\n"))
	}
	logger.Log.Info("process took: " + end.Sub(start).String())

	// Running is not cleared here: the queue worker owns it (and clears it after this
	// returns), so a scan finishing does not mark the runner idle while a queued job is
	// about to start.
	r.setStatus(func(s *Summary) {
		s.FinishedAt = &end
		s.Processed = processed
		s.Unchanged = unchanged
		s.Changed = changed
		s.TagsWritten = tagsWritten
		s.Errors = len(errorFiles)
	})

	// Record the scan as an Activity event.
	status := models.EventStatusOK
	if len(errorFiles) > 0 {
		status = models.EventStatusError
	}
	recorded := errorFiles
	if len(recorded) > maxErrorFilesRecorded {
		recorded = recorded[:maxErrorFilesRecorded]
	}
	mbStats := modules.MusicbrainzStatsSnapshot()
	logger.Log.Infof("MusicBrainz lookups: %d served from cache, %d coalesced onto an in-flight fetch, %d fetched",
		mbStats.CacheHits, mbStats.Coalesced, mbStats.Fetches)

	// Repair before the queue is drained, never after. A release-group deletion is
	// held for review until the manager has been asked about it, so this stage is what
	// unblocks the drain below — and running it the other way round would retire albums
	// the refresh was about to correct. Full runs only: a one-artist button should not
	// set the whole collection refreshing in Lidarr.
	if scopeIsFull(scope) {
		r.repairGhostAlbums(event)
	}

	// A scan fetches releases just as a sync does, so it detects redirects and
	// deletions too — draining the queue here keeps a cold scan from leaving them
	// for whenever the next sync happens to run.
	r.setPhase(PhaseMigrations)
	migrations := r.applyMigrations(event)

	// Re-derive the collection, then refresh the manager mirror against it. Both run
	// before the event is finished so what they changed can ride it — a rebuild that
	// moved an album between artists is news, and reporting it after the run it
	// belongs to has been written means reporting it nowhere. Order matters: see
	// syncManagers.
	r.setPhase(PhaseCollection)
	rebuild := r.rebuildCollection(event)
	mirrored := map[string]any{}
	if fullScan {
		syncedArtists, syncedAlbums := r.syncManagers(event)
		mirrored["artists"] = syncedArtists
		mirrored["albums"] = syncedAlbums
	}

	// Stop the flusher before Finish writes the row: its final snapshot lands the last
	// progress, and Finish then Saves the event without racing an in-flight update.
	stopProgress()

	summary := scanSummaryLine(processed, changed, tagsWritten, len(errorFiles), removed, rebuild.CreditChanges, refreshResult)
	details := map[string]any{
		"migrations":    migrations,
		"processed":     processed,
		"unchanged":     unchanged,
		"changed":       changed,
		"tags_written":  tagsWritten,
		"files_removed": removed,
		"errors":        len(errorFiles),
		"error_files":   recorded,
		"libraries":     libraryNames,
		"mb_lookups":    mbStats,
		"detail":        detailSummary(detail),
		"mirror":        mirrored,
		"refresh": map[string]any{
			"checked":          refreshResult.Checked,
			"fetched":          refreshResult.Fetched,
			"fresh":            refreshResult.Fresh,
			"changed_releases": len(refreshResult.ChangedReleases),
			"gone_releases":    refreshResult.GoneReleases,
			"relinked":         refreshResult.Relinked,
			"files_retagged":   drift.retagged,
		},
	}
	// What narrowed the scan, when something did. A partial scan otherwise reads in
	// the feed as a full one that mysteriously found forty files.
	for k, v := range scope.Detail {
		details[k] = v
	}
	// The run's counters are a roll-up across stages that do not share a unit, so they
	// carry no Filter: there is no single list they select from. The stages are where
	// a number becomes a control over rows.
	event.Stats = []models.EventStat{
		{Label: "Files processed", Value: processed},
		{Label: "Files changed", Value: changed, Kind: models.EventStatNotable},
		{Label: "Tags written", Value: tagsWritten},
		{Label: "Releases refreshed", Value: refreshResult.Fetched},
		{Label: "Changed upstream", Value: len(refreshResult.ChangedReleases)},
		{Label: "Credit changes", Value: rebuild.CreditChanges},
		{Label: "Failed", Value: len(errorFiles), Kind: models.EventStatBad},
	}

	// No detail rows on the run itself. Every row belongs to the stage that produced
	// it — files to the walk, releases to the drift re-tag — and duplicating them onto
	// the parent would show the same file twice to anyone opening the run.
	events.Finish(r.db, event, status, summary, details)
	events.Prune(r.db, r.eventRetention)
}

// countFiles sizes the run and records the walk that did it.
//
// The count is one full pass over every root, and on a cold library it is minutes of
// disk. It used to run inside the refresh phase with the progress bar at 0 of 0, so a
// run's first minutes reported as nothing happening at all — the shape of a hang. Its
// own activity says what it is doing and, afterwards, how big the run is.
func (r *Runner) countFiles(scope Scope, parent *models.Event) int {
	ev := events.BeginChild(r.db, parent, models.EventTypeCountFiles, "Counting files")

	total := 0
	perLibrary := map[string]int{}
	for _, target := range scope.Targets {
		roots := target.Roots
		if len(roots) == 0 {
			roots = []string{target.Library.Path}
		}
		n := 0
		for _, root := range roots {
			n += modules.CountSupportedFiles(root)
		}
		perLibrary[target.Library.Name] = n
		total += n
	}

	stats := make([]models.EventStat, 0, len(perLibrary)+1)
	stats = append(stats, models.EventStat{Label: "Files to process", Value: total, Kind: models.EventStatNotable})
	for _, target := range scope.Targets {
		stats = append(stats, models.EventStat{Label: target.Library.Name, Value: perLibrary[target.Library.Name], Kind: models.EventStatMuted})
	}
	ev.Stats = stats

	summary := fmt.Sprintf("%d file(s) to process across %d librar%s", total, len(scope.Targets), plural(len(scope.Targets), "y", "ies"))
	events.Finish(r.db, ev, models.EventStatusOK, summary, map[string]any{
		"files":         total,
		"by_library":    perLibrary,
		"library_count": len(scope.Targets),
	})
	return total
}

// plural picks the singular or plural form for n. One-off English, kept here so the
// summary lines above read as sentences rather than as "1 libraries".
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// taggingActivityTitle names a run's tagging stage.
//
// Just "Tagging". It used to append the run's scope — "Tagging — Processing music" —
// on the reasoning that a flat feed should say what a stage covered; but the row
// already prints `↳ Processing music` directly underneath it, so the scope was stated
// twice on the same row while the title grew long enough to wrap. The run reference
// carries it, and carries it in the one place that also links to the rest of the
// cascade.
//
// The verb invoked on its own keeps its own scoped titles ("Tag files in every
// library"): those have no parent to name them.
const taggingActivityTitle = "Tagging"

// taggingResult is everything one tagging pass wrote, from both of the ways a run
// reaches a file: the walk that found it changed on disk, and the drift pass that
// rewrote it because its release changed upstream.
type taggingResult struct {
	processed   int
	unchanged   int
	tagsWritten int
	removed     int
	errorFiles  []string
	drift       releaseRefresh
	libraries   []string
}

// finishTagging closes a tagging activity: the file counters, the per-file detail, and
// what the drift half of it did.
//
// One emitter for every tagging pass, whether a run reached it as a stage or a user
// pressed *Tag files*. Two emitters is how the same work ends up reported with
// different words in it depending on what started it — and a cascading activity and a
// hand-pressed one are the same work.
func (r *Runner) finishTagging(ev *models.Event, res taggingResult, detail *components.DetailCollector) {
	status := models.EventStatusOK
	if len(res.errorFiles) > 0 {
		status = models.EventStatusError
	}
	recorded := res.errorFiles
	if len(recorded) > maxErrorFilesRecorded {
		recorded = recorded[:maxErrorFilesRecorded]
	}
	changed := res.processed - res.unchanged

	// "files" is stated once and carries across the clauses that share the unit; the
	// tags clause is the one that does not, which is the whole reason to say it.
	summary := fmt.Sprintf("%d files processed · %d changed · %d tags written · %d errors",
		res.processed, changed, res.tagsWritten, len(res.errorFiles))
	if res.removed > 0 {
		summary += fmt.Sprintf(" · %d removed", res.removed)
	}
	if res.drift.changedReleases > 0 {
		summary += fmt.Sprintf(" · %d re-tagged from %d release(s) changed upstream",
			res.drift.retagged, res.drift.changedReleases)
	}

	// Both halves declare their counters, but only when they happened: a run where
	// nothing drifted should not carry two zeroes explaining a stage it never entered.
	// Every file counter names its unit, because the one that does not is "Tags
	// written" — and a row reading "Unchanged 26 · Changed 1 · Tags written 21" invites
	// the reading that 21 things changed when one file did. The counters are two units
	// in one row and only the labels can say so. It is also the vocabulary the run
	// roll-up, the drift stage and the mirror already use ("Files processed", "Files
	// re-tagged", "Releases checked").
	stats := []models.EventStat{
		{Label: "Files processed", Value: res.processed},
		{Label: "Files unchanged", Value: res.unchanged, Kind: models.EventStatMuted},
		{Label: "Files changed", Value: changed, Kind: models.EventStatNotable, Filter: models.EventItemStatusChanged},
		{Label: "Tags written", Value: res.tagsWritten},
		{Label: "Files removed", Value: res.removed},
	}
	if res.drift.changedReleases > 0 {
		stats = append(stats,
			models.EventStat{Label: "Changed upstream", Value: res.drift.changedReleases, Kind: models.EventStatNotable, Filter: models.EventItemStatusRefreshed},
			models.EventStat{Label: "Re-tagged", Value: res.drift.retagged},
		)
	}
	stats = append(stats, models.EventStat{Label: "Failed", Value: len(res.errorFiles), Kind: models.EventStatBad, Filter: models.EventItemStatusError})
	ev.Stats = stats

	events.Finish(r.db, ev, status, summary, map[string]any{
		"processed":        res.processed,
		"unchanged":        res.unchanged,
		"changed":          changed,
		"tags_written":     res.tagsWritten,
		"files_removed":    res.removed,
		"errors":           len(res.errorFiles),
		"error_files":      recorded,
		"libraries":        res.libraries,
		"releases_changed": res.drift.changedReleases,
		"files_retagged":   res.drift.retagged,
		"detail":           detailSummary(detail),
	})

	// The release rows go on last: they are few, they are the tie between a metadata
	// change and the files it rewrote, and they must not be lost to a walk that filled
	// the detail limit on its own.
	items := append(detail.Items(), res.drift.refreshItems...)
	events.AddItems(r.db, ev, items)
}

// scanSummaryLine is the one-line summary stored on a scan event. The removed-files
// and metadata-refresh clauses are appended only when they actually happened, so an
// ordinary scan with nothing gone and nothing due upstream reads exactly as before
// rather than trailing a "· 0 releases refreshed" that says nothing.
func scanSummaryLine(processed, changed, tagsWritten, errorCount, removed, creditChanges int, refresh mirror.Result) string {
	s := fmt.Sprintf("%d files processed · %d changed · %d tags written · %d errors", processed, changed, tagsWritten, errorCount)
	if removed > 0 {
		s += fmt.Sprintf(" · %d removed", removed)
	}
	if creditChanges > 0 {
		s += fmt.Sprintf(" · %d credit change(s)", creditChanges)
	}
	if refresh.Fetched > 0 || len(refresh.ChangedReleases) > 0 {
		s += fmt.Sprintf(" · %d releases refreshed", refresh.Fetched)
		if n := len(refresh.ChangedReleases); n > 0 {
			s += fmt.Sprintf(", %d changed upstream", n)
		}
	}
	return s
}

// refreshDetailItem builds the Activity detail row for a release that changed upstream
// and had files rewritten because of it. filesRetagged is how many, which is what ties
// the metadata change to the file changes in the same activity.
//
// It is an **entity** row: the identifier is an MBID, not a path. Without the kind it
// rendered through the file branch, which reports "N tags written" — so a release that
// needed no rewrite read as "0 tags written" against a release ID, a claim about the
// user's audio made by a row that is not about a file at all.
//
// Only releases that actually caused a write get one. Every release that changed
// upstream is already listed, one row each, on the metadata refresh activity of the
// same run; repeating the ones that changed nothing here says nothing twice.
func refreshDetailItem(mbID string, filesRetagged int) models.EventItem {
	return models.EventItem{
		Path:        mbID,
		Kind:        models.EventItemKindEntity,
		Status:      models.EventItemStatusRefreshed,
		Phase:       models.EventItemPhaseRefresh,
		TagsWritten: filesRetagged,
	}
}

// detailSummary describes the per-file detail attached to an event: how many changed
// and failed files there were in total, and how many rows were actually kept. The UI
// needs the pair to say "showing 500 of 3120" rather than implying 500 was all of it.
func detailSummary(detail *components.DetailCollector) map[string]any {
	changed, failed := detail.Totals()
	return map[string]any{
		"changed_files": changed,
		"failed_files":  failed,
		"recorded":      len(detail.Items()),
		"limit":         detail.Limit(),
	}
}

func (r *Runner) setStatus(f func(*Summary)) {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	f(&r.summary)
}

// rebuildCollection refreshes the present/collection view after a run. It is
// best-effort — a failure is logged, not propagated — and returns what the pass
// changed so a run can report it.
//
// It goes through collection.RecordScan, the same call the Scan button makes, so the
// verb records one activity with one set of words in it however it was reached. The
// only difference is the parent: pressed on its own it is top-level, reached by a run
// it belongs to that run.
func (r *Runner) rebuildCollection(parent *models.Event) collection.RebuildStats {
	stats, err := collection.RecordScanUnder(r.db, parent, "Collection scan", collection.RebuildScope{}, nil)
	if err != nil {
		logger.Log.Warnf("failed to rebuild collection: %s", err.Error())
	}
	return stats
}

// syncManagers refreshes the manager mirror at the end of a run, so the catalog half
// of the collection is maintained by the same thing that maintains the disk half.
//
// It used to be reachable only from POST /collection/sync-lidarr, which meant the
// catalog block was stale by default: every disk/catalog comparison on the artist page
// was against whenever somebody last pressed a button. Worse, an artist newly
// discovered by the rebuild — which is how an album re-credited upstream reaches its
// new artist — had no catalog at all until then, so the run that found them left them
// looking unmanaged.
//
// It must run *after* the rebuild for exactly that reason: the mirror only syncs
// artists that are already collection rows, so an artist discovered by this run's
// rebuild is only in the mirror's scope once that rebuild has committed.
//
// Only full-library scans do this. A per-artist or per-release-group scan is an
// interactive action, and making a one-album button wait on a mirror pass would be a
// poor trade. The nightly scan covers it, and a user who wants one artist re-mirrored
// now has the scoped verb for it (POST /artists/:mbid/sync-lidarr) rather than having
// to trigger a whole-collection pass.
func (r *Runner) syncManagers(parent *models.Event) (artists, albums int) {
	ev := events.BeginChild(r.db, parent, models.EventTypeLidarrSync, "Sync from Lidarr")
	stats, err := collection.SyncLidarr(r.db)
	artists, albums = stats.ArtistsSynced, stats.Groups

	status := models.EventStatusOK
	details := collection.SyncEventDetails(stats)
	summary := collection.SyncSummaryLine(stats)
	if err != nil {
		logger.Log.Warnf("failed to sync the manager mirror: %s", err.Error())
		status = models.EventStatusError
		details["error"] = err.Error()
	} else if artists > 0 {
		logger.Log.Infof("mirrored %d album(s) for %d manager-owned artist(s)", albums, artists)
	}
	// This stage runs on every full library run, so on a native-only install it is a
	// zero in the feed nightly. Saying which input was missing is what stops that
	// reading as a broken Lidarr rather than as a Lidarr nobody uses.
	if stats.EmptyReason != "" {
		summary += " — " + stats.EmptyReason
		details["empty_reason"] = stats.EmptyReason
	}
	ev.Stats = collection.SyncEventStats(stats)
	events.Finish(r.db, ev, status, summary, details)
	events.AddItems(r.db, ev, collection.SyncEventItems(stats))
	return artists, albums
}

// repairGhostAlbums asks each manager to re-read the artists holding albums whose
// MusicBrainz ID no longer resolves, so the queue drained next can tell a mis-keyed
// album from a genuinely dead one.
//
// Records an event **only when there was something to repair**, on the same reasoning
// as applyMigrations: a nightly "0 candidates" row would bury the runs that actually
// fixed something. Failures are logged and reported on the event, never returned — a
// manager being unreachable must not stop the run that found the problem.
func (r *Runner) repairGhostAlbums(parent *models.Event) collection.RepairStats {
	return r.repairGhostAlbumsWith(parent, collection.RepairOptions{})
}

// repairGhostAlbumsWith is repairGhostAlbums with the scope spelled out, so the
// approve button can repair one artist now without waiting for the artist's cooldown
// or setting the whole collection refreshing in the manager.
func (r *Runner) repairGhostAlbumsWith(parent *models.Event, opts collection.RepairOptions) collection.RepairStats {
	if r.db == nil {
		return collection.RepairStats{}
	}

	stats, err := collection.RepairGhostReleaseGroupsWith(r.db, opts)
	if err != nil {
		logger.Log.Warnf("failed to repair albums with unresolvable MusicBrainz IDs: %s", err.Error())
		return stats
	}
	if stats.Candidates == 0 {
		return stats
	}

	ev := events.BeginChild(r.db, parent, models.EventTypeLidarrSync, "Repair albums via Lidarr")
	status := models.EventStatusOK
	if len(stats.Failures) > 0 {
		status = models.EventStatusError
	}
	summary := fmt.Sprintf("%d album(s) with unresolvable IDs · %s refreshed · %d repaired",
		stats.Candidates, plural(stats.Artists, "artist", "artists"), stats.Repaired)
	if stats.Skipped > 0 {
		summary += fmt.Sprintf(" · %d skipped (asked recently)", stats.Skipped)
	}
	ev.Stats = []models.EventStat{
		{Label: "Unresolvable albums", Value: stats.Candidates, Kind: models.EventStatNotable},
		{Label: "Artists refreshed", Value: stats.Artists},
		{Label: "Repaired", Value: stats.Repaired, Kind: models.EventStatNotable},
		{Label: "Refreshes failed", Value: len(stats.Failures), Kind: models.EventStatBad},
	}
	events.Finish(r.db, ev, status, summary, map[string]any{"repair": stats})
	return stats
}

// RepairArtistAlbums asks the manager to re-read one artist whose albums hold
// MusicBrainz IDs that no longer resolve, then drains the migration queue.
//
// This is what the approve button does when a retirement is blocked by the manager
// still listing the album. Approving is the user saying "deal with this", and the only
// thing that can deal with it is the manager: a refresh either re-keys the album to a
// live ID (nothing to retire — the entry fixes itself) or drops it (retirable, which
// the drain below then does). Refusing with an instruction to go and press refresh in
// Lidarr made the user do by hand the one step Autotaggerr can do for them.
//
// It is queued rather than run inline because the manager's refresh command is waited
// on for up to three minutes, which is far longer than an HTTP request should be held
// open. The cooldown is ignored: it exists to keep an unattended nightly pass from
// re-asking, and a person pressing a button is not that.
//
// Run via `go` for background execution.
func (r *Runner) RepairArtistAlbums(artistMBID string) {
	r.enqueue(job{jobRepairArtist, "repair_artist:" + artistMBID, "Repair albums via the manager", func() {
		r.repairArtistAlbumsNow(artistMBID)
	}})
}

func (r *Runner) repairArtistAlbumsNow(artistMBID string) {
	if r.db == nil {
		return
	}

	// A parent event, so the refresh, the drain and the rebuild read as one action in
	// the feed rather than as three unrelated rows appearing at the same second.
	ev := events.Begin(r.db, models.EventTypeMigration, "Repair albums via the manager")

	stats := r.repairGhostAlbumsWith(ev, collection.RepairOptions{ArtistMBID: artistMBID, IgnoreCooldown: true})

	// Same order as a full run: repair, then drain. A release-group deletion stops
	// being held for review once the manager has been asked (see Policy.heldForReview),
	// so this drain is what actually retires the album — or fails it with the reason
	// the manager's answer produced.
	res := r.applyMigrations(ev)
	r.rebuildCollection(ev)

	status := models.EventStatusOK
	if len(stats.Failures) > 0 {
		status = models.EventStatusError
	}
	summary := fmt.Sprintf("%s refreshed · %d album(s) repaired · %d retired",
		plural(stats.Artists, "artist", "artists"), stats.Repaired, res.Retired)
	if stats.Candidates == 0 {
		summary = "nothing left to repair for this artist"
	}
	events.Finish(r.db, ev, status, summary, map[string]any{
		"repair":     stats,
		"artist":     artistMBID,
		"migrations": res,
	})
}

// applyMigrations drains the pending MusicBrainz migration queue at a run boundary.
//
// It runs here rather than where a redirect is detected because detection happens on
// the fetch path — mid-scan, on a worker goroutine, holding the rate limiter — and
// applying one rewrites MB IDs across several tables in a transaction. Doing that
// between files would interleave schema-wide rewrites with tag writes; doing it here
// means it happens once, with the collection rebuild that follows picking up the
// remapped ownership in the same pass.
//
// A run that detects nothing costs one indexed query for an empty set.
// It records an event **only when it found something**. Draining an empty queue is
// one indexed query, and a row per run saying "0 applied" would bury the runs that
// actually re-pointed a record — the opposite of what putting identity changes in the
// feed is for.
func (r *Runner) applyMigrations(parent *models.Event) migration.Result {
	if r.db == nil {
		return migration.Result{}
	}

	res, err := migration.ProcessPending(r.db, migration.PolicyFromConfig(files.ConfigFile))
	if err != nil {
		logger.Log.Warnf("failed to process MusicBrainz migrations: %s", err.Error())
		r.recordMigrations(parent, res, err)
		return res
	}
	if res.Applied > 0 || res.Pending > 0 || res.Failed > 0 {
		logger.Log.Infof("MusicBrainz migrations: %d applied (%d files remapped, %d files un-identified) · %d awaiting review · %d failed",
			res.Applied, res.Files, res.Unmatched, res.Pending, res.Failed)
		r.recordMigrations(parent, res, nil)
	}
	return res
}

// recordMigrations writes the identity-change stage's own event. The type existed as a
// constant for a long time with nothing emitting it, so applying a merge or a deletion
// — the one thing here that rewrites what a record *is* — was the least visible thing
// the app did.
func (r *Runner) recordMigrations(parent *models.Event, res migration.Result, err error) {
	ev := events.BeginChild(r.db, parent, models.EventTypeMigration, "Identity changes")

	status := models.EventStatusOK
	details := map[string]any{
		"applied":         res.Applied,
		"pending":         res.Pending,
		"failed":          res.Failed,
		"files_remapped":  res.Files,
		"files_unmatched": res.Unmatched,
	}
	summary := fmt.Sprintf("%d applied · %d files remapped · %d awaiting review", res.Applied, res.Files, res.Pending)
	if len(res.Errors) > 0 {
		details["errors"] = res.Errors
	}
	if res.Failed > 0 {
		status = models.EventStatusError
		summary += fmt.Sprintf(" · %d failed", res.Failed)
	}
	if err != nil {
		status = models.EventStatusError
		details["error"] = err.Error()
		summary = "failed — " + err.Error()
	}
	ev.Stats = []models.EventStat{
		{Label: "Applied", Value: res.Applied, Kind: models.EventStatNotable},
		{Label: "Files remapped", Value: res.Files},
		{Label: "Files un-identified", Value: res.Unmatched},
		{Label: "Awaiting review", Value: res.Pending, Kind: models.EventStatMuted},
		{Label: "Failed", Value: res.Failed, Kind: models.EventStatBad},
	}
	events.Finish(r.db, ev, status, summary, details)
}

// SyncDrift refreshes metadata for the whole collection. It is the refresh verb at
// collection scope, and it writes no files.
//
// It used to re-tag the files of any release that changed, which made a button
// called "sync" a button that rewrote audio. The re-tagging now happens in the scan
// (see the drift stage in Run), which is the verb that owns file writes. A user who
// wants it immediately presses Tag files.
//
// Run via `go` for background execution.
func (r *Runner) SyncDrift() {
	r.enqueue(job{jobRefreshAll, "refresh_all", "Metadata refresh", r.syncDriftNow})
}

func (r *Runner) syncDriftNow() {
	if err := r.refresh.RunCollection(context.Background(), false); err != nil && !errors.Is(err, mirror.ErrAlreadyRunning) {
		logger.Log.Warnf("metadata refresh failed: %s", err.Error())
	}
}

// VerifyIdentities re-reads every MBID the collection is keyed on, ignoring every
// cached copy, so entities merged or deleted upstream are found now rather than
// whenever their TTL happens to lapse.
//
// It is not a verb of its own: it is the refresh verb at collection scope with the
// cache ignored. Detecting a merge was never a separate activity — merges and
// deletions are recorded on the HTTP path by whatever fetch sees them, so any
// refresh that reaches the network finds them, and queues them under the same
// approval policy.
//
// Which is why it is *titled* one — "Full metadata refresh", the same words
// CollectionScope writes onto the event. A queue reading "Verify identities" above
// an event reading "Full metadata refresh" described one pass as two things, and the
// Go name is the only place the old word survives.
//
// Run via `go` for background execution.
func (r *Runner) VerifyIdentities() {
	r.enqueue(job{jobRefreshVerify, "refresh_verify", "Full metadata refresh", r.verifyIdentitiesNow})
}

func (r *Runner) verifyIdentitiesNow() {
	if err := r.refresh.RunCollection(context.Background(), true); err != nil && !errors.Is(err, mirror.ErrAlreadyRunning) {
		logger.Log.Warnf("full metadata refresh failed: %s", err.Error())
	}
}

// RefreshArtist pulls fresh metadata for one artist: who they are, their
// discography (so a release added upstream appears), the editions of each of their
// release-groups, and every release of theirs the collection holds. The TTL is
// ignored — asking by hand means "check now", and "it was checked recently, come
// back in a week" is not an answer to that.
//
// **It writes no files.** It used to re-tag every file of a release that had
// changed, which meant a button labelled *Refresh metadata* could rewrite hundreds
// of audio files. What it does now is report how many releases changed; the next
// scan re-tags them, or the user presses Tag files to do it immediately.
//
// Run via `go` for background execution.
// force ignores cached copies for this artist, which is a deliberate choice made in
// the refresh dialog and never a default.
//
// The queue key carries the reading, so a forced request is **not** deduped onto an
// unforced one already pending. Sharing a key would mean the cheap pass silently
// satisfying the expensive request — a force that reported as queued and then did not
// happen, which is the one failure mode this verb's naming rules exist to prevent.
// Two presses of the same reading still collapse, which is what dedup is for.
func (r *Runner) RefreshArtist(artistMBID string, force bool) {
	key := "refresh_artist:" + artistMBID
	title := "Metadata refresh"
	if force {
		key = "refresh_artist_force:" + artistMBID
		title = "Full metadata refresh"
	}
	r.enqueue(job{jobRefreshArtist, key, title, func() {
		r.refreshArtistNow(artistMBID, force)
	}})
}

func (r *Runner) refreshArtistNow(artistMBID string, force bool) {
	scope, err := mirror.ArtistScope(r.db, artistMBID, force)
	if err != nil {
		logger.Log.Warnf("metadata refresh skipped for artist %s: %s", artistMBID, err.Error())
		return
	}

	// The discography is synced through the collection layer as well as fetched,
	// because upserting the release-group rows is what makes a newly released album
	// appear in the collection at all — the cache alone would hold it and show
	// nobody.
	//
	// Doing both used to mean paging the discography twice over the network, since
	// this sync bypassed the cache and the scope below forced. On the ordinary reading
	// the sync now fills the cache and the pass finds it fresh, so the second read
	// costs nothing. A *forced* pass re-reads it anyway, by definition — a handful of
	// pages against the hundreds of requests forcing already costs, and the alternative
	// is a force that trusts a copy fetched moments earlier.
	if _, err := collection.SyncArtist(r.db, r.meta, artistMBID); err != nil {
		logger.Log.Warnf("failed to sync discography for %s: %s", artistMBID, err.Error())
	}

	if _, err := r.refresh.Run(context.Background(), scope); err != nil && !errors.Is(err, mirror.ErrAlreadyRunning) {
		logger.Log.Warnf("metadata refresh failed for %s: %s", artistMBID, err.Error())
	}
}

// RefreshLibrary pulls fresh metadata for everything one library's files point at,
// ignoring the cache. The middle scope between one artist and the whole collection.
//
// Like every refresh it writes no files: it reports what changed, and the next scan
// (or Tag files) applies it.
//
// Run via `go` for background execution.
func (r *Runner) RefreshLibrary(libraryID uuid.UUID) {
	r.enqueue(job{jobRefreshLibrary, "refresh_library:" + libraryID.String(), "Metadata refresh", func() {
		r.refreshLibraryNow(libraryID)
	}})
}

func (r *Runner) refreshLibraryNow(libraryID uuid.UUID) {
	scope, err := mirror.LibraryScope(r.db, libraryID)
	if err != nil {
		logger.Log.Warnf("metadata refresh skipped for library %s: %s", libraryID, err.Error())
		return
	}
	if _, err := r.refresh.Run(context.Background(), scope); err != nil && !errors.Is(err, mirror.ErrAlreadyRunning) {
		logger.Log.Warnf("metadata refresh failed for library %s: %s", libraryID, err.Error())
	}
}

// RetagAll queues a re-tag of every indexed file in every enabled library — *Tag
// files* at collection scope, the widest of the three.
//
// It is one job rather than one per library, so it dedups as a unit and the queue
// shows a single entry for what the user asked for once. Libraries are loaded when
// the job runs, not when it is queued, matching runAllNow: a library added while it
// waited is included.
func (r *Runner) RetagAll() {
	r.enqueue(job{jobRetagAll, "retag_all", "Tag files", r.retagAllNow})
}

func (r *Runner) retagAllNow() {
	var libraries []models.Library
	if err := r.db.Where("enabled = ?", true).Order("name").Find(&libraries).Error; err != nil {
		logger.Log.Error("failed to load libraries from database. error: " + err.Error())
		return
	}
	if len(libraries) == 0 {
		logger.Log.Info("no enabled libraries configured; nothing to tag")
		return
	}

	ids := make([]uuid.UUID, 0, len(libraries))
	names := make([]string, 0, len(libraries))
	for _, library := range libraries {
		ids = append(ids, library.ID)
		names = append(names, library.Name)
	}

	var items []models.LibraryItem
	if err := r.db.Where("library_id IN ?", ids).Scopes(models.TaggableItems).
		Order("path").Find(&items).Error; err != nil {
		logger.Log.Warnf("failed to load items for a collection-wide re-tag: %s", err.Error())
		return
	}

	logger.Log.Infof("re-tagging %d files across %d library(ies)", len(items), len(libraries))
	event := events.Begin(r.db, models.EventTypeTagFiles, "Tag files in every library")
	refreshSet := modules.NewAlbumRefreshSet(nil)
	detail := components.NewDetailCollector(r.detailRetention)

	result := releaseRefresh{}
	result.retagItems(r, items, map[uuid.UUID]models.Library{}, refreshSet, detail)

	r.flushPlex(refreshSet, event)
	summary := fmt.Sprintf("%d of %d files re-tagged · %d errors", result.retagged, len(items), len(result.errorFiles))
	logger.Log.Infof("re-tag finished. %s", summary)
	r.finishRefresh(event, summary, result, detail, map[string]any{
		"libraries":      names,
		"files_in_scope": len(items),
	})
}

// RetagLibrary queues a re-tag of every indexed file in one library from its stored
// correlation, without walking the disk or re-fetching anything. The library-scoped
// twin of RetagArtist.
func (r *Runner) RetagLibrary(libraryID uuid.UUID) {
	r.enqueue(job{jobRetagLibrary, "retag_library:" + libraryID.String(), "Tag files", func() {
		r.retagLibraryNow(libraryID)
	}})
}

func (r *Runner) retagLibraryNow(libraryID uuid.UUID) {
	var library models.Library
	if err := r.db.First(&library, "id = ?", libraryID).Error; err != nil {
		logger.Log.Warnf("re-tag skipped: library %s not found: %s", libraryID, err.Error())
		return
	}

	var items []models.LibraryItem
	if err := r.db.Where("library_id = ?", libraryID).Scopes(models.TaggableItems).
		Order("path").Find(&items).Error; err != nil {
		logger.Log.Warnf("failed to load items for library %s: %s", library.Name, err.Error())
		return
	}

	logger.Log.Infof("re-tagging %d files for library: %s", len(items), library.Name)
	event := events.Begin(r.db, models.EventTypeTagFiles, "Tag files in "+library.Name)
	refreshSet := modules.NewAlbumRefreshSet(nil)
	detail := components.NewDetailCollector(r.detailRetention)

	result := releaseRefresh{}
	result.retagItems(r, items, map[uuid.UUID]models.Library{}, refreshSet, detail)

	r.flushPlex(refreshSet, event)
	summary := fmt.Sprintf("%d of %d files re-tagged · %d errors", result.retagged, len(items), len(result.errorFiles))
	logger.Log.Infof("re-tag finished for %s. %s", library.Name, summary)
	r.finishRefresh(event, summary, result, detail, map[string]any{
		"library":        library.Name,
		"library_id":     libraryID.String(),
		"files_in_scope": len(items),
	})
}

// RetagArtist rewrites every indexed file of one artist from its stored correlation,
// without walking the disk or re-fetching anything.
//
// It is the cheapest of the three artist actions and answers a specific question:
// the tagger profile changed, or a file was edited outside Autotaggerr, and the
// metadata is already known to be right. A scan would skip those files (unchanged on
// disk), and a metadata refresh would spend rate limit re-reading releases that never
// changed; this writes what is already known and stops.
func (r *Runner) RetagArtist(artistMBID string) {
	r.enqueue(job{jobRetagArtist, "retag_artist:" + artistMBID, "Tag files", func() {
		r.retagArtistNow(artistMBID)
	}})
}

func (r *Runner) retagArtistNow(artistMBID string) {
	var artist models.CollectionArtist
	if err := r.db.Where("mb_id = ?", artistMBID).First(&artist).Error; err != nil {
		logger.Log.Warnf("re-tag skipped: artist %s not found: %s", artistMBID, err.Error())
		return
	}
	items, err := collection.ArtistItems(r.db, artistMBID)
	if err != nil {
		logger.Log.Warnf("failed to load items for %s: %s", artist.Name, err.Error())
		return
	}

	logger.Log.Infof("re-tagging %d files for artist: %s", len(items), artist.Name)
	event := events.Begin(r.db, models.EventTypeTagFiles, "Tag files for "+artist.Name)
	refreshSet := modules.NewAlbumRefreshSet(nil)
	detail := components.NewDetailCollector(r.detailRetention)

	result := releaseRefresh{}
	result.retagItems(r, items, map[uuid.UUID]models.Library{}, refreshSet, detail)

	r.flushPlex(refreshSet, event)
	summary := fmt.Sprintf("%d of %d files re-tagged · %d errors", result.retagged, len(items), len(result.errorFiles))
	logger.Log.Infof("re-tag finished for %s. %s", artist.Name, summary)
	r.finishRefresh(event, summary, result, detail, map[string]any{
		"artist":         artist.Name,
		"artist_mb_id":   artistMBID,
		"files_in_scope": len(items),
	})
}

// releaseRefresh accumulates the outcome of re-tagging work: how many releases were
// re-read, how many had changed, how many files were rewritten, and which failed.
type releaseRefresh struct {
	checked         int
	changedReleases int
	retagged        int
	errorFiles      []string
	// goneReleases and relinked record what the run learned about identity rather
	// than content: releases MusicBrainz no longer has, and releases that changed
	// release-group. Both are silent in the file counts — no file need change for
	// either — so they are counted separately or they would not be visible at all.
	goneReleases int
	relinked     int
	migrations   migration.Result
	// refreshItems is one Activity detail row per release that changed upstream,
	// carrying how many of its files were re-tagged. Collected here so the scan can
	// attach them to its event; see refreshDetailItem.
	refreshItems []models.EventItem
}

func (res releaseRefresh) summary() string {
	summary := fmt.Sprintf("%d releases checked · %d changed · %d files re-tagged · %d errors",
		res.checked, res.changedReleases, res.retagged, len(res.errorFiles))
	if res.migrations.Applied > 0 || res.migrations.Pending > 0 || res.goneReleases > 0 {
		summary += fmt.Sprintf(" · %d migrations applied · %d awaiting review",
			res.migrations.Applied, res.migrations.Pending)
	}
	return summary
}

// retagReleases rewrites the indexed files of releases that changed upstream.
//
// It is the write half of what used to be one cascading function: the refresh verb
// now decides *which* releases changed and this decides what to do about it. The
// split is what lets "Refresh metadata" be a button that only reads, while the scan
// remains the thing that touches files.
//
// filter confines the writes to the run's scope. Without it this stage was a
// collection-wide re-tag hiding inside every scan, however narrow: a file was
// rewritten because its release had expired, not because the run had anything to do
// with it. A release still out of scope is counted as checked and changed — that is
// true of the metadata regardless of whose folders were walked — with no files
// against it.
func (r *Runner) retagReleases(mbIDs []string, refreshSet *modules.AlbumRefreshSet, detail *components.DetailCollector, filter scopeFilter) releaseRefresh {
	res := releaseRefresh{}
	libraries := map[uuid.UUID]models.Library{} // small per-run cache

	for _, mbID := range mbIDs {
		res.checked++
		res.changedReleases++

		var items []models.LibraryItem
		if err := r.db.Where("mb_release_id = ?", mbID).Scopes(models.TaggableItems).Find(&items).Error; err != nil {
			logger.Log.Warnf("failed to load items for release %s: %s", mbID, err.Error())
			continue
		}
		items = filter.keep(items)
		before := res.retagged
		res.retagItems(r, items, libraries, refreshSet, detail)
		if written := res.retagged - before; written > 0 {
			res.refreshItems = append(res.refreshItems, refreshDetailItem(mbID, written))
		}
	}
	return res
}

// retagItems rewrites a batch of indexed files, recording each outcome. One
// unreadable file must not abandon the rest, so errors are collected rather than
// returned. The libraries map is the caller's cache, reused across batches.
func (res *releaseRefresh) retagItems(r *Runner, items []models.LibraryItem, libraries map[uuid.UUID]models.Library, refreshSet *modules.AlbumRefreshSet, detail *components.DetailCollector) {
	for _, item := range items {
		written, changes, err := r.retagItem(item, libraries, refreshSet)
		if err != nil {
			logger.Log.Errorf("failed to re-tag '%s'. error: %s", item.Path, err.Error())
			res.errorFiles = append(res.errorFiles, item.Path)
			detail.AddError(item.Path, err)
			continue
		}
		if written > 0 {
			res.retagged++
			detail.AddChanged(item.Path, written, changes)
		}
	}
}

// flushPlex tells Plex to refresh every album a run touched, and records the batch as
// a single Plex refresh event. A nil client (Plex not configured) or an empty set
// skips it — nothing happened, so no event. One event per run rather than per album
// keeps the feed readable when a scan touches hundreds of albums.
func (r *Runner) flushPlex(refreshSet *modules.AlbumRefreshSet, parent *models.Event) {
	if r.plex == nil {
		return
	}
	albums := refreshSet.Snapshot()
	if len(albums) == 0 {
		return
	}

	// This stage has always had its own event — it was the only one that did, and it
	// was not tied to the run that produced it, so a Plex refresh appeared in the feed
	// beside a run with nothing saying they were the same work.
	event := events.BeginChild(r.db, parent, models.EventTypePlexRefresh, "Plex refresh")
	refreshed := 0
	failed := make([]string, 0)

	// One row per album, in a stable order so two runs over the same set read the
	// same way — a map's iteration order would reshuffle the list on every run and
	// make "did this one work last night?" a search rather than a glance.
	names := make([]string, 0, len(albums))
	for albumName := range albums {
		names = append(names, albumName)
	}
	sort.Strings(names)

	items := make([]models.EventItem, 0, len(names))
	for _, albumName := range names {
		albumKey := albums[albumName]
		item := models.EventItem{Path: albumName, Kind: models.EventItemKindAlbum, Status: models.EventItemStatusRefreshed}
		if err := r.plex.RefreshAlbum(albumKey); err != nil {
			logger.Log.Error("failed to inform Plex to refresh album. error: " + err.Error())
			failed = append(failed, albumName)
			item.Status = models.EventItemStatusError
			item.Error = err.Error()
			items = append(items, item)
			continue
		}
		refreshed++
		items = append(items, item)
		logger.Log.Info("triggered Plex refresh for album: " + albumName)
	}

	status := models.EventStatusOK
	if len(failed) > 0 {
		status = models.EventStatusError
	}
	summary := fmt.Sprintf("%d album(s) refreshed · %d failed", refreshed, len(failed))
	// Both counters select rows now, which is the whole reason the rows exist: this
	// stage used to report two numbers over nothing, so "which albums?" — the only
	// question a Plex refresh provokes — had no answer anywhere in the app.
	event.Stats = []models.EventStat{
		{Label: "Albums refreshed", Value: refreshed, Filter: models.EventItemStatusRefreshed},
		{Label: "Failed", Value: len(failed), Kind: models.EventStatBad, Filter: models.EventItemStatusError},
	}
	events.Finish(r.db, event, status, summary, map[string]any{
		"albums_refreshed": refreshed,
		"albums_failed":    len(failed),
		"failed_albums":    failed,
		// The Plex rating keys, which the rows deliberately do not carry: a key is
		// what you need when the refresh went to the wrong album, and that is what
		// the raw-details escape hatch is for.
		"album_keys": albums,
	})
	events.AddItems(r.db, event, items)
	events.Prune(r.db, r.eventRetention)
}

// finishRefresh records the Activity event shared by every re-tagging run, with
// `extra` carrying whatever narrowed it.
func (r *Runner) finishRefresh(event *models.Event, summary string, res releaseRefresh, detail *components.DetailCollector, extra map[string]any) {
	status := models.EventStatusOK
	if len(res.errorFiles) > 0 {
		status = models.EventStatusError
	}
	recorded := res.errorFiles
	if len(recorded) > maxErrorFilesRecorded {
		recorded = recorded[:maxErrorFilesRecorded]
	}
	details := map[string]any{
		"releases_checked":  res.checked,
		"releases_changed":  res.changedReleases,
		"files_retagged":    res.retagged,
		"errors":            len(res.errorFiles),
		"error_files":       recorded,
		"detail":            detailSummary(detail),
		"releases_gone":     res.goneReleases,
		"releases_relinked": res.relinked,
		"migrations":        res.migrations,
	}
	for k, v := range extra {
		details[k] = v
	}

	// The collection scan runs *before* this event is finished, and as a stage of it,
	// for the same reason a processing run does it that way: a rebuild that moved an
	// album between artists is news, and one recorded after its parent had been written
	// belongs to nothing.
	r.rebuildCollection(event)

	event.Stats = []models.EventStat{
		{Label: "Releases checked", Value: res.checked},
		{Label: "Changed upstream", Value: res.changedReleases, Kind: models.EventStatNotable, Filter: models.EventItemStatusRefreshed},
		{Label: "Files re-tagged", Value: res.retagged, Filter: models.EventItemStatusChanged},
		{Label: "Failed", Value: len(res.errorFiles), Kind: models.EventStatBad, Filter: models.EventItemStatusError},
	}
	events.Finish(r.db, event, status, summary, details)
	events.AddItems(r.db, event, detail.Items())
	events.Prune(r.db, r.eventRetention)
}

// retagItem rewrites one indexed file's tags from its stored correlation and its
// library's tagger settings, then refreshes the item's on-disk identity so
// skip-unchanged stays correct. Libraries are cached across the run.
//
// It records the outcome the same way the processing pipeline does
// (components.recordItem): a write that succeeds clears whatever the last attempt
// failed at, and a write that fails says so. Without that, a file that errored
// during a scan and was then fixed by a re-tag kept reporting the old failure
// forever — the re-tag is precisely the thing that repaired it — and a re-tag that
// failed left the row claiming the file was fine.
func (r *Runner) retagItem(item models.LibraryItem, libraries map[uuid.UUID]models.Library, refreshSet *modules.AlbumRefreshSet) (int, []models.TagChange, error) {
	library, ok := libraries[item.LibraryID]
	if !ok {
		if err := r.db.First(&library, "id = ?", item.LibraryID).Error; err != nil {
			return 0, nil, err
		}
		libraries[item.LibraryID] = library
	}
	tagger := components.TaggerForLibrary(r.db, library)

	correlation := models.Correlation{
		MBReleaseID:      item.MBReleaseID,
		MBReleaseTrackID: item.MBReleaseTrackID,
		MBRecordingID:    item.MBRecordingID,
		Source:           item.CorrelationSource,
	}
	if !tagger.WriteEnabled() {
		return 0, nil, nil
	}
	// Nothing to rewrite from, and nothing to say about it: an unmatched file has no
	// correlation, so attempting the write would only turn "the manager does not know
	// this file" into an error status that misreports why.
	if correlation.MBReleaseID == "" {
		return 0, nil, nil
	}
	unchanged, written, changes, err := modules.TagResolvedFile(item.Path, correlation, r.plex, refreshSet, library.Path, tagger.Settings())
	if err != nil {
		r.recordRetagFailure(item, err)
		return 0, nil, err
	}

	now := time.Now()
	updates := map[string]any{
		"last_scanned_at":   now,
		"processed_version": r.version,
		// The file is written and its identity holds, which is the whole of what a
		// status says. Clearing the dated error with it is the same rule the manual
		// attach applies (routers.saveCorrelation): a failure that has been repaired
		// must stop reading as a live problem.
		"status":               models.LibraryItemStatusOK,
		"error":                "",
		"last_error_at":        nil,
		"last_error_transient": false,
	}
	if !unchanged {
		updates["last_tagged_at"] = now
	}
	if fi, statErr := os.Stat(item.Path); statErr == nil {
		mod := fi.ModTime()
		updates["size"] = fi.Size()
		updates["mod_time"] = mod
	}
	if err := r.db.Model(&models.LibraryItem{}).Where("id = ?", item.ID).Updates(updates).Error; err != nil {
		logger.Log.Warnf("failed to update item after re-tag: %s", err.Error())
	}
	return written, changes, nil
}

// recordRetagFailure stamps a failed re-tag onto the item, splitting the failure the
// same three ways the pipeline does: what went wrong, when, and whether it is the
// kind of thing that fixes itself.
//
// ProcessedVersion is deliberately not stamped here. Leaving it stale is what makes
// the file re-attempted for free by the next processing run, exactly as a scan
// failure is — stamping it on a failure path would turn a MusicBrainz outage into a
// permanent skip.
func (r *Runner) recordRetagFailure(item models.LibraryItem, cause error) {
	now := time.Now()
	updates := map[string]any{
		"last_scanned_at":      now,
		"status":               models.LibraryItemStatusError,
		"error":                cause.Error(),
		"last_error_at":        now,
		"last_error_transient": errors.Is(cause, modules.ErrTransient),
	}
	if err := r.db.Model(&models.LibraryItem{}).Where("id = ?", item.ID).Updates(updates).Error; err != nil {
		logger.Log.Warnf("failed to record a re-tag failure for %q: %s", item.Path, err.Error())
	}
}

// RetagItem rewrites one indexed file from its stored correlation, using its
// library's tagger. It is the manual-attach counterpart to the drift sync's re-tag:
// once a file's correlation is pinned by hand, this is what actually writes the
// tags so the file stops being unmatched.
//
// It deliberately refuses while a scan or drift sync is running rather than
// queueing: both would be writing tags to the same file from another goroutine,
// and a single file is not worth the coordination.
func (r *Runner) RetagItem(itemID uuid.UUID) (int, error) {
	results, err := r.RetagItems([]uuid.UUID{itemID})
	if err != nil {
		return 0, err
	}
	if len(results) == 0 {
		return 0, errors.New("item not found")
	}
	return results[0].Written, results[0].Err
}

// RetagResult is one file's outcome from a bulk re-tag. Err is per item: one
// unreadable file must not abandon the rest of an album the user just attached.
type RetagResult struct {
	ItemID  uuid.UUID
	Path    string
	Written int
	Err     error
}

// RetagItems rewrites several indexed files from their stored correlations. Unlike the
// queued verbs it runs synchronously on the caller's goroutine and returns per-file
// results — a bulk attach needs the outcome now, not an event later.
//
// It shares jobMu with the queue worker but TryLocks it: if a background job holds the
// lock it refuses immediately rather than blocking the HTTP request behind a job that
// could run for hours. That mutual exclusion is what stops a scan and an attach writing
// the same file from two goroutines. Libraries and taggers are resolved once and reused
// across the batch, which makes attaching a 20-track album one unit of work, not 20.
//
// The results go back to the caller *and* into an Activity event. Returning them is
// what the attach flow needs in the moment; the event is what makes the write visible
// afterwards, which every other file-writing path already is. Without it a hand-attach
// was the one way to change a file that the feed never mentioned — and its Plex refresh
// appeared there parentless, describing work nothing in the feed accounted for.
//
// The collection is deliberately *not* re-derived here: the attach handler already
// requests a rebuild when it saves the correlation, so doing it again would re-derive
// the whole collection once per attached album for no new information.
func (r *Runner) RetagItems(itemIDs []uuid.UUID) ([]RetagResult, error) {
	if len(itemIDs) == 0 {
		return nil, nil
	}
	if !r.jobMu.TryLock() {
		return nil, errors.New("a background job is running — try again once it finishes")
	}
	defer r.jobMu.Unlock()
	r.running.Store(true)
	defer r.running.Store(false)
	// A re-tag reports no progress of its own, and it flips Running with no CurrentJob
	// behind it — so the bar has to be cleared here too, or a status poll during one
	// shows the last scan's.
	r.resetProgress()

	// Opened before the first write and finished after the Plex hand-off, so an
	// interactive re-tag is one run in the feed however many files it touched — the
	// same shape the queued re-tags have.
	event := events.Begin(r.db, models.EventTypeTagFiles, retagItemsTitle(len(itemIDs)))
	detail := components.NewDetailCollector(r.detailRetention)

	libraries := map[uuid.UUID]models.Library{}
	results := make([]RetagResult, 0, len(itemIDs))
	// Collect changed albums so Plex is told to refresh them, exactly like the
	// scan and drift-sync paths. Passing a live set (not nil) is also what keeps
	// retagItem's Plex hand-off from dereferencing a nil pointer when a file's
	// tags actually change and a Plex client is attached.
	refreshSet := modules.NewAlbumRefreshSet(nil)
	written, unchanged, failed := 0, 0, 0
	for _, id := range itemIDs {
		var item models.LibraryItem
		if err := r.db.First(&item, "id = ?", id).Error; err != nil {
			results = append(results, RetagResult{ItemID: id, Err: err})
			failed++
			// The row is gone, so there is no path to name it by — the ID is the only
			// identity left, and a detail row saying nothing is worse than one saying
			// which file could not be loaded.
			detail.AddError(id.String(), err)
			continue
		}
		tagsWritten, changes, err := r.retagItem(item, libraries, refreshSet)
		results = append(results, RetagResult{ItemID: id, Path: item.Path, Written: tagsWritten, Err: err})
		switch {
		case err != nil:
			failed++
			detail.AddError(item.Path, err)
		case tagsWritten > 0:
			written++
			detail.AddChanged(item.Path, tagsWritten, changes)
		default:
			// Already correct on disk. Counted but not recorded: an album attached to
			// the release its files already carried would otherwise fill the event's
			// detail with rows saying nothing happened.
			unchanged++
		}
	}

	r.flushPlex(refreshSet, event)

	status := models.EventStatusOK
	if failed > 0 {
		status = models.EventStatusError
	}
	summary := fmt.Sprintf("%d of %d files re-tagged · %d unchanged · %d errors",
		written, len(itemIDs), unchanged, failed)
	event.Stats = []models.EventStat{
		{Label: "Files re-tagged", Value: written, Kind: models.EventStatNotable, Filter: models.EventItemStatusChanged},
		{Label: "Already correct", Value: unchanged, Kind: models.EventStatMuted},
		{Label: "Failed", Value: failed, Kind: models.EventStatBad, Filter: models.EventItemStatusError},
	}
	events.Finish(r.db, event, status, summary, map[string]any{
		"files_in_scope":  len(itemIDs),
		"files_retagged":  written,
		"files_unchanged": unchanged,
		"errors":          failed,
		"detail":          detailSummary(detail),
	})
	events.AddItems(r.db, event, detail.Items())
	events.Prune(r.db, r.eventRetention)

	return results, nil
}

// retagItemsTitle names an interactive re-tag by its size. The count is the only thing
// that distinguishes one of these from another in the feed — they all come from the
// same action, on files the user has just identified by hand.
func retagItemsTitle(files int) string {
	if files == 1 {
		return "Tag 1 attached file"
	}
	return fmt.Sprintf("Tag %d attached files", files)
}

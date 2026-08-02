// Package scan runs library scans and tracks their status. It wraps the component
// pipeline (components.ScanLibrary) behind a single serial job queue (see queue.go)
// and a last-run summary, so the scheduled job, the startup run, and the API all share
// one runner and one queue.
package scan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/components"
	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/migration"
	"github.com/aunefyren/autotaggerr/mirror"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// eventRetention caps how many Activity events are kept after a scan.
const eventRetention = 200

// maxErrorFilesRecorded bounds the error-file list stored on a scan event.
const maxErrorFilesRecorded = 100

// maxDetailItemsRecorded bounds the per-file detail rows stored for one run. A large
// library can change tens of thousands of files in a single cold scan; the point of
// the detail is to show what happened, which the first few hundred rows do, so the
// rest are counted and dropped rather than turned into a table nobody reads.
const maxDetailItemsRecorded = 500

// Scan phases, reported in Summary.Phase so a running scan is legible. A scan reads
// the metadata cache, walks the libraries, then acts on what changed — these name
// each stage in order.
const (
	PhaseRefresh    = "refresh"    // re-reading metadata due for a refresh
	PhaseScanning   = "scanning"   // walking libraries and tagging files
	PhaseDrift      = "drift"      // re-tagging files of upstream-changed releases
	PhasePlex       = "plex"       // telling Plex to refresh changed albums
	PhaseMigrations = "migrations" // applying MusicBrainz redirects/deletions
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
}

// Runner owns scan execution and status. A single Runner instance is shared by
// main (cron + startup) and the API.
type Runner struct {
	db          *gorm.DB
	plex        *modules.PlexClient
	version     string
	concurrency int

	// refresh is the metadata verb. The scan owns file writes and delegates every
	// MusicBrainz read to it, so "refresh this artist" and the scan's own refresh
	// stage cannot drift apart in what they fetch.
	refresh *mirror.Runner

	running atomic.Bool // a job is currently executing
	jobMu   sync.Mutex  // held for the duration of each job; TryLock'd by interactive re-tags

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
		db:          db,
		plex:        plex,
		version:     cfg.AutotaggerrVersion,
		concurrency: cfg.AutotaggerrProcessConcurrency,
		wake:        make(chan struct{}, 1),
	}
	r.refresh = mirror.NewRunner(db, nil)
	go r.worker()
	return r
}

// Refresher exposes the metadata runner so the cron job and the API can drive the
// refresh verb without going through a scan.
func (r *Runner) Refresher() *mirror.Runner { return r.refresh }

// Status returns a copy of the current/last scan summary plus the queue. Running is
// taken from the atomic so it reflects both queue jobs and the interactive re-tags
// that never touch the summary; while a job runs the live progress atomics are folded
// in, so a scan shows a moving bar without the per-file path taking the status lock.
func (r *Runner) Status() Summary {
	r.statusMu.Lock()
	s := r.summary
	r.statusMu.Unlock()
	s.Running = r.running.Load()
	if s.Running {
		p := r.progressSnapshot()
		s.Total, s.Done, s.Phase, s.Current = p.Total, p.Done, p.Phase, p.Current
	}
	return s
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

// ErrNothingToScan reports a scope that resolved to no folders on disk. It is a
// refusal, not a failure: running it would report "0 files processed" and look like
// the scan silently did nothing.
var ErrNothingToScan = errors.New("nothing to scan: no indexed files found — run a library scan first")

// LibraryScope covers whole libraries, which is what the scheduled job, the startup
// run, and a per-library scan all want.
func LibraryScope(libraries []models.Library) Scope {
	title := "Library scan"
	if len(libraries) == 1 {
		title = "Scan of " + libraries[0].Name
	}
	targets := make([]Target, 0, len(libraries))
	for _, library := range libraries {
		targets = append(targets, Target{Library: library})
	}
	return Scope{Title: title, Targets: targets}
}

// ArtistScope covers just one artist's folders, derived from where their indexed
// files actually sit (see collection.ArtistTargets). It returns ErrNothingToScan when
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
		return Scope{}, ErrNothingToScan
	}

	return buildScope("Scan of "+artist.Name,
		map[string]any{"artist": artist.Name, "artist_mb_id": artistMBID}, found), nil
}

// ReleaseGroupScope covers the album folders of one release-group, for the narrowest
// force re-correlate. It returns ErrNothingToScan when nothing is owned of the group,
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
		return Scope{}, ErrNothingToScan
	}
	title := rg.Title
	if title == "" {
		title = rgMBID
	}
	return buildScope("Scan of "+title,
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
	r.enqueue(job{jobScanAll, "scan_all", "Scan all libraries", r.runAllNow})
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
	r.enqueue(job{jobScanLibrary, "scan_library:" + id.String(), "Scan of " + library.Name, func() {
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
	key := "scan_artist:" + scope.Title
	if mbid, ok := scope.Detail["artist_mb_id"].(string); ok && mbid != "" {
		key = "scan_artist:" + mbid
	}
	r.enqueue(job{jobScanArtist, key, scope.Title, func() { r.runScope(scope) }})
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

	event := events.Begin(r.db, models.EventTypeScan, scope.Title)

	// Reset the live progress atomics and start the flusher that writes them onto the
	// running event, so the Activity feed can draw a bar. Stopped before Finish, whose
	// Save must not race the flusher for the row.
	r.progDone.Store(0)
	r.progTotal.Store(0)
	r.setCurrent("")
	r.setPhase(PhaseRefresh)
	stopProgress := events.StartProgress(r.db, event, r.progressSnapshot)

	// Per-run MusicBrainz accounting. Only the `fetches` figure pays the rate
	// limiter, so this is what makes "why did this scan take seven hours" answerable
	// from the Activity feed instead of by guesswork.
	modules.MusicbrainzResetStats()

	refreshSet := modules.NewAlbumRefreshSet(nil)
	detail := components.NewDetailCollector(maxDetailItemsRecorded)
	var processed, unchanged, tagsWritten int
	var errorFiles []string
	libraryNames := make([]string, 0, len(scope.Targets))

	// Size the progress bar up front by counting the files the scan will visit. The
	// same walk WalkAndProcess does per root, summed across every target, so the bar
	// tracks the whole scan rather than resetting per library. One extra disk walk,
	// against the far heavier tag reads the scan is about to do.
	var totalFiles int
	for _, target := range scope.Targets {
		roots := target.Roots
		if len(roots) == 0 {
			roots = []string{target.Library.Path}
		}
		for _, root := range roots {
			totalFiles += modules.CountSupportedFiles(root)
		}
	}
	r.progTotal.Store(int64(totalFiles))

	// Refresh stage. A scan reads files through the *cache*, so without this it
	// would tag from a week-old copy and never notice a release that changed
	// upstream. Only expired entries are re-read, which is what keeps a nightly
	// scan from re-fetching the whole collection.
	//
	// Run inline: this holds the file-writing guard already, and a scan waiting on
	// a scheduled refresh it is perfectly able to run alongside would be absurd.
	refreshResult := r.refresh.RunInline(context.Background(), mirror.DueScope(modules.MusicbrainzDueForRefresh()))

	r.setPhase(PhaseScanning)
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
		c, u, tw, errs, err := components.ScanLibraryRoots(r.db, library, target.Roots, r.plex, refreshSet, detail, r.version, r.concurrency, scope.Force, onFile)
		if err != nil {
			logger.Log.Error("failed to process library '" + library.Path + "'. error: " + err.Error())
			r.setStatus(func(s *Summary) { s.LastError = err.Error() })
			continue
		}
		processed += c
		unchanged += u
		tagsWritten += tw
		errorFiles = append(errorFiles, errs...)

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

	// Drift re-tag stage. shouldSkip drops files whose size and mtime are unchanged,
	// which is every file of a release that changed only *upstream* — so the walk
	// above cannot have caught them. This is where the refresh stage's findings are
	// acted on, and it is here rather than in the refresh verb because writing files
	// is the scan's job, not the refresh's.
	drift := releaseRefresh{}
	if len(refreshResult.ChangedReleases) > 0 {
		r.setPhase(PhaseDrift)
		logger.Log.Infof("%d release(s) changed upstream; re-tagging their files", len(refreshResult.ChangedReleases))
		drift = r.retagReleases(refreshResult.ChangedReleases, refreshSet, detail)
		tagsWritten += drift.retagged
		errorFiles = append(errorFiles, drift.errorFiles...)
	}

	r.setPhase(PhasePlex)
	r.flushPlex(refreshSet)

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

	// A scan fetches releases just as a sync does, so it detects redirects and
	// deletions too — draining the queue here keeps a cold scan from leaving them
	// for whenever the next sync happens to run.
	r.setPhase(PhaseMigrations)
	migrations := r.applyMigrations()

	// Stop the flusher before Finish writes the row: its final snapshot lands the last
	// progress, and Finish then Saves the event without racing an in-flight update.
	stopProgress()

	summary := fmt.Sprintf("%d processed · %d changed · %d tags written · %d errors", processed, changed, tagsWritten, len(errorFiles))
	details := map[string]any{
		"migrations":   migrations,
		"processed":    processed,
		"unchanged":    unchanged,
		"changed":      changed,
		"tags_written": tagsWritten,
		"errors":       len(errorFiles),
		"error_files":  recorded,
		"libraries":    libraryNames,
		"duration":     end.Sub(start).String(),
		"mb_lookups":   mbStats,
		"detail":       detailSummary(detail),
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
	events.Finish(r.db, event, status, summary, details)
	events.AddItems(r.db, event, detail.Items())
	events.Prune(r.db, eventRetention)
	r.rebuildCollection()
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
		"limit":         maxDetailItemsRecorded,
	}
}

func (r *Runner) setStatus(f func(*Summary)) {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	f(&r.summary)
}

// rebuildCollection refreshes the present/collection view after a run. It is
// best-effort — a failure is logged, not propagated.
func (r *Runner) rebuildCollection() {
	if _, _, err := collection.Rebuild(r.db); err != nil {
		logger.Log.Warnf("failed to rebuild collection: %s", err.Error())
	}
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
func (r *Runner) applyMigrations() migration.Result {
	if r.db == nil {
		return migration.Result{}
	}

	res, err := migration.ProcessPending(r.db, migration.PolicyFromConfig(files.ConfigFile))
	if err != nil {
		logger.Log.Warnf("failed to process MusicBrainz migrations: %s", err.Error())
		return res
	}
	if res.Applied > 0 || res.Pending > 0 || res.Failed > 0 {
		logger.Log.Infof("MusicBrainz migrations: %d applied (%d files remapped, %d files un-identified) · %d awaiting review · %d failed",
			res.Applied, res.Files, res.Unmatched, res.Pending, res.Failed)
	}
	return res
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
// Run via `go` for background execution.
func (r *Runner) VerifyIdentities() {
	r.enqueue(job{jobRefreshVerify, "refresh_verify", "Verify identities", r.verifyIdentitiesNow})
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
func (r *Runner) RefreshArtist(artistMBID string) {
	r.enqueue(job{jobRefreshArtist, "refresh_artist:" + artistMBID, "Refresh metadata", func() {
		r.refreshArtistNow(artistMBID)
	}})
}

func (r *Runner) refreshArtistNow(artistMBID string) {
	scope, err := mirror.ArtistScope(r.db, artistMBID)
	if err != nil {
		logger.Log.Warnf("metadata refresh skipped for artist %s: %s", artistMBID, err.Error())
		return
	}

	// The discography is synced through the collection layer as well as fetched,
	// because upserting the release-group rows is what makes a newly released album
	// appear in the collection at all — the cache alone would hold it and show
	// nobody.
	if _, err := collection.SyncArtist(r.db, artistMBID); err != nil {
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
	r.enqueue(job{jobRefreshLibrary, "refresh_library:" + libraryID.String(), "Refresh metadata", func() {
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
	if err := r.db.Where("library_id = ? AND status = ?", libraryID, models.LibraryItemStatusOK).
		Order("path").Find(&items).Error; err != nil {
		logger.Log.Warnf("failed to load items for library %s: %s", library.Name, err.Error())
		return
	}

	logger.Log.Infof("re-tagging %d files for library: %s", len(items), library.Name)
	event := events.Begin(r.db, models.EventTypeDriftSync, "Tag files in "+library.Name)
	refreshSet := modules.NewAlbumRefreshSet(nil)
	detail := components.NewDetailCollector(maxDetailItemsRecorded)

	result := releaseRefresh{}
	result.retagItems(r, items, map[uuid.UUID]models.Library{}, refreshSet, detail)

	r.flushPlex(refreshSet)
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
	event := events.Begin(r.db, models.EventTypeDriftSync, "Tag files for "+artist.Name)
	refreshSet := modules.NewAlbumRefreshSet(nil)
	detail := components.NewDetailCollector(maxDetailItemsRecorded)

	result := releaseRefresh{}
	result.retagItems(r, items, map[uuid.UUID]models.Library{}, refreshSet, detail)

	r.flushPlex(refreshSet)
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
func (r *Runner) retagReleases(mbIDs []string, refreshSet *modules.AlbumRefreshSet, detail *components.DetailCollector) releaseRefresh {
	res := releaseRefresh{}
	libraries := map[uuid.UUID]models.Library{} // small per-run cache

	for _, mbID := range mbIDs {
		res.checked++
		res.changedReleases++

		var items []models.LibraryItem
		if err := r.db.Where("mb_release_id = ? AND status = ?", mbID, models.LibraryItemStatusOK).Find(&items).Error; err != nil {
			logger.Log.Warnf("failed to load items for release %s: %s", mbID, err.Error())
			continue
		}
		res.retagItems(r, items, libraries, refreshSet, detail)
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
func (r *Runner) flushPlex(refreshSet *modules.AlbumRefreshSet) {
	if r.plex == nil {
		return
	}
	albums := refreshSet.Snapshot()
	if len(albums) == 0 {
		return
	}

	event := events.Begin(r.db, models.EventTypePlexRefresh, "Plex refresh")
	refreshed := 0
	failed := make([]string, 0)
	for albumName, albumKey := range albums {
		if err := r.plex.RefreshAlbum(albumKey); err != nil {
			logger.Log.Error("failed to inform Plex to refresh album. error: " + err.Error())
			failed = append(failed, albumName)
			continue
		}
		refreshed++
		logger.Log.Info("triggered Plex refresh for album: " + albumName)
	}

	status := models.EventStatusOK
	if len(failed) > 0 {
		status = models.EventStatusError
	}
	summary := fmt.Sprintf("%d album(s) refreshed · %d failed", refreshed, len(failed))
	events.Finish(r.db, event, status, summary, map[string]any{
		"albums_refreshed": refreshed,
		"albums_failed":    len(failed),
		"failed_albums":    failed,
	})
	events.Prune(r.db, eventRetention)
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
	events.Finish(r.db, event, status, summary, details)
	events.AddItems(r.db, event, detail.Items())
	events.Prune(r.db, eventRetention)
	r.rebuildCollection()
}

// retagItem rewrites one indexed file's tags from its stored correlation and its
// library's tagger settings, then refreshes the item's on-disk identity so
// skip-unchanged stays correct. Libraries are cached across the run.
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
	unchanged, written, changes, err := modules.TagResolvedFile(item.Path, correlation, r.plex, refreshSet, library.Path, tagger.Config())
	if err != nil {
		return 0, nil, err
	}

	now := time.Now()
	updates := map[string]any{"last_scanned_at": now, "processed_version": r.version}
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

	libraries := map[uuid.UUID]models.Library{}
	results := make([]RetagResult, 0, len(itemIDs))
	// Collect changed albums so Plex is told to refresh them, exactly like the
	// scan and drift-sync paths. Passing a live set (not nil) is also what keeps
	// retagItem's Plex hand-off from dereferencing a nil pointer when a file's
	// tags actually change and a Plex client is attached.
	refreshSet := modules.NewAlbumRefreshSet(nil)
	for _, id := range itemIDs {
		var item models.LibraryItem
		if err := r.db.First(&item, "id = ?", id).Error; err != nil {
			results = append(results, RetagResult{ItemID: id, Err: err})
			continue
		}
		written, _, err := r.retagItem(item, libraries, refreshSet)
		results = append(results, RetagResult{ItemID: id, Path: item.Path, Written: written, Err: err})
	}
	r.flushPlex(refreshSet)
	return results, nil
}

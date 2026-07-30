// Package scan runs library scans and tracks their status. It wraps the component
// pipeline (components.ScanLibrary) with a single-run guard and a last-run summary
// so the scheduled job, the startup run, and the API all share one runner.
package scan

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/components"
	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/logger"
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

// Summary is the status of the current or most recent scan.
type Summary struct {
	Running     bool       `json:"running"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	Processed   int        `json:"processed"`
	Unchanged   int        `json:"unchanged"`
	Changed     int        `json:"changed"`
	TagsWritten int        `json:"tags_written"`
	Errors      int        `json:"errors"`
	LastError   string     `json:"last_error,omitempty"`
}

// Runner owns scan execution and status. A single Runner instance is shared by
// main (cron + startup) and the API.
type Runner struct {
	db          *gorm.DB
	plex        *modules.PlexClient
	version     string
	concurrency int

	running atomic.Bool // drops overlapping runs
	jobMu   sync.Mutex  // serializes run bodies

	statusMu sync.Mutex
	summary  Summary
}

// NewRunner builds a runner. plex may be nil (Plex refresh is then skipped).
func NewRunner(db *gorm.DB, plex *modules.PlexClient, cfg models.ConfigStruct) *Runner {
	return &Runner{
		db:          db,
		plex:        plex,
		version:     cfg.AutotaggerrVersion,
		concurrency: cfg.AutotaggerrProcessConcurrency,
	}
}

// Status returns a copy of the current/last scan summary.
func (r *Runner) Status() Summary {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	return r.summary
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

	// One target per library, so an artist split across two libraries is two
	// targets — each walk has to correlate against its own library root.
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

	return Scope{
		Title:   "Scan of " + artist.Name,
		Targets: targets,
		Detail:  map[string]any{"artist": artist.Name, "artist_mb_id": artistMBID, "folders": folders},
	}, nil
}

// RunAll scans every enabled library. It drops the run if one is already in
// progress. Run it via `go` for background execution.
func (r *Runner) RunAll() {
	var libraries []models.Library
	if err := r.db.Where("enabled = ?", true).Order("name").Find(&libraries).Error; err != nil {
		logger.Log.Error("failed to load libraries from database. error: " + err.Error())
		return
	}
	if len(libraries) == 0 {
		logger.Log.Info("no enabled libraries configured; nothing to scan")
		return
	}
	r.Run(LibraryScope(libraries))
}

// RunLibrary scans one library by ID. It returns an error only when the library
// cannot be loaded; the scan itself runs under the same single-run guard.
func (r *Runner) RunLibrary(id uuid.UUID) error {
	var library models.Library
	if err := r.db.First(&library, "id = ?", id).Error; err != nil {
		return err
	}
	r.Run(LibraryScope([]models.Library{library}))
	return nil
}

// RunArtist scans one artist's folders. It returns an error when the scope cannot be
// resolved (unknown artist, no files on disk); the scan itself runs under the same
// single-run guard as every other scan. Callers that want to report the resolution
// failure to a user should call ArtistScope first and Run the result.
func (r *Runner) RunArtist(artistMBID string) error {
	scope, err := r.ArtistScope(artistMBID)
	if err != nil {
		return err
	}
	r.Run(scope)
	return nil
}

// Run executes a scope. It drops the run if a scan or sync is already in progress.
// Run it via `go` for background execution.
func (r *Runner) Run(scope Scope) {
	if !r.running.CompareAndSwap(false, true) {
		logger.Log.Warn("library scan skipped: previous run still in progress")
		return
	}
	defer r.running.Store(false)

	r.jobMu.Lock()
	defer r.jobMu.Unlock()

	start := time.Now()
	r.setStatus(func(s *Summary) { *s = Summary{Running: true, StartedAt: &start} })
	logger.Log.Info("library process task starting...")

	event := events.Begin(r.db, models.EventTypeScan, scope.Title)

	// Per-run MusicBrainz accounting. Only the `fetches` figure pays the rate
	// limiter, so this is what makes "why did this scan take seven hours" answerable
	// from the Activity feed instead of by guesswork.
	modules.MusicbrainzResetStats()

	refreshSet := modules.NewAlbumRefreshSet(nil)
	detail := components.NewDetailCollector(maxDetailItemsRecorded)
	var processed, unchanged, tagsWritten int
	var errorFiles []string
	libraryNames := make([]string, 0, len(scope.Targets))

	for _, target := range scope.Targets {
		library := target.Library
		libraryNames = append(libraryNames, library.Name)
		logger.Log.Info("processing library: " + library.Path)
		c, u, tw, errs, err := components.ScanLibraryRoots(r.db, library, target.Roots, r.plex, refreshSet, detail, r.version, r.concurrency)
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

	r.flushPlex(refreshSet)

	end := time.Now()
	changed := processed - unchanged
	logger.Log.Infof("library process task finished. %d files processed. %d files not processed because of errors. %d files changed. %d tags written",
		processed, len(errorFiles), changed, tagsWritten)
	if len(errorFiles) > 0 {
		logger.Log.Warnf("files that failed to be processed:\n%s", strings.Join(errorFiles, "\n"))
	}
	logger.Log.Info("process took: " + end.Sub(start).String())

	r.setStatus(func(s *Summary) {
		s.Running = false
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

	summary := fmt.Sprintf("%d processed · %d changed · %d tags written · %d errors", processed, changed, tagsWritten, len(errorFiles))
	details := map[string]any{
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

// SyncDrift re-checks cached MusicBrainz releases whose TTL has elapsed and, when
// a release changed upstream, re-tags the indexed files that use it — the case a
// normal scan skips (unchanged on disk). It shares the single-run guard with scans
// so the two never overlap. Run via `go` for background execution.
func (r *Runner) SyncDrift() {
	if !r.running.CompareAndSwap(false, true) {
		logger.Log.Warn("metadata sync skipped: a scan or sync is already running")
		return
	}
	defer r.running.Store(false)

	r.jobMu.Lock()
	defer r.jobMu.Unlock()

	logger.Log.Info("metadata sync starting...")
	event := events.Begin(r.db, models.EventTypeDriftSync, "Metadata sync")
	refreshSet := modules.NewAlbumRefreshSet(nil)
	detail := components.NewDetailCollector(maxDetailItemsRecorded)

	result := r.refreshReleases(modules.MusicbrainzDueForRefresh(), refreshSet, detail)

	r.flushPlex(refreshSet)
	logger.Log.Info("metadata sync finished. " + result.summary())
	r.finishRefresh(event, result.summary(), result, detail, nil)
}

// RefreshArtist pulls fresh metadata for one artist: their catalogue (the
// discography, so releases added upstream appear) and every edition of theirs the
// collection holds, re-tagging the files of any release that changed.
//
// It is the drift sync narrowed to one artist with the TTL ignored, and both halves
// of that matter. The narrowing keeps a manual refresh within the MusicBrainz rate
// limit — one artist is tens of releases, not thousands. Ignoring the TTL is the
// point of asking by hand: a user who suspects a release is wrong is not helped by
// "it was checked recently, come back in a week".
//
// It shares the single-run guard with scans so the two never write tags at once. Run
// via `go` for background execution.
func (r *Runner) RefreshArtist(artistMBID string) {
	var artist models.CollectionArtist
	if err := r.db.Where("mb_id = ?", artistMBID).First(&artist).Error; err != nil {
		logger.Log.Warnf("metadata refresh skipped: artist %s not found: %s", artistMBID, err.Error())
		return
	}

	if !r.running.CompareAndSwap(false, true) {
		logger.Log.Warn("metadata refresh skipped: a scan or sync is already running")
		return
	}
	defer r.running.Store(false)

	r.jobMu.Lock()
	defer r.jobMu.Unlock()

	logger.Log.Info("metadata refresh starting for artist: " + artist.Name)
	event := events.Begin(r.db, models.EventTypeDriftSync, "Metadata refresh for "+artist.Name)
	refreshSet := modules.NewAlbumRefreshSet(nil)
	detail := components.NewDetailCollector(maxDetailItemsRecorded)

	// The catalogue first: a refresh that re-read the editions but not the
	// discography would never surface the album released last month, which is a
	// large part of why anyone presses refresh on an artist.
	catalogued, err := collection.SyncArtist(r.db, artistMBID)
	if err != nil {
		logger.Log.Warnf("failed to sync discography for %s: %s", artist.Name, err.Error())
	}

	releases, err := collection.ArtistReleaseMBIDs(r.db, artistMBID)
	if err != nil {
		logger.Log.Warnf("failed to resolve releases for %s: %s", artist.Name, err.Error())
	}
	result := r.refreshReleases(releases, refreshSet, detail)

	r.flushPlex(refreshSet)
	logger.Log.Infof("metadata refresh finished for %s. %s", artist.Name, result.summary())
	r.finishRefresh(event, result.summary(), result, detail, map[string]any{
		"artist":            artist.Name,
		"artist_mb_id":      artistMBID,
		"release_groups":    catalogued,
		"releases_in_scope": len(releases),
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

	if !r.running.CompareAndSwap(false, true) {
		logger.Log.Warn("re-tag skipped: a scan or sync is already running")
		return
	}
	defer r.running.Store(false)

	r.jobMu.Lock()
	defer r.jobMu.Unlock()

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
}

func (res releaseRefresh) summary() string {
	return fmt.Sprintf("%d releases checked · %d changed · %d files re-tagged · %d errors",
		res.checked, res.changedReleases, res.retagged, len(res.errorFiles))
}

// refreshReleases re-fetches each release from MusicBrainz and, for those whose
// content changed upstream, re-tags every indexed file that uses them — the case a
// normal scan skips, because nothing about the file on disk changed.
//
// Every caller narrowing "which releases" (the whole expired set, one artist's) goes
// through here, so they cannot drift apart in how a changed release is applied.
func (r *Runner) refreshReleases(mbIDs []string, refreshSet *modules.AlbumRefreshSet, detail *components.DetailCollector) releaseRefresh {
	res := releaseRefresh{}
	libraries := map[uuid.UUID]models.Library{} // small per-run cache

	for _, mbID := range mbIDs {
		res.checked++
		_, changed, err := modules.RefreshMusicBrainzRelease(mbID)
		if err != nil {
			logger.Log.Warnf("failed to refresh release %s: %s", mbID, err.Error())
			continue
		}
		if !changed {
			continue
		}
		res.changedReleases++
		logger.Log.Infof("release changed upstream, re-tagging affected files: %s", mbID)

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

// flushPlex tells Plex to refresh every album a run touched. A nil client (Plex not
// configured) skips it, which is the documented default.
func (r *Runner) flushPlex(refreshSet *modules.AlbumRefreshSet) {
	if r.plex == nil {
		return
	}
	for albumName, albumKey := range refreshSet.Snapshot() {
		if err := r.plex.RefreshAlbum(albumKey); err != nil {
			logger.Log.Error("failed to inform Plex to refresh album. error: " + err.Error())
		}
		logger.Log.Info("triggered Plex refresh for album: " + albumName)
	}
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
		"releases_checked": res.checked,
		"releases_changed": res.changedReleases,
		"files_retagged":   res.retagged,
		"errors":           len(res.errorFiles),
		"error_files":      recorded,
		"detail":           detailSummary(detail),
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

// RetagItems rewrites several indexed files from their stored correlations. It
// takes the scan run-guard for the whole batch rather than checking it per file:
// a bulk attach writes a whole folder, and a scan starting halfway through would
// be tagging the same files from another goroutine.
//
// Libraries and taggers are resolved once and reused across the batch, which is
// what makes attaching a 20-track album one unit of work rather than 20.
func (r *Runner) RetagItems(itemIDs []uuid.UUID) ([]RetagResult, error) {
	if len(itemIDs) == 0 {
		return nil, nil
	}
	if !r.running.CompareAndSwap(false, true) {
		return nil, errors.New("a scan is already running — try again once it finishes")
	}
	defer r.running.Store(false)

	r.jobMu.Lock()
	defer r.jobMu.Unlock()

	libraries := map[uuid.UUID]models.Library{}
	results := make([]RetagResult, 0, len(itemIDs))
	for _, id := range itemIDs {
		var item models.LibraryItem
		if err := r.db.First(&item, "id = ?", id).Error; err != nil {
			results = append(results, RetagResult{ItemID: id, Err: err})
			continue
		}
		written, _, err := r.retagItem(item, libraries, nil)
		results = append(results, RetagResult{ItemID: id, Path: item.Path, Written: written, Err: err})
	}
	return results, nil
}

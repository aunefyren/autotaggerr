// Package scan runs library scans and tracks their status. It wraps the component
// pipeline (components.ScanLibrary) with a single-run guard and a last-run summary
// so the scheduled job, the startup run, and the API all share one runner.
package scan

import (
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
	r.run(libraries)
}

// RunLibrary scans one library by ID. It returns an error only when the library
// cannot be loaded; the scan itself runs under the same single-run guard.
func (r *Runner) RunLibrary(id uuid.UUID) error {
	var library models.Library
	if err := r.db.First(&library, "id = ?", id).Error; err != nil {
		return err
	}
	r.run([]models.Library{library})
	return nil
}

func (r *Runner) run(libraries []models.Library) {
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

	title := "Library scan"
	if len(libraries) == 1 {
		title = "Scan of " + libraries[0].Name
	}
	event := events.Begin(r.db, models.EventTypeScan, title)

	refreshSet := modules.NewAlbumRefreshSet(nil)
	var processed, unchanged, tagsWritten int
	var errorFiles []string
	libraryNames := make([]string, 0, len(libraries))

	for _, library := range libraries {
		libraryNames = append(libraryNames, library.Name)
		logger.Log.Info("processing library: " + library.Path)
		c, u, tw, errs, err := components.ScanLibrary(r.db, library, r.plex, refreshSet, r.version, r.concurrency)
		if err != nil {
			logger.Log.Error("failed to process library '" + library.Path + "'. error: " + err.Error())
			r.setStatus(func(s *Summary) { s.LastError = err.Error() })
			continue
		}
		processed += c
		unchanged += u
		tagsWritten += tw
		errorFiles = append(errorFiles, errs...)

		now := time.Now()
		if err := r.db.Model(&models.Library{}).Where("id = ?", library.ID).Update("last_scan", now).Error; err != nil {
			logger.Log.Warnf("failed to record last scan time for library %s: %s", library.ID, err.Error())
		}
		logger.Log.Info("processed library: " + library.Path)
	}

	if r.plex != nil {
		for albumName, albumKey := range refreshSet.Snapshot() {
			if err := r.plex.RefreshAlbum(albumKey); err != nil {
				logger.Log.Error("failed to inform Plex to refresh album. error: " + err.Error())
			}
			logger.Log.Info("triggered Plex refresh for album: " + albumName)
		}
	}

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
	summary := fmt.Sprintf("%d processed · %d changed · %d tags written · %d errors", processed, changed, tagsWritten, len(errorFiles))
	events.Finish(r.db, event, status, summary, map[string]any{
		"processed":    processed,
		"unchanged":    unchanged,
		"changed":      changed,
		"tags_written": tagsWritten,
		"errors":       len(errorFiles),
		"error_files":  recorded,
		"libraries":    libraryNames,
		"duration":     end.Sub(start).String(),
	})
	events.Prune(r.db, eventRetention)
	r.rebuildCollection()
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

	due := modules.MusicbrainzDueForRefresh()
	checked, changedReleases, retagged := 0, 0, 0
	var errorFiles []string
	libraries := map[uuid.UUID]models.Library{} // small per-run cache

	for _, mbID := range due {
		checked++
		_, changed, err := modules.RefreshMusicBrainzRelease(mbID)
		if err != nil {
			logger.Log.Warnf("failed to refresh release %s: %s", mbID, err.Error())
			continue
		}
		if !changed {
			continue
		}
		changedReleases++
		logger.Log.Infof("release changed upstream, re-tagging affected files: %s", mbID)

		var items []models.LibraryItem
		if err := r.db.Where("mb_release_id = ? AND status = ?", mbID, models.LibraryItemStatusOK).Find(&items).Error; err != nil {
			logger.Log.Warnf("failed to load items for release %s: %s", mbID, err.Error())
			continue
		}
		for _, item := range items {
			written, err := r.retagItem(item, libraries, refreshSet)
			if err != nil {
				logger.Log.Errorf("failed to re-tag '%s'. error: %s", item.Path, err.Error())
				errorFiles = append(errorFiles, item.Path)
				continue
			}
			if written > 0 {
				retagged++
			}
		}
	}

	if r.plex != nil {
		for albumName, albumKey := range refreshSet.Snapshot() {
			if err := r.plex.RefreshAlbum(albumKey); err != nil {
				logger.Log.Error("failed to inform Plex to refresh album. error: " + err.Error())
			}
			logger.Log.Info("triggered Plex refresh for album: " + albumName)
		}
	}

	status := models.EventStatusOK
	if len(errorFiles) > 0 {
		status = models.EventStatusError
	}
	recorded := errorFiles
	if len(recorded) > maxErrorFilesRecorded {
		recorded = recorded[:maxErrorFilesRecorded]
	}
	summary := fmt.Sprintf("%d releases checked · %d changed · %d files re-tagged · %d errors", checked, changedReleases, retagged, len(errorFiles))
	logger.Log.Info("metadata sync finished. " + summary)
	events.Finish(r.db, event, status, summary, map[string]any{
		"releases_checked": checked,
		"releases_changed": changedReleases,
		"files_retagged":   retagged,
		"errors":           len(errorFiles),
		"error_files":      recorded,
	})
	events.Prune(r.db, eventRetention)
	r.rebuildCollection()
}

// retagItem rewrites one indexed file's tags from its stored correlation and its
// library's tagger settings, then refreshes the item's on-disk identity so
// skip-unchanged stays correct. Libraries are cached across the run.
func (r *Runner) retagItem(item models.LibraryItem, libraries map[uuid.UUID]models.Library, refreshSet *modules.AlbumRefreshSet) (int, error) {
	library, ok := libraries[item.LibraryID]
	if !ok {
		if err := r.db.First(&library, "id = ?", item.LibraryID).Error; err != nil {
			return 0, err
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
		return 0, nil
	}
	unchanged, written, err := modules.TagResolvedFile(item.Path, correlation, r.plex, refreshSet, library.Path, tagger.Config())
	if err != nil {
		return 0, err
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
	return written, nil
}

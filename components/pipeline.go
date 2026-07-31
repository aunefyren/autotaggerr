package components

import (
	"os"
	"sync"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// DetailCollector gathers the per-file outcomes of a run — which files changed, the
// exact fields that changed on each, and which failed — so the Activity feed can
// answer "what did that scan actually do to my files". Counters alone cannot: they
// say twelve files changed, never which twelve.
//
// It is concurrency-safe because scans process files on a worker pool, and bounded
// because a large library would otherwise produce a row per file: once `limit`
// entries are held, further ones are counted and dropped, so the UI can say "showing
// the first N of M" instead of the process growing without limit. A nil collector
// disables collection entirely, which is what DB-less and single-file callers want.
type DetailCollector struct {
	mu      sync.Mutex
	limit   int
	items   []models.EventItem
	changed int // total changed files seen, including any past the limit
	failed  int // total failed files seen, including any past the limit
}

// NewDetailCollector returns a collector holding at most limit entries. A limit < 1
// disables collection (the collector stays usable, so callers need no nil checks).
func NewDetailCollector(limit int) *DetailCollector {
	return &DetailCollector{limit: limit}
}

// AddChanged records a file whose tags were rewritten, with its field-level diff.
func (d *DetailCollector) AddChanged(path string, tagsWritten int, changes []models.TagChange) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.changed++
	d.append(models.EventItem{
		Path:        path,
		Status:      models.EventItemStatusChanged,
		TagsWritten: tagsWritten,
		Changes:     changes,
	})
}

// AddError records a file that could not be processed.
func (d *DetailCollector) AddError(path string, err error) {
	if d == nil || err == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failed++
	d.append(models.EventItem{
		Path:   path,
		Status: models.EventItemStatusError,
		Error:  err.Error(),
	})
}

// append stores an entry if there is room. Callers hold the lock.
func (d *DetailCollector) append(item models.EventItem) {
	if d.limit < 1 || len(d.items) >= d.limit {
		return
	}
	d.items = append(d.items, item)
}

// Items returns the collected entries, safe to use after the run.
func (d *DetailCollector) Items() []models.EventItem {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]models.EventItem(nil), d.items...)
}

// Totals reports how many changed and failed files were seen in total — including
// those past the limit, which is what makes a truncated list honest about it.
func (d *DetailCollector) Totals() (changed, failed int) {
	if d == nil {
		return 0, 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.changed, d.failed
}

// ProcessFile runs the per-file pipeline for one library: the Manager resolves
// the correlation, the Tagger writes tags (via the shared engine), and the
// result is recorded into the library_items index. A nil db skips indexing (used
// by tests / DB-less callers); a nil plexClient/refreshSet skips Plex queuing, and a
// nil detail collector skips per-file Activity detail.
//
// A pinned item is the exception: its correlation was chosen by hand, so it is
// reused as-is instead of being resolved again. That governs the tags written to the
// file as well as the index row — resolving would otherwise tag a pinned file to
// whatever the manager thinks it is, contradicting the pin the index still reports.
func ProcessFile(
	db *gorm.DB,
	library models.Library,
	manager Manager,
	tagger *Tagger,
	plexClient *modules.PlexClient,
	refreshSet *modules.AlbumRefreshSet,
	detail *DetailCollector,
	filePath, rootDir, processedVersion string,
) (unchanged bool, tagsWritten int, err error) {
	managerType := manager.Type()

	correlation, pinned := pinnedCorrelation(db, filePath)
	if !pinned {
		correlation, err = manager.Correlate(filePath, rootDir)
		if err != nil {
			recordItem(db, library.ID, filePath, models.Correlation{}, false, processedVersion, managerType, err)
			detail.AddError(filePath, err)
			return false, 0, err
		}
	}

	unchanged = true // no tag write unless the profile enables it
	if tagger.WriteEnabled() {
		var changes []models.TagChange
		unchanged, tagsWritten, changes, err = modules.TagResolvedFile(filePath, correlation, plexClient, refreshSet, rootDir, tagger.Config())
		if err != nil {
			recordItem(db, library.ID, filePath, correlation, false, processedVersion, managerType, err)
			detail.AddError(filePath, err)
			return unchanged, tagsWritten, err
		}
		if !unchanged {
			detail.AddChanged(filePath, tagsWritten, changes)
		}
	}

	recordItem(db, library.ID, filePath, correlation, unchanged, processedVersion, managerType, nil)
	return unchanged, tagsWritten, nil
}

// ScanLibrary processes one library end-to-end: it builds the library's manager
// and tagger, then walks the folder (shared worker pool), skipping files whose
// index entry shows they are unchanged since the last successful scan and
// processing the rest through ProcessFile. Counters match modules.ScanFolderRecursive.
func ScanLibrary(
	db *gorm.DB,
	library models.Library,
	plexClient *modules.PlexClient,
	refreshSet *modules.AlbumRefreshSet,
	detail *DetailCollector,
	processedVersion string,
	workers int,
) (counter, unchangedFiles, tagsWritten int, errorFiles []string, err error) {
	return ScanLibraryRoots(db, library, nil, plexClient, refreshSet, detail, processedVersion, workers, nil)
}

// ScanLibraryRoots is ScanLibrary narrowed to part of a library: it walks each of
// `roots` instead of the library folder, and an empty roots walks the whole library
// (which is exactly what ScanLibrary asks for).
//
// The distinction that makes a partial scan safe is between the folder *walked* and
// the folder files are *resolved against*. Only the walk narrows; `library.Path`
// stays the correlation root, because the path convention the manager reads
// (<root>/<ARTIST>/<ALBUM>/...) is anchored there. Passing the narrowed folder as the
// root instead would make every file one segment too shallow and correlate the whole
// scope to the wrong album.
//
// Roots are walked in sequence, each with the full worker pool, and their counters
// summed. A root that no longer exists on disk is walked as an empty folder — the
// zero counters that produces are the honest report for "the folder is gone", and it
// keeps one stale target from abandoning the others.
//
// onFile, if non-nil, is called once per processed file with its path; it is passed
// straight through to WalkAndProcess so the scan runner can advance a live progress
// counter. It must be cheap and concurrency-safe.
func ScanLibraryRoots(
	db *gorm.DB,
	library models.Library,
	roots []string,
	plexClient *modules.PlexClient,
	refreshSet *modules.AlbumRefreshSet,
	detail *DetailCollector,
	processedVersion string,
	workers int,
	onFile func(path string),
) (counter, unchangedFiles, tagsWritten int, errorFiles []string, err error) {
	manager, tagger, err := BuildForLibrary(db, library)
	if err != nil {
		return 0, 0, 0, nil, err
	}
	if len(roots) == 0 {
		roots = []string{library.Path}
	}

	managerType := manager.Type()
	errorFiles = []string{}
	for _, root := range roots {
		c, u, tw, errs, walkErr := modules.WalkAndProcess(root, workers, func(path string) (bool, int, error) {
			if shouldSkip(db, path, processedVersion, managerType) {
				return true, 0, nil // counts as unchanged
			}
			return ProcessFile(db, library, manager, tagger, plexClient, refreshSet, detail, path, library.Path, processedVersion)
		}, onFile)
		counter += c
		unchangedFiles += u
		tagsWritten += tw
		errorFiles = append(errorFiles, errs...)
		if walkErr != nil {
			return counter, unchangedFiles, tagsWritten, errorFiles, walkErr
		}
	}
	return counter, unchangedFiles, tagsWritten, errorFiles, nil
}

// shouldSkip reports whether a file can be skipped this scan: its index row exists
// and is healthy (status ok with a correlation), the running app version still
// matches the one that tagged it, the correlation came from the library's current
// manager, and the file is byte-identical on disk (same size and modification
// second). It deliberately does not detect upstream MusicBrainz changes — that is
// the drift sync's job (M4).
//
// The manager check is what makes a manager swap take effect: without it a file
// keeps reporting the old correlation_source forever, because nothing else about
// it changed. Pinned items are exempt — a manual correlation is not the manager's
// to redo, and re-correlating one would overwrite the pin's MB IDs.
func shouldSkip(db *gorm.DB, filePath, processedVersion, managerType string) bool {
	if db == nil {
		return false
	}

	var item models.LibraryItem
	if err := db.Where("path = ?", filePath).First(&item).Error; err != nil {
		return false // not indexed yet
	}
	if item.Status != models.LibraryItemStatusOK || item.MBReleaseID == "" {
		return false
	}
	if item.ProcessedVersion != processedVersion || item.ModTime == nil {
		return false
	}
	if !item.Pinned && item.CorrelatedByManager != managerType {
		return false // manager changed (or predates this column) -> re-correlate
	}

	fi, err := os.Stat(filePath)
	if err != nil {
		return false
	}
	// Compare to the second: SQLite time round-tripping can drop sub-second precision.
	return item.Size == fi.Size() && item.ModTime.Unix() == fi.ModTime().Unix()
}

// pinnedCorrelation returns a file's stored correlation when its index row is pinned
// and actually holds a release. Anything else (no DB, no row, unpinned, or a pin
// without a release ID to reuse) reports false so the caller resolves normally.
func pinnedCorrelation(db *gorm.DB, filePath string) (models.Correlation, bool) {
	if db == nil {
		return models.Correlation{}, false
	}

	var item models.LibraryItem
	if err := db.Where("path = ?", filePath).First(&item).Error; err != nil {
		return models.Correlation{}, false
	}
	if !item.Pinned || item.MBReleaseID == "" {
		return models.Correlation{}, false
	}

	return models.Correlation{
		MBReleaseID:      item.MBReleaseID,
		MBReleaseTrackID: item.MBReleaseTrackID,
		MBRecordingID:    item.MBRecordingID,
		Source:           item.CorrelationSource,
	}, true
}

// TaggerForLibrary returns just the tagger a library is configured with (no
// manager construction) — used by the drift sync's re-tag path.
func TaggerForLibrary(db *gorm.DB, library models.Library) *Tagger {
	return NewTagger(resolveTaggerProfile(db, library, true))
}

// BuildForLibrary assembles the manager and tagger a library is configured with,
// falling back to the first configured (or a native/default) component when the
// library has no explicit assignment.
func BuildForLibrary(db *gorm.DB, library models.Library) (Manager, *Tagger, error) {
	managerRow, err := resolveManagerRow(db, library, true)
	if err != nil {
		return nil, nil, err
	}
	manager, err := NewManager(managerRow)
	if err != nil {
		return nil, nil, err
	}
	return manager, NewTagger(resolveTaggerProfile(db, library, true)), nil
}

// recordItem upserts the library_items row for a file: its correlation, on-disk
// identity (size/mtime), and scan/tag timestamps. Failures to record are logged
// but never abort processing — the index is a cache of decisions, not the source
// of truth for whether a file was tagged.
func recordItem(db *gorm.DB, libraryID uuid.UUID, filePath string, correlation models.Correlation, unchanged bool, processedVersion, managerType string, procErr error) {
	if db == nil {
		return
	}

	item := models.LibraryItem{Path: filePath}
	if err := db.Where("path = ?", filePath).FirstOrInit(&item).Error; err != nil {
		logger.Log.Warnf("failed to load library item for %q: %s", filePath, err.Error())
		return
	}

	now := time.Now()
	item.LibraryID = libraryID
	item.LastScannedAt = &now

	if fi, statErr := os.Stat(filePath); statErr == nil {
		item.Size = fi.Size()
		mod := fi.ModTime()
		item.ModTime = &mod
	}

	if procErr != nil {
		item.Status = models.LibraryItemStatusError
		item.Error = procErr.Error()
	} else {
		item.Status = models.LibraryItemStatusOK
		item.Error = ""
		item.ProcessedVersion = processedVersion
		if correlation.MBReleaseID != "" {
			// Never override a manual/pinned correlation with an automatic one — the MB
			// IDs included, not just the source label. Guarding only the label left a
			// re-processed pinned file (version bump, edited file) pointing at the
			// manager's release while still reporting "manual".
			if !item.Pinned {
				item.MBReleaseID = correlation.MBReleaseID
				item.MBRecordingID = correlation.MBRecordingID
				item.MBReleaseTrackID = correlation.MBReleaseTrackID
				item.CorrelationSource = correlation.Source
				item.CorrelatedByManager = managerType
			}
			if item.CorrelatedAt == nil {
				item.CorrelatedAt = &now
			}
		}
		if !unchanged {
			item.LastTaggedAt = &now
		}
	}

	if err := db.Save(&item).Error; err != nil {
		logger.Log.Warnf("failed to record library item for %q: %s", filePath, err.Error())
	}
}

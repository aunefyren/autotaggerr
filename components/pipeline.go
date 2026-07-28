package components

import (
	"os"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ProcessFile runs the per-file pipeline for one library: the Manager resolves
// the correlation, the Tagger writes tags (via the shared engine), and the
// result is recorded into the library_items index. A nil db skips indexing (used
// by tests / DB-less callers); a nil plexClient/refreshSet skips Plex queuing.
func ProcessFile(
	db *gorm.DB,
	library models.Library,
	manager Manager,
	tagger *Tagger,
	plexClient *modules.PlexClient,
	refreshSet *modules.AlbumRefreshSet,
	filePath, rootDir, processedVersion string,
) (unchanged bool, tagsWritten int, err error) {
	correlation, err := manager.Correlate(filePath, rootDir)
	if err != nil {
		recordItem(db, library.ID, filePath, models.Correlation{}, false, processedVersion, err)
		return false, 0, err
	}

	unchanged = true // no tag write unless the profile enables it
	if tagger.WriteEnabled() {
		unchanged, tagsWritten, err = modules.TagResolvedFile(filePath, correlation, plexClient, refreshSet, rootDir, tagger.Config())
		if err != nil {
			recordItem(db, library.ID, filePath, correlation, false, processedVersion, err)
			return unchanged, tagsWritten, err
		}
	}

	recordItem(db, library.ID, filePath, correlation, unchanged, processedVersion, nil)
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
	processedVersion string,
	workers int,
) (counter, unchangedFiles, tagsWritten int, errorFiles []string, err error) {
	manager, tagger, err := BuildForLibrary(db, library)
	if err != nil {
		return 0, 0, 0, nil, err
	}

	return modules.WalkAndProcess(library.Path, workers, func(path string) (bool, int, error) {
		if shouldSkip(db, path, processedVersion) {
			return true, 0, nil // counts as unchanged
		}
		return ProcessFile(db, library, manager, tagger, plexClient, refreshSet, path, library.Path, processedVersion)
	})
}

// shouldSkip reports whether a file can be skipped this scan: its index row exists
// and is healthy (status ok with a correlation), the running app version still
// matches the one that tagged it, and the file is byte-identical on disk (same
// size and modification second). It deliberately does not detect upstream
// MusicBrainz changes — that is the drift sync's job (M4).
func shouldSkip(db *gorm.DB, filePath, processedVersion string) bool {
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

	fi, err := os.Stat(filePath)
	if err != nil {
		return false
	}
	// Compare to the second: SQLite time round-tripping can drop sub-second precision.
	return item.Size == fi.Size() && item.ModTime.Unix() == fi.ModTime().Unix()
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
func recordItem(db *gorm.DB, libraryID uuid.UUID, filePath string, correlation models.Correlation, unchanged bool, processedVersion string, procErr error) {
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
			item.MBReleaseID = correlation.MBReleaseID
			item.MBRecordingID = correlation.MBRecordingID
			item.MBReleaseTrackID = correlation.MBReleaseTrackID
			// Never downgrade a manual/pinned source with an automatic one.
			if !item.Pinned {
				item.CorrelationSource = correlation.Source
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

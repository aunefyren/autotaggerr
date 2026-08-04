package scan

// Pruning index rows whose files are gone.
//
// library_items is keyed by path, and a scan only ever writes rows for files it
// finds — so nothing had ever removed one. A file that a manager moved, renamed or
// deleted left its row behind with status ok and its release still set, and
// collection.Rebuild counts exactly those rows: it selects `status = ok AND
// mb_release_id <> ''` with no existence check. The library therefore kept owning
// albums it no longer had, under whatever artist the old path resolved to.
//
// Nothing self-healed it either. collection.ArtistTargets derives the folder to
// re-scan *from the stale path*, and walking a folder that no longer exists yields an
// empty walk rather than an error, so every subsequent scan confirmed the ghost by
// never visiting it.
//
// The pass below is therefore an existence check over the index rather than a diff
// against what the walk visited. That is deliberate: it stays correct for the
// narrowed scopes (one artist, one release-group), which visit a subset of a library
// and must not be allowed to conclude anything about the rest of it.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// pruneDeleteBatch bounds the id list per DELETE. SQLite's default host-parameter
// limit is the constraint, and a library that lost a whole disc can exceed it.
const pruneDeleteBatch = 200

// pruneMissingItems deletes the index rows under roots whose files are no longer on
// disk, and reports how many went. Empty roots means the whole library.
//
// Two guards are the whole design, because the failure mode is deleting a library's
// index rather than a few rows:
//
//   - **Every root must exist.** An unmounted library, or one whose path moved,
//     stats as "every file is gone" and would otherwise empty its own index. The
//     caller also only runs this after a successful walk, so a root that vanished
//     mid-scan is already excluded.
//   - **Only fs.ErrNotExist counts as gone.** A permission error, an I/O error or a
//     dead network mount leaves the row alone. Absence has to be proven, not
//     assumed — the one reading that cannot be recovered from is a wrong deletion.
//
// A pinned row is a manual attachment, so losing one loses authored state. It is
// still deleted — the pin identifies a file that no longer exists — but it is
// counted and logged separately, because "your manual attachments went away" is not
// something the user should have to infer from a file count.
func pruneMissingItems(db *gorm.DB, library models.Library, roots []string) (int, error) {
	if db == nil {
		return 0, nil
	}

	walked := roots
	if len(walked) == 0 {
		walked = []string{library.Path}
	}
	for _, root := range walked {
		if _, err := os.Stat(root); err != nil {
			return 0, fmt.Errorf("scan root %q is unavailable, skipping prune: %w", root, err)
		}
	}

	var items []models.LibraryItem
	if err := db.Where("library_id = ?", library.ID).Find(&items).Error; err != nil {
		return 0, err
	}

	gone := make([]uuid.UUID, 0)
	pinned := 0
	for _, item := range items {
		if !pathInScope(item.Path, roots) {
			continue
		}
		if _, err := os.Stat(item.Path); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			logger.Log.Warnf("cannot tell whether %q still exists, keeping its index row: %s", item.Path, err.Error())
			continue
		}
		gone = append(gone, item.ID)
		if item.Pinned {
			pinned++
			logger.Log.Warnf("dropping the manual attachment for %q: the file is no longer on disk", item.Path)
		}
		logger.Log.Debugf("pruning index row for missing file: %s", item.Path)
	}
	if len(gone) == 0 {
		return 0, nil
	}

	removed := 0
	for start := 0; start < len(gone); start += pruneDeleteBatch {
		end := min(start+pruneDeleteBatch, len(gone))
		if err := db.Where("id IN ?", gone[start:end]).Delete(&models.LibraryItem{}).Error; err != nil {
			// Report what did land: the caller records it on the event, and a partial
			// prune is still progress rather than something to roll back.
			return removed, err
		}
		removed += end - start
	}

	logger.Log.Infof("pruned %d index row(s) in %q whose files are gone (%d manual attachment(s) among them)", removed, library.Name, pinned)
	return removed, nil
}

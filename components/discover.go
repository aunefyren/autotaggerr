package components

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// Discovery is the disk-walking half of the Scan verb, run only when it is asked for.
//
// Scan on its own proves that each indexed file is still there and drops the rows it
// cannot find, so it sees a file *leave* its path and never where it went. A manager
// that renames or moves files — Lidarr re-importing after a tag write, say — therefore
// emptied an album that was intact on disk, and no amount of re-scanning brought it
// back: the rows were gone, and nothing short of a Process walked the folder again.
//
// This closes that gap without becoming Process. It walks the scope, carries the
// identity of a moved file over to its new path, and asks the manager about files the
// index has no identity for. It **writes no audio file**: a file it resolves is
// recorded without a processed version, which is what makes the next Process tag it
// rather than skip it as unchanged (see shouldSkip).

// DiscoverStats is what one discovery pass did.
type DiscoverStats struct {
	// Walked is every supported audio file found on disk in scope.
	Walked int
	// Moved is index rows carried over to the file's new path.
	Moved int
	// Resolved is files the manager gave an identity.
	Resolved int
	// Unmatched is files the manager answered it does not know.
	Unmatched int
	// Failed is files whose lookup failed.
	Failed []string
}

// Add folds another pass's counts into this one, for a run over several libraries.
func (s *DiscoverStats) Add(o DiscoverStats) {
	s.Walked += o.Walked
	s.Moved += o.Moved
	s.Resolved += o.Resolved
	s.Unmatched += o.Unmatched
	s.Failed = append(s.Failed, o.Failed...)
}

// moveKey is what a file keeps through a rename or a move on the same filesystem:
// its size and its modification time, to the second (SQLite round-tripping drops the
// rest, which is why shouldSkip compares the same way).
type moveKey struct {
	size  int64
	mtime int64
}

// DiscoverLibraryRoots runs discovery over part of a library — each of roots, or the
// whole library when roots is empty. Like ScanLibraryRoots, only the walk narrows:
// files are correlated against library.Path, where the path convention is anchored.
//
// The library root must exist. An unmounted library walks as empty, and reading that
// as "nothing here" is harmless for discovery itself — but the Scan that follows would
// then have nothing to compare against, so refusing here is the honest answer.
func DiscoverLibraryRoots(db *gorm.DB, library models.Library, roots []string, detail *DetailCollector, workers int) (DiscoverStats, error) {
	if db == nil {
		return DiscoverStats{Failed: []string{}}, nil
	}
	manager, _, err := BuildForLibrary(db, library)
	if err != nil {
		return DiscoverStats{Failed: []string{}}, err
	}
	return discoverWith(db, library, manager, roots, detail, workers)
}

// discoverWith is DiscoverLibraryRoots with the manager supplied.
func discoverWith(db *gorm.DB, library models.Library, manager Manager, roots []string, detail *DetailCollector, workers int) (DiscoverStats, error) {
	stats := DiscoverStats{Failed: []string{}}
	if _, err := os.Stat(library.Path); err != nil {
		return stats, fmt.Errorf("library root %q is unavailable, skipping discovery: %w", library.Path, err)
	}
	if len(roots) == 0 {
		roots = []string{library.Path}
	}

	onDisk, err := walkAudioFiles(roots)
	if err != nil {
		return stats, err
	}
	stats.Walked = len(onDisk)

	var rows []models.LibraryItem
	if err := db.Where("library_id = ?", library.ID).Find(&rows).Error; err != nil {
		return stats, err
	}
	indexed := make(map[string]models.LibraryItem, len(rows))
	var gone []models.LibraryItem
	for _, row := range rows {
		if !underAny(row.Path, roots) {
			continue
		}
		indexed[row.Path] = row
		if _, statErr := os.Stat(row.Path); statErr != nil && errors.Is(statErr, fs.ErrNotExist) {
			gone = append(gone, row)
		}
	}

	var unknown []string       // on disk, no row
	var needsIdentity []string // a row, but nothing to tag it from
	for _, path := range onDisk {
		row, ok := indexed[path]
		if !ok {
			unknown = append(unknown, path)
			continue
		}
		if row.Pinned {
			continue // a hand-chosen identity is not the manager's to redo
		}
		if row.MBReleaseID == "" || row.Status == models.LibraryItemStatusUnmatched {
			needsIdentity = append(needsIdentity, path)
		}
	}

	moved, rest := carryMovedRows(db, gone, unknown)
	stats.Moved = moved

	resolveIdentities(db, library, manager, append(rest, needsIdentity...), detail, workers, &stats)

	logger.Log.Infof("discovery in %q: %d files on disk · %d moved · %d resolved · %d unmatched · %d failed",
		library.Name, stats.Walked, stats.Moved, stats.Resolved, stats.Unmatched, len(stats.Failed))
	return stats, nil
}

// carryMovedRows moves the index row of a vanished file onto an unindexed file that is
// the same bytes, and returns how many it moved and the unindexed paths left over.
//
// A pair is taken only when it is **unique on both sides**: one vanished row and one
// new file sharing a size and modification second. Anything else — two candidates for
// one row, or one file that could be either of two rows — is left to the manager to
// resolve, because carrying an identity onto the wrong file is a mistag with the
// manager's name on it, and resolving costs one lookup.
//
// The whole row moves, pin included: a manual attachment is a fact about the file, and
// the file did not change. Only its path, and when it was last seen, are new.
func carryMovedRows(db *gorm.DB, gone []models.LibraryItem, unknown []string) (int, []string) {
	if len(gone) == 0 || len(unknown) == 0 {
		return 0, unknown
	}

	goneBy := map[moveKey][]models.LibraryItem{}
	for _, row := range gone {
		if row.ModTime == nil {
			continue
		}
		k := moveKey{row.Size, row.ModTime.Unix()}
		goneBy[k] = append(goneBy[k], row)
	}
	newBy := map[moveKey][]string{}
	for _, path := range unknown {
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		k := moveKey{fi.Size(), fi.ModTime().Unix()}
		newBy[k] = append(newBy[k], path)
	}

	moved := 0
	taken := map[string]bool{}
	now := time.Now()
	for k, rows := range goneBy {
		paths := newBy[k]
		if len(rows) != 1 || len(paths) != 1 {
			if len(paths) > 0 {
				logger.Log.Infof("not carrying identity by size and time: %d vanished row(s) and %d new file(s) share one, resolving instead", len(rows), len(paths))
			}
			continue
		}
		row, path := rows[0], paths[0]
		if err := db.Model(&models.LibraryItem{}).Where("id = ?", row.ID).
			Updates(map[string]any{"path": path, "last_scanned_at": now}).Error; err != nil {
			logger.Log.Warnf("failed to carry the index row for %q over to %q: %s", row.Path, path, err.Error())
			continue
		}
		logger.Log.Infof("file moved on disk, identity carried over: %q -> %q", row.Path, path)
		moved++
		// A row that moved without an identity still needs one, so its file stays on the
		// list to resolve — now against the row at its new path.
		if row.Pinned || (row.MBReleaseID != "" && row.Status != models.LibraryItemStatusUnmatched) {
			taken[path] = true
		}
	}

	rest := make([]string, 0, len(unknown)-len(taken))
	for _, path := range unknown {
		if !taken[path] {
			rest = append(rest, path)
		}
	}
	return moved, rest
}

// resolveIdentities asks the manager about each path and records the answer without
// touching the file. The processed version is recorded blank on purpose: it is what
// tells the next Process this file has never been tagged against its identity.
func resolveIdentities(db *gorm.DB, library models.Library, manager Manager, paths []string, detail *DetailCollector, workers int, stats *DiscoverStats) {
	if len(paths) == 0 {
		return
	}
	if workers < 1 {
		workers = 1
	}
	managerType := manager.Type()

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)
	for _, path := range paths {
		wg.Add(1)
		sem <- struct{}{}
		go func(path string) {
			defer wg.Done()
			defer func() { <-sem }()

			correlation, err := manager.Correlate(path, library.Path)
			switch {
			case err == nil:
				recordItem(db, library.ID, path, correlation, true, "", managerType, nil)
				mu.Lock()
				stats.Resolved++
				mu.Unlock()
			case errors.Is(err, modules.ErrUnmatched):
				recordItem(db, library.ID, path, models.Correlation{}, true, "", managerType, err)
				mu.Lock()
				stats.Unmatched++
				mu.Unlock()
			default:
				recordItem(db, library.ID, path, models.Correlation{}, false, "", managerType, err)
				detail.AddError(path, err)
				mu.Lock()
				stats.Failed = append(stats.Failed, path)
				mu.Unlock()
			}
		}(path)
	}
	wg.Wait()
}

// walkAudioFiles lists the supported audio files under each root. A root that no
// longer exists walks as empty, as it does for ScanLibraryRoots; any other walk error
// is returned, because a walk that stopped partway would make every file past the
// failure look absent.
func walkAudioFiles(roots []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				if errors.Is(walkErr, fs.ErrNotExist) && path == root {
					return filepath.SkipDir
				}
				return walkErr
			}
			if d.IsDir() || !modules.IsSupportedAudioFile(path) || seen[path] {
				return nil
			}
			seen[path] = true
			out = append(out, path)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walking %q: %w", root, err)
		}
	}
	return out, nil
}

// underAny reports whether path sits in one of roots.
func underAny(path string, roots []string) bool {
	clean := filepath.Clean(path)
	for _, root := range roots {
		root = filepath.Clean(root)
		if clean == root || strings.HasPrefix(clean, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

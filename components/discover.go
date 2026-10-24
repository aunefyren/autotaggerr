package components

import (
	"context"
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
	"github.com/google/uuid"
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

// copyKey is what a file keeps through a move that rewrites it — across filesystems,
// or by a tool that copies and deletes — which resets the modification time: its size,
// its name, and the name of the album folder it sat in. A rename changes the name and
// keeps the time; a copy keeps the name and loses the time; so between them the two
// keys cover both, and neither alone has to be loose enough to cover the other.
type copyKey struct {
	size   int64
	name   string
	folder string
}

func copyKeyOf(path string, size int64) copyKey {
	return copyKey{size: size, name: filepath.Base(path), folder: filepath.Base(filepath.Dir(path))}
}

// DiscoverLibraryRoots runs discovery over part of a library — each of roots, or the
// whole library when roots is empty. Like ScanLibraryRoots, only the walk narrows:
// files are correlated against library.Path, where the path convention is anchored.
//
// The library root must exist. An unmounted library walks as empty, and reading that
// as "nothing here" is harmless for discovery itself — but the Scan that follows would
// then have nothing to compare against, so refusing here is the honest answer.
//
// ctx stops it between lookups; a stopped pass returns ctx's error with what it did.
func DiscoverLibraryRoots(ctx context.Context, db *gorm.DB, library models.Library, roots []string, detail *DetailCollector, workers int) (DiscoverStats, error) {
	if db == nil {
		return DiscoverStats{Failed: []string{}}, nil
	}
	manager, _, err := BuildForLibrary(db, library)
	if err != nil {
		return DiscoverStats{Failed: []string{}}, err
	}
	return discoverWithContext(ctx, db, library, manager, roots, detail, workers)
}

// discoverWith is DiscoverLibraryRoots with the manager supplied.
func discoverWith(db *gorm.DB, library models.Library, manager Manager, roots []string, detail *DetailCollector, workers int) (DiscoverStats, error) {
	return discoverWithContext(context.Background(), db, library, manager, roots, detail, workers)
}

func discoverWithContext(ctx context.Context, db *gorm.DB, library models.Library, manager Manager, roots []string, detail *DetailCollector, workers int) (DiscoverStats, error) {
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

	moved, rest, stranded := carryMovedRows(db, gone, unknown)
	stats.Moved = moved
	if len(stranded) > 0 && narrowed(roots, library.Path) {
		more, _ := carryFromElsewhere(db, library, roots, rows, stranded)
		stats.Moved += more
	}

	resolveIdentities(ctx, db, library, manager, append(rest, needsIdentity...), detail, workers, &stats)
	if err := ctx.Err(); err != nil {
		return stats, err
	}

	logger.Log.Infof("discovery in %q: %d files on disk · %d moved · %d resolved · %d unmatched · %d failed",
		library.Name, stats.Walked, stats.Moved, stats.Resolved, stats.Unmatched, len(stats.Failed))
	return stats, nil
}

// carryMovedRows moves the index row of a vanished file onto an unindexed file that is
// the same bytes. It returns how many it moved, the unindexed paths left over, and the
// vanished rows it found no partner for.
//
// A pair is taken only when it is **unique on both sides**: one vanished row and one
// new file sharing a key. Anything else — two candidates for one row, or one file that
// could be either of two rows — is left to the manager to resolve, because carrying an
// identity onto the wrong file is a mistag with the manager's name on it, and
// resolving costs one lookup.
//
// Two keys, tried in turn: size and modification second (moveKey) for a rename or a
// move on one filesystem, then size, name and album folder (copyKey) for a move that
// reset the time. The second only sees what the first left.
//
// The whole row moves, pin included: a manual attachment is a fact about the file, and
// the file did not change. Only its path, and when it was last seen, are new.
func carryMovedRows(db *gorm.DB, gone []models.LibraryItem, unknown []string) (int, []string, []models.LibraryItem) {
	if len(gone) == 0 || len(unknown) == 0 {
		return 0, unknown, gone
	}

	sizes := map[string]os.FileInfo{}
	for _, path := range unknown {
		if fi, err := os.Stat(path); err == nil {
			sizes[path] = fi
		}
	}

	moved := 0
	taken := map[string]bool{}      // new paths now holding a carried row
	settled := map[string]bool{}    // new paths no longer unindexed
	carried := map[uuid.UUID]bool{} // vanished rows that found their file
	now := time.Now()

	carry := func(label string, rowKey func(models.LibraryItem) (any, bool), fileKey func(string, os.FileInfo) any) {
		goneBy := map[any][]models.LibraryItem{}
		for _, row := range gone {
			if carried[row.ID] {
				continue
			}
			if k, ok := rowKey(row); ok {
				goneBy[k] = append(goneBy[k], row)
			}
		}
		newBy := map[any][]string{}
		for _, path := range unknown {
			if fi, ok := sizes[path]; ok && !settled[path] {
				k := fileKey(path, fi)
				newBy[k] = append(newBy[k], path)
			}
		}
		for k, rows := range goneBy {
			paths := newBy[k]
			if len(rows) != 1 || len(paths) != 1 {
				if len(paths) > 0 {
					logger.Log.Infof("not carrying identity by %s: %d vanished row(s) and %d new file(s) share one, resolving instead", label, len(rows), len(paths))
				}
				continue
			}
			row, path := rows[0], paths[0]
			if err := db.Model(&models.LibraryItem{}).Where("id = ?", row.ID).
				Updates(map[string]any{"path": path, "last_scanned_at": now}).Error; err != nil {
				logger.Log.Warnf("failed to carry the index row for %q over to %q: %s", row.Path, path, err.Error())
				continue
			}
			logger.Log.Infof("file moved on disk, identity carried over by %s: %q -> %q", label, row.Path, path)
			moved++
			carried[row.ID] = true
			settled[path] = true
			// A row that moved without an identity still needs one, so its file stays on
			// the list to resolve — now against the row at its new path.
			if row.Pinned || (row.MBReleaseID != "" && row.Status != models.LibraryItemStatusUnmatched) {
				taken[path] = true
			}
		}
	}

	carry("size and time",
		func(row models.LibraryItem) (any, bool) {
			if row.ModTime == nil {
				return nil, false
			}
			return moveKey{row.Size, row.ModTime.Unix()}, true
		},
		func(_ string, fi os.FileInfo) any { return moveKey{fi.Size(), fi.ModTime().Unix()} })
	carry("size, name and folder",
		func(row models.LibraryItem) (any, bool) {
			if row.Size == 0 {
				return nil, false
			}
			return copyKeyOf(row.Path, row.Size), true
		},
		func(path string, fi os.FileInfo) any { return copyKeyOf(path, fi.Size()) })

	rest := make([]string, 0, len(unknown)-len(taken))
	for _, path := range unknown {
		if !taken[path] {
			rest = append(rest, path)
		}
	}
	var stranded []models.LibraryItem
	for _, row := range gone {
		if !carried[row.ID] {
			stranded = append(stranded, row)
		}
	}
	return moved, rest, stranded
}

// narrowed reports whether a pass walked less than its whole library.
func narrowed(roots []string, libraryPath string) bool {
	return !(len(roots) == 1 && filepath.Clean(roots[0]) == filepath.Clean(libraryPath))
}

// carryFromElsewhere looks for vanished rows a scoped walk could not pair in the rest
// of their library, and returns how many it carried.
//
// An artist-scoped walk covers the artist's folders as indexed, so an album the
// manager moved under a *different* artist folder vanished from the walk's view
// without appearing in it — and the Scan that follows would prune its rows, leaving
// the files to be resolved again from scratch by whatever next walked that folder.
// This is the one case a scoped pass has to look outside its scope to get right.
//
// It runs only when something vanished unpaired, which is rare, and it only lists and
// stats: no file is read, and nothing outside the scope is resolved through the
// manager. A file found here is either carried or left exactly as it was, for the pass
// that owns its folder.
func carryFromElsewhere(db *gorm.DB, library models.Library, roots []string, rows []models.LibraryItem, stranded []models.LibraryItem) (int, []models.LibraryItem) {
	everywhere, err := walkAudioFiles([]string{library.Path})
	if err != nil {
		logger.Log.Warnf("failed to look for moved files in the rest of %q: %s", library.Name, err.Error())
		return 0, stranded
	}
	indexed := make(map[string]bool, len(rows))
	for _, row := range rows {
		indexed[row.Path] = true
	}
	var elsewhere []string
	for _, path := range everywhere {
		if !indexed[path] && !underAny(path, roots) {
			elsewhere = append(elsewhere, path)
		}
	}
	moved, _, left := carryMovedRows(db, stranded, elsewhere)
	if moved > 0 {
		logger.Log.Infof("carried %d row(s) to files moved outside the scanned folders in %q", moved, library.Name)
	}
	return moved, left
}

// CarryMovedFiles is the carry step on its own, for a Process to run before it walks.
//
// A Process resolves what its walk finds and prunes what it does not, so a moved file
// used to be a deleted row plus a new one resolved from scratch — the pin and the
// identity lost, and under an artist scope the new folder never visited at all, so the
// album simply left the collection. Run first, this moves the rows onto the files'
// new paths before the walk reaches them (where they then read as unchanged), and
// looks outside a narrowed scope for what vanished from it.
//
// It returns how many rows it carried and the rows that vanished without a partner —
// the only ones the prune after the walk needs to check again. Nothing is resolved
// here: an unknown file is the walk's.
func CarryMovedFiles(db *gorm.DB, library models.Library, roots []string) (int, []models.LibraryItem, error) {
	if db == nil {
		return 0, nil, nil
	}
	if _, err := os.Stat(library.Path); err != nil {
		return 0, nil, fmt.Errorf("library root %q is unavailable: %w", library.Path, err)
	}
	if len(roots) == 0 {
		roots = []string{library.Path}
	}
	var rows []models.LibraryItem
	if err := db.Where("library_id = ?", library.ID).Find(&rows).Error; err != nil {
		return 0, nil, err
	}
	indexed := make(map[string]bool, len(rows))
	var gone []models.LibraryItem
	for _, row := range rows {
		indexed[row.Path] = true
		if !underAny(row.Path, roots) {
			continue
		}
		if _, err := os.Stat(row.Path); err != nil && errors.Is(err, fs.ErrNotExist) {
			gone = append(gone, row)
		}
	}
	if len(gone) == 0 {
		return 0, nil, nil
	}

	onDisk, err := walkAudioFiles(roots)
	if err != nil {
		return 0, gone, err
	}
	var unknown []string
	for _, path := range onDisk {
		if !indexed[path] {
			unknown = append(unknown, path)
		}
	}
	moved, _, stranded := carryMovedRows(db, gone, unknown)
	if len(stranded) > 0 && narrowed(roots, library.Path) {
		var more int
		more, stranded = carryFromElsewhere(db, library, roots, rows, stranded)
		moved += more
	}
	return moved, stranded, nil
}

// resolveIdentities asks the manager about each path and records the answer without
// touching the file. The processed version is recorded blank on purpose: it is what
// tells the next Process this file has never been tagged against its identity.
func resolveIdentities(ctx context.Context, db *gorm.DB, library models.Library, manager Manager, paths []string, detail *DetailCollector, workers int, stats *DiscoverStats) {
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
		if ctx.Err() != nil {
			break // the lookups already started finish; no new one begins
		}
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

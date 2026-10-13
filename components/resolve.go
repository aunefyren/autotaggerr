package components

import (
	"fmt"
	"strings"

	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
)

// BuildForFile assembles the pipeline (owning library + manager + tagger) for a
// single file, resolving components from the database. When no configured library
// contains the file there is no manager — the file is identified from its own tags,
// as in a library without one — and the first tagger profile (or plain defaults),
// so the single-file path always works.
func BuildForFile(db *gorm.DB, filePath, fileRoot string) (models.Library, Manager, *Tagger, error) {
	library, found := findLibraryForFile(db, filePath, fileRoot)

	managerRow, err := resolveManagerRow(db, library, found)
	if err != nil {
		return library, nil, nil, err
	}
	manager, err := NewManager(managerRow)
	if err != nil {
		return library, nil, nil, err
	}

	tagger := NewTagger(resolveTaggerProfile(db, library, found)).ForLibrary(library)
	return library, manager, tagger, nil
}

// findLibraryForFile returns the configured library that contains filePath (the
// longest matching path wins), tolerant of Windows/Unix separators so a library
// seeded from a Windows config still matches a Unix runtime path by suffix.
func findLibraryForFile(db *gorm.DB, filePath, fileRoot string) (models.Library, bool) {
	if db == nil {
		return models.Library{}, false
	}

	var libraries []models.Library
	if err := db.Find(&libraries).Error; err != nil || len(libraries) == 0 {
		return models.Library{}, false
	}

	target := normalizePath(filePath)
	root := normalizePath(fileRoot)

	var best models.Library
	bestLen := -1
	for _, lib := range libraries {
		p := normalizePath(lib.Path)
		if p == root || target == p || strings.HasPrefix(target, p+"/") {
			if len(p) > bestLen {
				best, bestLen = lib, len(p)
			}
		}
	}
	if bestLen >= 0 {
		return best, true
	}
	return models.Library{}, false
}

// resolveManagerRow picks the manager that governs a library.
//
// A library with an explicit manager must get *that* manager: if it is disabled or
// missing, the scan fails for that library rather than quietly falling back. The
// manager is the correlation authority, so silently swapping it changes which MB
// IDs get written into the user's files — the one outcome worth erroring over.
// Disabling a manager is a deliberate act, so a loud, recoverable failure is the
// honest response; re-enable it or point the library elsewhere.
//
// A library with no manager has no manager. It is not handed whichever manager
// happens to be enabled — that used to happen, picked by row order, while the
// collection treated the same library as unmanaged, so the two disagreed about who
// governed it. Its files are identified from the MusicBrainz IDs already in their
// tags, which is all there is without an authority to ask, and the run says so
// (NoManagerWarning). The row returned has no ID; HasManager tells it apart.
func resolveManagerRow(db *gorm.DB, library models.Library, found bool) (models.Manager, error) {
	if db != nil && found && library.ManagerID != nil {
		var m models.Manager
		if err := db.First(&m, "id = ?", *library.ManagerID).Error; err != nil {
			return models.Manager{}, fmt.Errorf("library %q is assigned a manager that no longer exists (%s) — reassign or remove it", library.Name, *library.ManagerID)
		}
		if !m.Enabled {
			return models.Manager{}, fmt.Errorf("library %q is assigned the disabled manager %q — re-enable it, or assign the library a different manager", library.Name, m.Name)
		}
		return m, nil
	}
	// The type is the native one because the resolution is the same — embedded tags
	// only — and because it is what files correlated this way have always recorded,
	// so assigning a native manager later does not re-correlate a whole library to
	// arrive at the identical answer.
	return models.Manager{Type: models.ManagerTypeAutotaggerr, Enabled: true}, nil
}

// HasManager reports whether a library is assigned a manager at all.
func HasManager(library models.Library) bool { return library.ManagerID != nil }

// NoManagerWarning is what a run says about a library with no manager. Stated on
// every run rather than once: it is a configuration choice with a cost, and a fresh
// install that never created a manager otherwise processes its whole library with no
// sign that most files had nothing to be identified by.
func NoManagerWarning(library models.Library) string {
	return fmt.Sprintf("Library %q has no manager. Files were identified only from MusicBrainz IDs already in their tags; files without them are left unmatched. Lidarr does not write those IDs into MP3s. Assign a manager under Libraries → edit.", library.Name)
}

func resolveTaggerProfile(db *gorm.DB, library models.Library, found bool) models.TaggerProfile {
	if db != nil {
		if found && library.TaggerProfileID != nil {
			var p models.TaggerProfile
			if err := db.First(&p, "id = ?", *library.TaggerProfileID).Error; err == nil {
				return p
			}
		}
		var first models.TaggerProfile
		if err := db.Order("id").First(&first).Error; err == nil {
			return first
		}
	}
	// No profile configured — write tags with plain defaults.
	return models.TaggerProfile{Name: "Default", WriteTags: true}
}

// normalizePath lower-noise path comparison: unify separators and drop a trailing
// slash. Case is preserved (Unix paths are case-sensitive).
func normalizePath(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	return strings.TrimRight(p, "/")
}

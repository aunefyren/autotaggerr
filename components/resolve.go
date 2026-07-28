package components

import (
	"strings"

	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
)

// BuildForFile assembles the pipeline (owning library + manager + tagger) for a
// single file, resolving components from the database. When no configured library
// contains the file it falls back to the first configured manager/tagger (or a
// native Autotaggerr manager + default tagger when the DB is empty), so the
// single-file path always works.
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

	tagger := NewTagger(resolveTaggerProfile(db, library, found))
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

func resolveManagerRow(db *gorm.DB, library models.Library, found bool) (models.Manager, error) {
	if db != nil {
		if found && library.ManagerID != nil {
			var m models.Manager
			if err := db.First(&m, "id = ?", *library.ManagerID).Error; err == nil {
				return m, nil
			}
		}
		var first models.Manager
		if err := db.Order("id").First(&first).Error; err == nil {
			return first, nil
		}
	}
	// No managers configured — default to the native Autotaggerr manager.
	return models.Manager{Type: models.ManagerTypeAutotaggerr}, nil
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

package models_test

import (
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/database"
	"github.com/aunefyren/autotaggerr/models"
)

// TestTaggableItemsScope: only files with a release and a usable status are
// taggable, and the scope still works across a join on libraries.
func TestTaggableItemsScope(t *testing.T) {
	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	lib := models.Library{Name: "L", Path: t.TempDir(), Enabled: true}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatal(err)
	}
	for _, item := range []models.LibraryItem{
		{Path: "ok.flac", MBReleaseID: "rel", Status: models.LibraryItemStatusOK, LibraryID: lib.ID},
		{Path: "err.flac", MBReleaseID: "rel", Status: models.LibraryItemStatusError, LibraryID: lib.ID},
		{Path: "unmatched.flac", MBReleaseID: "rel", Status: models.LibraryItemStatusUnmatched, LibraryID: lib.ID},
		{Path: "unknown.flac", Status: models.LibraryItemStatusOK, LibraryID: lib.ID},
	} {
		if err := db.Create(&item).Error; err != nil {
			t.Fatal(err)
		}
	}

	var n int64
	if err := models.TaggableItems(db.Model(&models.LibraryItem{})).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("taggable = %d, want 2 (ok and error, both with a release)", n)
	}

	var joined int64
	if err := models.TaggableItems(db.Model(&models.LibraryItem{}).
		Joins("JOIN libraries ON libraries.id = library_items.library_id")).Count(&joined).Error; err != nil {
		t.Fatalf("scope across a join: %v", err)
	}
	if joined != 2 {
		t.Errorf("taggable across a join = %d, want 2", joined)
	}
}

package collection

import (
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
)

// A disabled library is switched off, not forgotten: its files stay indexed, but the
// collection does not count them (#40).

// TestRebuildLeavesADisabledLibraryOut: an album held only in a disabled library is
// not owned, a copy in a disabled library is not listed, and the album held in both
// counts only the enabled library's tracks. Enabling it again brings all of it back.
func TestRebuildLeavesADisabledLibraryOut(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)
	seedCachedRelease(t, db, pruneTestRelease("rel-shared", "rg-shared", "art-1", "The Band"))
	seedCachedRelease(t, db, pruneTestRelease("rel-only", "rg-only", "art-2", "Other Band"))

	on := diskLibrary(t, db, "On", nil)
	off := diskLibrary(t, db, "Off", nil)
	indexFile(t, db, on, "rel-shared", "t1", true)
	indexFile(t, db, off, "rel-shared", "t2", true)
	indexFile(t, db, off, "rel-only", "t1", true)
	setEnabled := func(lib models.Library, enabled bool) {
		t.Helper()
		if err := db.Model(&models.Library{}).Where("id = ?", lib.ID).Update("enabled", enabled).Error; err != nil {
			t.Fatalf("enabled = %v: %v", enabled, err)
		}
	}
	setEnabled(off, false)

	if _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if rg := releaseGroup(t, db, "rg-shared"); !rg.Owned || rg.OwnedTracks != 1 {
		t.Errorf("rg-shared = owned %v, %d tracks; want owned with 1 (the enabled library's t1)", rg.Owned, rg.OwnedTracks)
	}
	var only models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", "rg-only").First(&only).Error; err == nil && only.Owned {
		t.Error("an album held only in a disabled library should not be owned")
	}
	copies, err := ReleaseGroupCopies(db, []string{"rg-shared", "rg-only"})
	if err != nil {
		t.Fatal(err)
	}
	if got := copies["rg-shared"]; len(got) != 1 || got[0].LibraryID != on.ID {
		t.Errorf("rg-shared copies = %+v, want the enabled library's only", got)
	}
	if got := copies["rg-only"]; len(got) != 0 {
		t.Errorf("rg-only copies = %+v, want none", got)
	}
	if items, err := ArtistItems(db, "art-1"); err != nil || len(items) != 1 {
		t.Errorf("art-1 items = %d (%v), want the enabled library's one file", len(items), err)
	}

	setEnabled(off, true)
	if _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if rg := releaseGroup(t, db, "rg-shared"); rg.OwnedTracks != 2 {
		t.Errorf("rg-shared after enabling = %d tracks, want 2", rg.OwnedTracks)
	}
	if rg := releaseGroup(t, db, "rg-only"); !rg.Owned {
		t.Error("rg-only should be owned again once its library is enabled")
	}
}

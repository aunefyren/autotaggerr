package collection

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// pruneTestRelease builds a release for seedCachedRelease (defined in
// collection_test.go): one artist, one release-group, three tracks — enough for
// Rebuild to derive an artist and a release-group from library_items rows that point
// at it.
func pruneTestRelease(relID, rgID, artistID, artistName string) models.MusicBrainzReleaseResponse {
	return models.MusicBrainzReleaseResponse{
		ID:           relID,
		Title:        "Album",
		ArtistCredit: []models.ArtistCredit{{Name: artistName, Artist: models.Artist{ID: artistID, Name: artistName}}},
		ReleaseGroup: models.ReleaseGroup{ID: rgID, Title: "Album", PrimaryType: "Album"},
		Media:        []models.MusicBrainzMedia{{Tracks: []models.Track{{ID: "t1"}, {ID: "t2"}, {ID: "t3"}}}},
	}
}

func libraryItemCount(t *testing.T, db *gorm.DB, path string) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.LibraryItem{}).Where("path = ?", path).Count(&n).Error; err != nil {
		t.Fatalf("count %s: %v", path, err)
	}
	return n
}

// TestScanPrunesFilesGoneFromDisk is the fix for "Scan says the same stale thing
// Rebuild always did": a Scan now proves each of its rows still exists before
// aggregating them, and deletes the ones that do not — the same disk truth Process
// establishes, without a directory walk.
func TestScanPrunesFilesGoneFromDisk(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)

	seedCachedRelease(t, db, pruneTestRelease("rel-1", "rg-1", "art-1", "The Band"))

	root := t.TempDir()
	lib := models.Library{Name: "L", Path: root}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}

	present := filepath.Join(root, "present.flac")
	if err := os.WriteFile(present, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(root, "gone.flac")

	for _, p := range []string{present, gone} {
		if err := db.Create(&models.LibraryItem{LibraryID: lib.ID, Path: p, Status: "ok", MBReleaseID: "rel-1"}).Error; err != nil {
			t.Fatalf("item %s: %v", p, err)
		}
	}

	stats, err := Rebuild(db)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if stats.FilesRemoved != 1 {
		t.Fatalf("files removed = %d, want 1", stats.FilesRemoved)
	}

	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", "rg-1").First(&rg).Error; err != nil {
		t.Fatalf("release-group: %v", err)
	}
	if rg.OwnedTracks != 1 {
		t.Errorf("owned tracks = %d, want 1 — the gone file must not count toward it", rg.OwnedTracks)
	}

	if n := libraryItemCount(t, db, gone); n != 0 {
		t.Error("the index row for the missing file should have been deleted")
	}
	if n := libraryItemCount(t, db, present); n != 1 {
		t.Error("the index row for the present file must survive")
	}
}

// TestScanLeavesFilesAloneWhenLibraryUnavailable is the guard: a library that fails
// to stat — unmounted, a dead network share — must never be read as "every file is
// gone". Its rows are left exactly as they were, the same standard
// process.pruneMissingItems holds for a vanished library root.
func TestScanLeavesFilesAloneWhenLibraryUnavailable(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)

	seedCachedRelease(t, db, pruneTestRelease("rel-1", "rg-1", "art-1", "The Band"))

	root := filepath.Join(t.TempDir(), "not-mounted")
	lib := models.Library{Name: "L", Path: root}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	path := filepath.Join(root, "01.flac")
	if err := db.Create(&models.LibraryItem{LibraryID: lib.ID, Path: path, Status: "ok", MBReleaseID: "rel-1"}).Error; err != nil {
		t.Fatalf("item: %v", err)
	}

	stats, err := Rebuild(db)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if stats.FilesRemoved != 0 {
		t.Fatalf("files removed = %d, want 0 — the library could not be checked", stats.FilesRemoved)
	}
	if n := libraryItemCount(t, db, path); n != 1 {
		t.Error("a row under an unavailable library must not be pruned")
	}

	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", "rg-1").First(&rg).Error; err != nil {
		t.Fatalf("release-group: %v", err)
	}
	if !rg.Owned {
		t.Error("a file that could not be checked must still be presumed present")
	}
}

// TestScanPruneIsScopedToTheArtist: the artist-scoped Scan button must conclude
// nothing about a different artist's files, the same rule the scoped rebuild already
// holds for everything else it writes.
func TestScanPruneIsScopedToTheArtist(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)

	seedCachedRelease(t, db, pruneTestRelease("rel-a", "rg-a", "art-a", "Artist A"))
	seedCachedRelease(t, db, pruneTestRelease("rel-b", "rg-b", "art-b", "Artist B"))

	root := t.TempDir()
	lib := models.Library{Name: "L", Path: root}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}

	goneA := filepath.Join(root, "a-gone.flac")
	goneB := filepath.Join(root, "b-gone.flac")
	if err := db.Create(&models.LibraryItem{LibraryID: lib.ID, Path: goneA, Status: "ok", MBReleaseID: "rel-a"}).Error; err != nil {
		t.Fatalf("item a: %v", err)
	}
	if err := db.Create(&models.LibraryItem{LibraryID: lib.ID, Path: goneB, Status: "ok", MBReleaseID: "rel-b"}).Error; err != nil {
		t.Fatalf("item b: %v", err)
	}

	stats, err := RebuildArtist(db, "art-a")
	if err != nil {
		t.Fatalf("RebuildArtist: %v", err)
	}
	if stats.FilesRemoved != 1 {
		t.Fatalf("files removed = %d, want 1", stats.FilesRemoved)
	}
	if n := libraryItemCount(t, db, goneA); n != 0 {
		t.Error("the scoped artist's missing file should have been pruned")
	}
	if n := libraryItemCount(t, db, goneB); n != 1 {
		t.Error("a scoped scan must not prune another artist's missing file")
	}
}

package collection

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// The Scan verb at library scope (#15): it re-derives every artist with a file in the
// library, reading their files in every library, but proves only its own library's
// files still exist.

// diskLibrary creates a library whose root exists, so the existence check runs.
func diskLibrary(t *testing.T, db *gorm.DB, name string, managerID *uuid.UUID) models.Library {
	t.Helper()
	lib := models.Library{Name: name, Path: t.TempDir(), ManagerID: managerID}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	return lib
}

// indexFile records one track of a release in a library, writing the file to disk
// when present is set.
func indexFile(t *testing.T, db *gorm.DB, lib models.Library, relID, track string, present bool) string {
	t.Helper()
	path := filepath.Join(lib.Path, relID+"-"+track+".flac")
	if present {
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	item := models.LibraryItem{LibraryID: lib.ID, Path: path, Status: models.LibraryItemStatusOK, MBReleaseID: relID, MBReleaseTrackID: track}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("item %s: %v", path, err)
	}
	return path
}

func releaseGroup(t *testing.T, db *gorm.DB, mbid string) models.CollectionReleaseGroup {
	t.Helper()
	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", mbid).First(&rg).Error; err != nil {
		t.Fatalf("release-group %s: %v", mbid, err)
	}
	return rg
}

// TestLibraryScanProvesOnlyItsOwnFiles: a gone file in the scanned library is pruned;
// a gone file of the same artist in another library is left for that library's Scan.
func TestLibraryScanProvesOnlyItsOwnFiles(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)
	seedCachedRelease(t, db, pruneTestRelease("rel-1", "rg-1", "art-1", "The Band"))

	libA := diskLibrary(t, db, "A", nil)
	libB := diskLibrary(t, db, "B", nil)
	indexFile(t, db, libA, "rel-1", "t1", true)
	goneA := indexFile(t, db, libA, "rel-1", "t2", false)
	goneB := indexFile(t, db, libB, "rel-1", "t3", false)

	stats, err := RebuildLibrary(db, libA.ID)
	if err != nil {
		t.Fatalf("RebuildLibrary: %v", err)
	}
	if stats.FilesRemoved != 1 {
		t.Fatalf("files removed = %d, want 1", stats.FilesRemoved)
	}
	if libraryItemCount(t, db, goneA) != 0 {
		t.Error("the scanned library's missing file should have been pruned")
	}
	if libraryItemCount(t, db, goneB) != 1 {
		t.Error("a library Scan must not prune another library's file")
	}
	// The unproven copy in B still counts toward the album: the pass reads it, it
	// just concludes nothing about it.
	if rg := releaseGroup(t, db, "rg-1"); !rg.Owned || rg.OwnedTracks != 2 {
		t.Errorf("rg-1 = owned %v, %d tracks; want owned with 2 (A's t1 and B's t3)", rg.Owned, rg.OwnedTracks)
	}
}

// TestLibraryScanCountsSharedAlbumsFromEveryLibrary: an album held across two
// libraries is one row, and a pass over one library must not shrink it to that
// library's share.
func TestLibraryScanCountsSharedAlbumsFromEveryLibrary(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)
	seedCachedRelease(t, db, pruneTestRelease("rel-1", "rg-1", "art-1", "The Band"))
	seedCachedRelease(t, db, pruneTestRelease("rel-2", "rg-2", "art-2", "Other Band"))

	libA := diskLibrary(t, db, "A", nil)
	libB := diskLibrary(t, db, "B", nil)
	indexFile(t, db, libA, "rel-1", "t1", true)
	indexFile(t, db, libB, "rel-1", "t2", true)
	indexFile(t, db, libB, "rel-1", "t3", true)
	indexFile(t, db, libB, "rel-2", "t1", true)
	if _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	before := releaseGroup(t, db, "rg-2")

	stats, err := RebuildLibrary(db, libA.ID)
	if err != nil {
		t.Fatalf("RebuildLibrary: %v", err)
	}
	if rg := releaseGroup(t, db, "rg-1"); !rg.Owned || rg.OwnedTracks != 3 {
		t.Errorf("shared rg-1 = owned %v, %d tracks; want 3 across both libraries", rg.Owned, rg.OwnedTracks)
	}
	if after := releaseGroup(t, db, "rg-2"); !after.Owned || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Error("an artist with no file in the library must not be rewritten by its Scan")
	}
	if stats.Artists != 1 || stats.Owned != 1 {
		t.Errorf("stats = %d artists, %d albums; want the pass to describe its own scope (1, 1)", stats.Artists, stats.Owned)
	}
}

// TestLibraryScanKeepsProvenanceFromOtherLibraries: "managed by" is read off every
// library an artist has files in, so scanning the native library must not demote a
// mixed artist to native-only.
func TestLibraryScanKeepsProvenanceFromOtherLibraries(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)
	seedTrackedRelease(t, db, "rel-1", "rg-1", "art-1", 3)
	seedTrackedRelease(t, db, "rel-2", "rg-2", "art-1", 3)
	if err := modules.MusicbrainzLoadCache(); err != nil {
		t.Fatalf("load cache: %v", err)
	}
	lidarrLib, nativeLib := twoLibraries(t, db)
	ownTracks(t, db, lidarrLib, "rel-1", 3)
	ownTracks(t, db, nativeLib, "rel-2", 3)

	if _, err := RebuildLibrary(db, nativeLib.ID); err != nil {
		t.Fatalf("RebuildLibrary: %v", err)
	}
	var artist models.CollectionArtist
	if err := db.Where("mb_id = ?", "art-1").First(&artist).Error; err != nil {
		t.Fatalf("artist: %v", err)
	}
	if artist.ManagedBy != models.ManagedByMixed {
		t.Errorf("managed by = %q, want %q", artist.ManagedBy, models.ManagedByMixed)
	}
	if rg := releaseGroup(t, db, "rg-1"); !rg.Owned {
		t.Error("the artist's album in the other library must stay owned")
	}
}

// TestLibraryScanDropsAnAlbumWhoseFilesWentAway: the library's artists are read
// before anything is pruned, so an album whose every file in the library is gone is
// still in scope — and leaves the disk view.
func TestLibraryScanDropsAnAlbumWhoseFilesWentAway(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)
	seedCachedRelease(t, db, pruneTestRelease("rel-1", "rg-1", "art-1", "The Band"))

	lib := diskLibrary(t, db, "A", nil)
	path := indexFile(t, db, lib, "rel-1", "t1", true)
	if _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	stats, err := RebuildLibrary(db, lib.ID)
	if err != nil {
		t.Fatalf("RebuildLibrary: %v", err)
	}
	if stats.FilesRemoved != 1 || stats.AlbumsRemoved != 1 {
		t.Errorf("stats = %d files removed, %d albums gone; want 1 and 1", stats.FilesRemoved, stats.AlbumsRemoved)
	}
	if rg := releaseGroup(t, db, "rg-1"); rg.Owned {
		t.Error("an album with no file left should no longer be owned")
	}
}

// TestLibraryScanLeavesAnUnavailableLibraryAlone: the same guard as the collection
// Scan — an unmounted library is unavailable, not empty.
func TestLibraryScanLeavesAnUnavailableLibraryAlone(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)
	seedCachedRelease(t, db, pruneTestRelease("rel-1", "rg-1", "art-1", "The Band"))

	lib := models.Library{Name: "A", Path: filepath.Join(t.TempDir(), "not-mounted")}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	path := indexFile(t, db, lib, "rel-1", "t1", false)

	stats, err := RebuildLibrary(db, lib.ID)
	if err != nil {
		t.Fatalf("RebuildLibrary: %v", err)
	}
	if stats.FilesRemoved != 0 || libraryItemCount(t, db, path) != 1 {
		t.Error("a row under an unavailable library must not be pruned")
	}
	if rg := releaseGroup(t, db, "rg-1"); !rg.Owned {
		t.Error("a file that could not be checked must still be presumed present")
	}
}

// TestLibraryScanExplainsAnEmptyLibrary: a library with nothing matched says so in
// its own terms, not the artist's.
func TestLibraryScanExplainsAnEmptyLibrary(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)
	seedCachedRelease(t, db, pruneTestRelease("rel-1", "rg-1", "art-1", "The Band"))

	full := diskLibrary(t, db, "A", nil)
	empty := diskLibrary(t, db, "B", nil)
	indexFile(t, db, full, "rel-1", "t1", true)
	if _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	stats, err := RebuildLibrary(db, empty.ID)
	if err != nil {
		t.Fatalf("RebuildLibrary: %v", err)
	}
	if stats.EmptyReason != ScanEmptyLibraryNoFiles {
		t.Errorf("empty reason = %q, want %q", stats.EmptyReason, ScanEmptyLibraryNoFiles)
	}
	if rg := releaseGroup(t, db, "rg-1"); !rg.Owned {
		t.Error("scanning an empty library must not touch another library's album")
	}
}

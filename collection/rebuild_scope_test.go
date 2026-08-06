package collection

import (
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// seedOwnedAlbum wires up one artist's album end to end: a cached release credited to
// them, and an indexed file pointing at it.
func seedOwnedAlbum(t *testing.T, db *gorm.DB, lib models.Library, artistID, artistName, rgID, relID, path string) {
	t.Helper()
	seedCachedRelease(t, db, models.MusicBrainzReleaseResponse{
		ID:           relID,
		Title:        rgID,
		ArtistCredit: []models.ArtistCredit{{Name: artistName, Artist: models.Artist{ID: artistID, Name: artistName}}},
		ReleaseGroup: models.ReleaseGroup{ID: rgID, Title: rgID, PrimaryType: "Album"},
		Media:        []models.MusicBrainzMedia{{Tracks: []models.Track{{ID: relID + "-t1"}}}},
	})
	if err := db.Create(&models.LibraryItem{
		LibraryID: lib.ID, Path: path, Status: "ok", MBReleaseID: relID,
	}).Error; err != nil {
		t.Fatalf("item %q: %v", path, err)
	}
}

// twoArtistCollection is the fixture the scope tests share: two artists, one album
// each, both owned.
func twoArtistCollection(t *testing.T) *gorm.DB {
	t.Helper()
	db := testDB(t)
	modules.SetDB(db)
	t.Cleanup(func() { modules.SetDB(nil) })

	lib := models.Library{Name: "L", Path: "/m"}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	seedOwnedAlbum(t, db, lib, "art-parliament", "Parliament", "rg-mothership", "rel-mothership", "/m/Parliament/Mothership/01.flac")
	seedOwnedAlbum(t, db, lib, "art-jayz", "Jay-Z", "rg-blueprint", "rel-blueprint", "/m/Jay-Z/Blueprint/01.flac")

	if _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	return db
}

func ownedFlag(t *testing.T, db *gorm.DB, rgMBID string) models.CollectionReleaseGroup {
	t.Helper()
	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", rgMBID).First(&rg).Error; err != nil {
		t.Fatalf("release-group %s: %v", rgMBID, err)
	}
	return rg
}

// TestRebuildArtistLeavesTheRestAlone is the reason the scope exists. A rebuild
// clears the disk view before re-establishing it; narrowed to one artist it must
// clear only that artist's rows, or every other album in the collection is reported
// as owning nothing.
func TestRebuildArtistLeavesTheRestAlone(t *testing.T) {
	db := twoArtistCollection(t)

	// The other artist's files disappear from the index entirely. A pass that read
	// them would notice; a pass scoped to Parliament must not touch their rows.
	if err := db.Where("mb_release_id = ?", "rel-blueprint").Delete(&models.LibraryItem{}).Error; err != nil {
		t.Fatalf("delete items: %v", err)
	}

	stats, err := RebuildArtist(db, "art-parliament")
	if err != nil {
		t.Fatalf("RebuildArtist: %v", err)
	}
	if stats.Artists != 1 || stats.Owned != 1 {
		t.Errorf("stats = %d artists / %d albums, want 1/1 — a scoped pass reports its own scope", stats.Artists, stats.Owned)
	}

	if rg := ownedFlag(t, db, "rg-mothership"); !rg.Owned {
		t.Error("the scoped artist's album must still be owned")
	}
	if rg := ownedFlag(t, db, "rg-blueprint"); !rg.Owned {
		t.Error("another artist's album was cleared by a scoped rebuild — the bug the scope prevents")
	}
	// Same for the per-edition rows, whose prune is the other unbounded write.
	var editions int64
	db.Model(&models.CollectionRelease{}).Where("mb_id = ?", "rel-blueprint").Count(&editions)
	if editions != 1 {
		t.Errorf("out-of-scope owned edition rows = %d, want 1 (untouched)", editions)
	}
}

// TestRebuildArtistDropsItsOwnStaleRows: narrowing must not mean never removing
// anything. Inside the scope, an album whose files are gone stops being owned.
func TestRebuildArtistDropsItsOwnStaleRows(t *testing.T) {
	db := twoArtistCollection(t)

	if err := db.Where("mb_release_id = ?", "rel-mothership").Delete(&models.LibraryItem{}).Error; err != nil {
		t.Fatalf("delete items: %v", err)
	}
	if _, err := RebuildArtist(db, "art-parliament"); err != nil {
		t.Fatalf("RebuildArtist: %v", err)
	}

	if rg := ownedFlag(t, db, "rg-mothership"); rg.Owned {
		t.Error("an in-scope album with no files left must stop being owned")
	}
	var editions int64
	db.Model(&models.CollectionRelease{}).Where("mb_id = ?", "rel-mothership").Count(&editions)
	if editions != 0 {
		t.Errorf("in-scope owned edition rows = %d, want 0 — the prune still runs inside the scope", editions)
	}
	if rg := ownedFlag(t, db, "rg-blueprint"); !rg.Owned {
		t.Error("the other artist must be untouched")
	}
}

// TestRebuildArtistDiscoversNewAlbums: the button's whole purpose. Files processed
// since the last rebuild point at a release no collection row names yet, so the scope
// cannot be resolved from existing rows alone — it reads the cached release's credit.
func TestRebuildArtistDiscoversNewAlbums(t *testing.T) {
	db := twoArtistCollection(t)

	var lib models.Library
	if err := db.First(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	seedOwnedAlbum(t, db, lib, "art-parliament", "Parliament", "rg-funkentelechy", "rel-funkentelechy", "/m/Parliament/Funkentelechy/01.flac")

	if _, err := RebuildArtist(db, "art-parliament"); err != nil {
		t.Fatalf("RebuildArtist: %v", err)
	}
	if rg := ownedFlag(t, db, "rg-funkentelechy"); !rg.Owned {
		t.Error("an album added since the last rebuild must be found by the artist's own scan")
	}
}

// TestRebuildScopedIgnoresOtherArtistsFiles: an artist's pass must not gain albums
// from files that are not theirs, even though it reads the whole index to find its
// own.
func TestRebuildScopedIgnoresOtherArtistsFiles(t *testing.T) {
	db := twoArtistCollection(t)

	// Clear both disk views by hand, then rebuild one artist: only theirs comes back.
	if err := db.Model(&models.CollectionReleaseGroup{}).
		Where("owned = ?", true).
		Updates(map[string]any{"owned": false}).Error; err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, err := RebuildArtist(db, "art-parliament"); err != nil {
		t.Fatalf("RebuildArtist: %v", err)
	}
	if rg := ownedFlag(t, db, "rg-mothership"); !rg.Owned {
		t.Error("the scoped artist's album must be re-established")
	}
	if rg := ownedFlag(t, db, "rg-blueprint"); rg.Owned {
		t.Error("a scoped pass re-established another artist's album — it read files that are not in scope")
	}
}

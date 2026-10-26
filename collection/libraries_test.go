package collection

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// The collection across several libraries: a Lidarr library and a native one holding
// the same artist — the production case of an album downloaded into a sandbox library
// while the Lidarr library already had it.

// seedTrackedRelease caches a release whose tracks carry IDs t1..tN, so files can
// resolve to distinct tracks.
func seedTrackedRelease(t *testing.T, db *gorm.DB, relID, rgID, artistID string, tracks int) {
	t.Helper()
	list := make([]models.Track, tracks)
	for i := range list {
		list[i] = models.Track{ID: trackID(i)}
	}
	release := models.MusicBrainzReleaseResponse{
		ID:           relID,
		Title:        "Album " + relID,
		ArtistCredit: []models.ArtistCredit{{Name: "Band", Artist: models.Artist{ID: artistID, Name: "Band"}}},
		ReleaseGroup: models.ReleaseGroup{ID: rgID, Title: "Album " + rgID, PrimaryType: "Album"},
		Media:        []models.MusicBrainzMedia{{Tracks: list}},
	}
	payload, _ := json.Marshal(release)
	if err := db.Create(&models.MusicbrainzReleaseCache{MBID: relID, Payload: string(payload), FetchedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}).Error; err != nil {
		t.Fatalf("seed release %s: %v", relID, err)
	}
}

func trackID(i int) string { return "t" + string(rune('a'+i)) }

// ownTracks files tracks [0, n) of a release in a library.
func ownTracks(t *testing.T, db *gorm.DB, lib models.Library, relID string, n int) {
	t.Helper()
	for i := range n {
		item := models.LibraryItem{
			LibraryID: lib.ID, Path: lib.Path + "/" + relID + "/" + trackID(i) + ".flac",
			Status: models.LibraryItemStatusOK, MBReleaseID: relID, MBReleaseTrackID: trackID(i),
		}
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("own %s: %v", item.Path, err)
		}
	}
}

// twoLibraries is a Lidarr library and a native one. Their paths do not exist, so
// the rebuild's existence check presumes every file present.
func twoLibraries(t *testing.T, db *gorm.DB) (lidarrLib, nativeLib models.Library) {
	t.Helper()
	mgr := models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true}
	if err := db.Create(&mgr).Error; err != nil {
		t.Fatalf("manager: %v", err)
	}
	lidarrLib = models.Library{Name: "Music", Path: "/nonexistent/music", ManagerID: &mgr.ID}
	nativeLib = models.Library{Name: "Music Sandbox", Path: "/nonexistent/sandbox"}
	for _, l := range []*models.Library{&lidarrLib, &nativeLib} {
		if err := db.Create(l).Error; err != nil {
			t.Fatalf("library: %v", err)
		}
	}
	return lidarrLib, nativeLib
}

func TestRebuildCountsCopiesAsOneEdition(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)
	seedTrackedRelease(t, db, "rel-1", "rg-1", "art-1", 12)
	if err := modules.MusicbrainzLoadCache(); err != nil {
		t.Fatalf("load cache: %v", err)
	}
	lidarrLib, nativeLib := twoLibraries(t, db)
	ownTracks(t, db, lidarrLib, "rel-1", 12)
	ownTracks(t, db, nativeLib, "rel-1", 12)

	if _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	var rel models.CollectionRelease
	if err := db.Where("mb_id = ?", "rel-1").First(&rel).Error; err != nil {
		t.Fatalf("edition: %v", err)
	}
	if rel.OwnedTracks != 12 || rel.TotalTracks != 12 || rel.Files != 24 {
		t.Errorf("edition = %d/%d tracks from %d files, want 12/12 from 24", rel.OwnedTracks, rel.TotalTracks, rel.Files)
	}

	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", "rg-1").First(&rg).Error; err != nil {
		t.Fatalf("release-group: %v", err)
	}
	if rg.OwnedTracks != 12 || !rg.Complete() {
		t.Errorf("release-group owns %d/%d, want a complete 12/12", rg.OwnedTracks, rg.TotalTracks)
	}

	var copies []models.CollectionReleaseCopy
	if err := db.Order("files").Find(&copies).Error; err != nil {
		t.Fatalf("copies: %v", err)
	}
	if len(copies) != 2 {
		t.Fatalf("copies = %d, want one per library", len(copies))
	}
	for _, c := range copies {
		if c.ReleaseMBID != "rel-1" || c.ReleaseGroupMBID != "rg-1" || c.OwnedTracks != 12 || c.Files != 12 {
			t.Errorf("copy = %+v, want rel-1 / rg-1, 12 tracks from 12 files", c)
		}
	}

	a := artistRow(t, db, "art-1")
	if a.ManagedBy != models.ManagedByMixed {
		t.Errorf("managed_by = %q, want mixed", a.ManagedBy)
	}
	// Lidarr governs a mixed artist, so the catalog compares with its library alone —
	// which holds the whole album, so Lidarr agreeing reads as agreement.
	if !rg.AuthorityOwned || rg.AuthorityOwnedTracks != 12 {
		t.Errorf("authority = %v/%d, want the Lidarr library's 12", rg.AuthorityOwned, rg.AuthorityOwnedTracks)
	}
	rg.InCatalog, rg.CatalogOwnedTracks, rg.CatalogTotalTracks, rg.CatalogReleaseMBID = true, 12, 12, "rel-1"
	if got := rg.Discrepancy(true); got != models.DiscrepancyNone {
		t.Errorf("Discrepancy = %q, want none: a second copy is not a stale catalog", got)
	}
}

func TestRebuildComparesTheCatalogWithTheManagersLibrariesOnly(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)
	seedTrackedRelease(t, db, "rel-lidarr", "rg-lidarr", "art-1", 10)
	seedTrackedRelease(t, db, "rel-sandbox", "rg-sandbox", "art-1", 8)
	if err := modules.MusicbrainzLoadCache(); err != nil {
		t.Fatalf("load cache: %v", err)
	}
	lidarrLib, nativeLib := twoLibraries(t, db)
	ownTracks(t, db, lidarrLib, "rel-lidarr", 10)
	ownTracks(t, db, nativeLib, "rel-sandbox", 8)

	if _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	var sandbox models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", "rg-sandbox").First(&sandbox).Error; err != nil {
		t.Fatalf("release-group: %v", err)
	}
	if !sandbox.Owned || sandbox.OwnedTracks != 8 {
		t.Errorf("sandbox album owned = %v/%d, want owned 8", sandbox.Owned, sandbox.OwnedTracks)
	}
	if sandbox.AuthorityOwned {
		t.Error("an album only the native library holds counted as on disk for Lidarr")
	}
	// Lidarr has never heard of it, and has no reason to: not unmapped.
	if got := sandbox.Discrepancy(true); got != models.DiscrepancyNone {
		t.Errorf("Discrepancy = %q, want none", got)
	}

	// Detached, the artist is native, so every library is its authority's again.
	if _, err := DetachArtist(db, "art-1"); err != nil {
		t.Fatalf("DetachArtist: %v", err)
	}
	if _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if err := db.Where("mb_id = ?", "rg-sandbox").First(&sandbox).Error; err != nil {
		t.Fatalf("release-group: %v", err)
	}
	if !sandbox.AuthorityOwned || sandbox.AuthorityOwnedTracks != 8 {
		t.Errorf("detached authority = %v/%d, want 8 from every library", sandbox.AuthorityOwned, sandbox.AuthorityOwnedTracks)
	}
}

// TestRebuildScopedKeepsOtherCopies: an artist-scoped pass rewrites only its own
// artist's copies.
func TestRebuildScopedKeepsOtherCopies(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)
	seedTrackedRelease(t, db, "rel-1", "rg-1", "art-1", 3)
	seedTrackedRelease(t, db, "rel-2", "rg-2", "art-2", 3)
	if err := modules.MusicbrainzLoadCache(); err != nil {
		t.Fatalf("load cache: %v", err)
	}
	_, nativeLib := twoLibraries(t, db)
	ownTracks(t, db, nativeLib, "rel-1", 3)
	ownTracks(t, db, nativeLib, "rel-2", 2)
	if _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	if _, err := RebuildScoped(db, RebuildScope{ArtistMBID: "art-1"}); err != nil {
		t.Fatalf("scoped Rebuild: %v", err)
	}
	var n int64
	if err := db.Model(&models.CollectionReleaseCopy{}).Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("copies after a scoped pass = %d, want both artists' kept", n)
	}
}

package collection

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// The auto-desire reconciliation (follow-up C) keeps an "any" want tracking the files:
// it migrates to the owned edition, re-points when the files change edition, and never
// touches a hand-pinned edition or a Lidarr-managed artist.

// seedRelease caches a MusicBrainz release so Rebuild can materialise it as an owned
// edition. tracks is the total track count (ownership is counted per file, not here).
func seedRelease(t *testing.T, db *gorm.DB, relID, rgID, artistID, title string, tracks int) {
	t.Helper()
	release := models.MusicBrainzReleaseResponse{
		ID:           relID,
		Title:        title,
		ArtistCredit: []models.ArtistCredit{{Name: "Band", Artist: models.Artist{ID: artistID, Name: "Band"}}},
		ReleaseGroup: models.ReleaseGroup{ID: rgID, Title: title, PrimaryType: "Album"},
		Media:        []models.MusicBrainzMedia{{Tracks: make([]models.Track, tracks)}},
	}
	payload, _ := json.Marshal(release)
	if err := db.Create(&models.MusicbrainzReleaseCache{MBID: relID, Payload: string(payload), FetchedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}).Error; err != nil {
		t.Fatalf("seed release %s: %v", relID, err)
	}
}

func ownFile(t *testing.T, db *gorm.DB, path, releaseMBID string, lib models.Library) {
	t.Helper()
	if err := db.Create(&models.LibraryItem{LibraryID: lib.ID, Path: path, Status: models.LibraryItemStatusOK, MBReleaseID: releaseMBID}).Error; err != nil {
		t.Fatalf("own file %s: %v", path, err)
	}
}

func desiresFor(t *testing.T, db *gorm.DB, rgMBID string) []models.CollectionDesire {
	t.Helper()
	var out []models.CollectionDesire
	if err := db.Where("release_group_mb_id = ?", rgMBID).Order("release_mb_id").Find(&out).Error; err != nil {
		t.Fatalf("load desires: %v", err)
	}
	return out
}

func TestReconcileAutoDesiresPromotesAnyToOwnedEdition(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)
	seedRelease(t, db, "rel-1", "rg-1", "art-1", "Album", 3)
	if err := modules.MusicbrainzLoadCache(); err != nil {
		t.Fatalf("load cache: %v", err)
	}

	lib := models.Library{Name: "L", Path: "/m"}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	ownFile(t, db, "/m/a.flac", "rel-1", lib)
	if err := db.Create(&models.CollectionDesire{ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1"}).Error; err != nil {
		t.Fatalf("desire: %v", err)
	}

	if _, _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	got := desiresFor(t, db, "rg-1")
	if len(got) != 1 || got[0].ReleaseMBID != "rel-1" || !got[0].Auto {
		t.Fatalf("want single auto want for rel-1, got %+v", got)
	}
}

func TestReconcileAutoDesiresRepointsOnReplacedFiles(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)
	// The files now hold a different edition than the one the auto want points at.
	seedRelease(t, db, "rel-2", "rg-1", "art-1", "Album (Remaster)", 3)
	if err := modules.MusicbrainzLoadCache(); err != nil {
		t.Fatalf("load cache: %v", err)
	}

	lib := models.Library{Name: "L", Path: "/m"}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	ownFile(t, db, "/m/a.flac", "rel-2", lib)
	if err := db.Create(&models.CollectionDesire{ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", ReleaseMBID: "rel-1", Auto: true}).Error; err != nil {
		t.Fatalf("desire: %v", err)
	}

	if _, _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	got := desiresFor(t, db, "rg-1")
	if len(got) != 1 || got[0].ReleaseMBID != "rel-2" || !got[0].Auto {
		t.Fatalf("want auto want re-pointed to rel-2, got %+v", got)
	}
}

func TestReconcileAutoDesiresLeavesManualEdition(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)
	seedRelease(t, db, "rel-1", "rg-1", "art-1", "Album", 3)
	if err := modules.MusicbrainzLoadCache(); err != nil {
		t.Fatalf("load cache: %v", err)
	}

	lib := models.Library{Name: "L", Path: "/m"}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	ownFile(t, db, "/m/a.flac", "rel-1", lib)
	// Hand-pinned edition: auto=false. The reconcile must not adopt or re-point it.
	if err := db.Create(&models.CollectionDesire{ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", ReleaseMBID: "rel-1", Auto: false}).Error; err != nil {
		t.Fatalf("desire: %v", err)
	}

	if _, _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	got := desiresFor(t, db, "rg-1")
	if len(got) != 1 || got[0].Auto {
		t.Fatalf("manual edition must stay manual and single, got %+v", got)
	}
}

func TestReconcileAutoDesiresSkipsLidarrArtist(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)
	seedRelease(t, db, "rel-1", "rg-1", "art-1", "Album", 3)
	if err := modules.MusicbrainzLoadCache(); err != nil {
		t.Fatalf("load cache: %v", err)
	}

	mgr := models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true, LidarrBaseURL: "http://x", LidarrAPIKey: "k"}
	if err := db.Create(&mgr).Error; err != nil {
		t.Fatalf("manager: %v", err)
	}
	lib := models.Library{Name: "L", Path: "/m", ManagerID: &mgr.ID}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	ownFile(t, db, "/m/a.flac", "rel-1", lib)
	if err := db.Create(&models.CollectionDesire{ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1"}).Error; err != nil {
		t.Fatalf("desire: %v", err)
	}

	if _, _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	got := desiresFor(t, db, "rg-1")
	if len(got) != 1 || got[0].ReleaseMBID != "" || got[0].Auto {
		t.Fatalf("lidarr artist: the any want must be left untouched, got %+v", got)
	}
}

func TestReconcileAutoDesiresMultipleOwnedEditions(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)
	seedRelease(t, db, "rel-1", "rg-1", "art-1", "Album", 3)
	seedRelease(t, db, "rel-2", "rg-1", "art-1", "Album (Deluxe)", 5)
	if err := modules.MusicbrainzLoadCache(); err != nil {
		t.Fatalf("load cache: %v", err)
	}

	lib := models.Library{Name: "L", Path: "/m"}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	ownFile(t, db, "/m/a.flac", "rel-1", lib)
	ownFile(t, db, "/m/b.flac", "rel-2", lib)
	if err := db.Create(&models.CollectionDesire{ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1"}).Error; err != nil {
		t.Fatalf("desire: %v", err)
	}

	if _, _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	got := desiresFor(t, db, "rg-1")
	if len(got) != 2 {
		t.Fatalf("want one auto want per owned edition (2), got %+v", got)
	}
	for _, d := range got {
		if !d.Auto || d.ReleaseMBID == "" {
			t.Errorf("want auto edition wants, got %+v", d)
		}
	}
}

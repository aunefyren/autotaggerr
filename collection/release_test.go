package collection

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// seedEdition caches one release of a shared release-group. Two editions of the
// same album is the case pass C exists for — the release-group summary can only
// report one of them.
func seedEdition(t *testing.T, db *gorm.DB, relID, title, date string, formats string, tracks int) {
	t.Helper()
	release := models.MusicBrainzReleaseResponse{
		ID:           relID,
		Title:        title,
		Date:         date,
		Country:      "GB",
		ArtistCredit: []models.ArtistCredit{{Name: "Fleetwood Mac", Artist: models.Artist{ID: "art-1", Name: "Fleetwood Mac"}}},
		ReleaseGroup: models.ReleaseGroup{ID: "rg-1", Title: "Rumours", PrimaryType: "Album"},
	}
	medium := models.MusicBrainzMedia{Position: 1, Format: formats}
	for i := 0; i < tracks; i++ {
		medium.Tracks = append(medium.Tracks, models.Track{ID: relID + "-t"})
	}
	release.Media = []models.MusicBrainzMedia{medium}

	payload, _ := json.Marshal(release)
	if err := db.Create(&models.MusicbrainzReleaseCache{
		MBID: relID, Payload: string(payload),
		FetchedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed cache: %v", err)
	}
}

func ownedFile(t *testing.T, db *gorm.DB, libID, path, relID string) {
	t.Helper()
	var lib models.Library
	if err := db.Where("name = ?", libID).First(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	if err := db.Create(&models.LibraryItem{
		LibraryID: lib.ID, Path: path, Status: "ok", MBReleaseID: relID,
	}).Error; err != nil {
		t.Fatalf("item: %v", err)
	}
}

func editionFixtures(t *testing.T) *gorm.DB {
	t.Helper()
	db := testDB(t)
	modules.SetDB(db)
	t.Cleanup(func() { modules.SetDB(nil) })

	if err := db.Create(&models.Library{Name: "L", Path: "/m"}).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	// The 1977 original (11 tracks) and the 2017 remaster (17).
	seedEdition(t, db, "rel-77", "Rumours", "1977-02-04", "Vinyl", 11)
	seedEdition(t, db, "rel-17", "Rumours", "2017-01-20", "CD", 17)
	if err := modules.MusicbrainzLoadCache(); err != nil {
		t.Fatalf("load cache: %v", err)
	}
	return db
}

// TestRebuildRecordsEveryOwnedEdition: owning 5 tracks of the original and 7 of the
// remaster is two partial editions, not one album that is 12/17 complete. The
// release-group keeps its best-edition summary; the detail lives in its own rows.
func TestRebuildRecordsEveryOwnedEdition(t *testing.T) {
	db := editionFixtures(t)

	for i := 0; i < 5; i++ {
		ownedFile(t, db, "L", "/m/orig/"+string(rune('a'+i))+".flac", "rel-77")
	}
	for i := 0; i < 7; i++ {
		ownedFile(t, db, "L", "/m/remaster/"+string(rune('a'+i))+".flac", "rel-17")
	}

	if _, _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	editions, err := OwnedReleases(db, "rg-1")
	if err != nil {
		t.Fatalf("OwnedReleases: %v", err)
	}
	if len(editions) != 2 {
		t.Fatalf("got %d owned editions, want 2: %+v", len(editions), editions)
	}
	// Best-owned first.
	if editions[0].MBID != "rel-17" || editions[0].OwnedTracks != 7 || editions[0].TotalTracks != 17 {
		t.Errorf("remaster = %+v", editions[0])
	}
	if editions[1].MBID != "rel-77" || editions[1].OwnedTracks != 5 || editions[1].TotalTracks != 11 {
		t.Errorf("original = %+v", editions[1])
	}
	if editions[0].Complete() || editions[1].Complete() {
		t.Error("a partial edition reported complete")
	}
	if editions[1].Format != "Vinyl" || editions[1].Date != "1977-02-04" {
		t.Errorf("edition metadata lost: %+v", editions[1])
	}

	// The release-group summary still reports the single best-owned edition — that
	// is the headline ("how close am I to having this album"), and pass C adds the
	// detail behind it rather than redefining it.
	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", "rg-1").First(&rg).Error; err != nil {
		t.Fatalf("release-group: %v", err)
	}
	if rg.OwnedTracks != 7 || rg.TotalTracks != 17 {
		t.Errorf("release-group summary = %d/%d, want 7/17", rg.OwnedTracks, rg.TotalTracks)
	}

	counts, err := OwnedReleaseCounts(db, "art-1")
	if err != nil {
		t.Fatalf("OwnedReleaseCounts: %v", err)
	}
	if counts["rg-1"] != 2 {
		t.Errorf("owned edition count = %d, want 2", counts["rg-1"])
	}
}

// TestRebuildPrunesEditionsThatLostTheirFiles: re-attaching a file from the
// original to the remaster (which manual attach makes easy) must not leave the
// original still claiming files it no longer has.
func TestRebuildPrunesEditionsThatLostTheirFiles(t *testing.T) {
	db := editionFixtures(t)
	ownedFile(t, db, "L", "/m/a.flac", "rel-77")
	ownedFile(t, db, "L", "/m/b.flac", "rel-17")

	if _, _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if editions, _ := OwnedReleases(db, "rg-1"); len(editions) != 2 {
		t.Fatalf("setup: got %d editions, want 2", len(editions))
	}

	// The file moves to the remaster.
	if err := db.Model(&models.LibraryItem{}).Where("path = ?", "/m/a.flac").
		Update("mb_release_id", "rel-17").Error; err != nil {
		t.Fatalf("re-attach: %v", err)
	}
	if _, _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	editions, _ := OwnedReleases(db, "rg-1")
	if len(editions) != 1 || editions[0].MBID != "rel-17" {
		t.Fatalf("stale edition survived: %+v", editions)
	}
	if editions[0].OwnedTracks != 2 {
		t.Errorf("owned tracks = %d, want 2", editions[0].OwnedTracks)
	}
}

// TestRebuildClearsEditionsWhenNothingIsOwned: owning nothing is a real state, and
// a "NOT IN ()" prune with no values would quietly delete nothing at all.
func TestRebuildClearsEditionsWhenNothingIsOwned(t *testing.T) {
	db := editionFixtures(t)
	ownedFile(t, db, "L", "/m/a.flac", "rel-77")
	if _, _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	if err := db.Where("1 = 1").Delete(&models.LibraryItem{}).Error; err != nil {
		t.Fatalf("clear items: %v", err)
	}
	if _, _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	editions, _ := OwnedReleases(db, "rg-1")
	if len(editions) != 0 {
		t.Errorf("editions survived losing every file: %+v", editions)
	}
}

// TestRebuildDoesNotTouchDesires: desires reference releases by MBID, never by a
// CollectionRelease row, so rebuilding the disk view can never disturb authored
// intent — the separation the whole desire model rests on.
func TestRebuildDoesNotTouchDesires(t *testing.T) {
	db := editionFixtures(t)
	ownedFile(t, db, "L", "/m/a.flac", "rel-77")
	if _, _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	if _, err := SetDesire(db, DesireInput{
		ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", ReleaseMBID: "rel-17",
		Title: "Rumours", PrimaryType: "Album",
	}); err != nil {
		t.Fatalf("SetDesire: %v", err)
	}

	// Lose every file, so the prune runs at its most destructive.
	if err := db.Where("1 = 1").Delete(&models.LibraryItem{}).Error; err != nil {
		t.Fatalf("clear items: %v", err)
	}
	if _, _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	desires, err := DesiresForArtist(db, "art-1")
	if err != nil {
		t.Fatalf("DesiresForArtist: %v", err)
	}
	if len(desires) != 1 || desires[0].ReleaseMBID != "rel-17" {
		t.Errorf("rebuild disturbed authored desires: %+v", desires)
	}
}

func TestMediaSummary(t *testing.T) {
	tests := []struct {
		formats []string
		want    string
	}{
		{nil, ""},
		{[]string{"CD"}, "CD"},
		{[]string{"CD", "CD"}, "2×CD"},
		{[]string{"CD", "DVD"}, "CD + DVD"},
		{[]string{"CD", "CD", "DVD"}, "2×CD + DVD"},
		{[]string{""}, "Unknown"},
	}
	for _, tt := range tests {
		release := models.MusicBrainzReleaseResponse{}
		for _, f := range tt.formats {
			release.Media = append(release.Media, models.MusicBrainzMedia{Format: f})
		}
		if got := mediaSummary(release); got != tt.want {
			t.Errorf("mediaSummary(%v) = %q, want %q", tt.formats, got, tt.want)
		}
	}
}

package collection

import (
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Locating an artist on disk is derived, not stored, so what these tests pin is the
// derivation: that it follows the credit links (collaborations included), that it
// collapses many files into the few folders a scan should walk, and that it never
// invents a folder for an artist whose files are not there.

// artistOnDisk seeds one artist with one release-group, one owned edition, and an
// item per given file path.
func artistOnDisk(t *testing.T, db *gorm.DB, libraryPath string, paths ...string) (models.Library, string) {
	t.Helper()

	library := models.Library{Name: "L", Path: libraryPath, Enabled: true}
	if err := db.Create(&library).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	artistMBID := uuid.NewString()
	if err := db.Create(&models.CollectionArtist{MBID: artistMBID, Name: "Artist"}).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	rgMBID, releaseMBID := uuid.NewString(), uuid.NewString()
	if err := db.Create(&models.CollectionReleaseGroupArtist{ReleaseGroupMBID: rgMBID, ArtistMBID: artistMBID}).Error; err != nil {
		t.Fatalf("link release-group: %v", err)
	}
	if err := db.Create(&models.CollectionRelease{MBID: releaseMBID, ReleaseGroupMBID: rgMBID, ArtistMBID: artistMBID}).Error; err != nil {
		t.Fatalf("create release: %v", err)
	}
	for _, p := range paths {
		item := models.LibraryItem{
			LibraryID:   library.ID,
			Path:        p,
			MBReleaseID: releaseMBID,
			Status:      models.LibraryItemStatusOK,
		}
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("create item: %v", err)
		}
	}
	return library, artistMBID
}

// A whole discography lives under one artist folder, and that folder — not fifty
// album folders — is what a scan of the artist should walk.
func TestArtistTargetsCollapsesToArtistFolder(t *testing.T) {
	db := testDB(t)
	root := filepath.Join("/music")
	_, artistMBID := artistOnDisk(t, db, root,
		filepath.Join(root, "Artist", "Album (2020)", "01 a.flac"),
		filepath.Join(root, "Artist", "Album (2020)", "02 b.flac"),
		filepath.Join(root, "Artist", "Other (2022)", "01 c.flac"),
	)

	targets, err := ArtistTargets(db, artistMBID)
	if err != nil {
		t.Fatalf("ArtistTargets: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %d (%+v), want 1", len(targets), targets)
	}
	if want := filepath.Join(root, "Artist"); targets[0].Path != want {
		t.Errorf("path = %q, want %q", targets[0].Path, want)
	}
	if targets[0].Library.Path != root {
		t.Errorf("target lost its library: %+v", targets[0].Library)
	}
}

// The folder is read from the files, not from the artist's name: a library that
// spells the folder differently from MusicBrainz still resolves.
func TestArtistTargetsUsesFolderNameNotArtistName(t *testing.T) {
	db := testDB(t)
	root := "/music"
	_, artistMBID := artistOnDisk(t, db, root, filepath.Join(root, "Beatles, The", "Revolver (1966)", "01 a.flac"))

	targets, err := ArtistTargets(db, artistMBID)
	if err != nil {
		t.Fatalf("ArtistTargets: %v", err)
	}
	if len(targets) != 1 || targets[0].Path != filepath.Join(root, "Beatles, The") {
		t.Fatalf("targets = %+v, want the on-disk folder name", targets)
	}
}

// An artist with nothing on disk yields no target at all. The caller turns that into
// a refusal — scanning a guessed folder would either walk the wrong thing or report
// a silent zero.
func TestArtistTargetsEmptyWithoutFiles(t *testing.T) {
	db := testDB(t)
	artistMBID := uuid.NewString()
	if err := db.Create(&models.CollectionArtist{MBID: artistMBID, Name: "Nobody"}).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}

	targets, err := ArtistTargets(db, artistMBID)
	if err != nil {
		t.Fatalf("ArtistTargets: %v", err)
	}
	if len(targets) != 0 {
		t.Errorf("targets = %+v, want none", targets)
	}
}

// A collaboration belongs to both artists. The second one is credited only through
// the link table, which is exactly why the resolution goes through it.
func TestArtistItemsFollowsCreditLinks(t *testing.T) {
	db := testDB(t)
	root := "/music"
	_, primary := artistOnDisk(t, db, root, filepath.Join(root, "Artist", "Split (2020)", "01 a.flac"))

	var link models.CollectionReleaseGroupArtist
	if err := db.Where("artist_mb_id = ?", primary).First(&link).Error; err != nil {
		t.Fatalf("load link: %v", err)
	}
	guest := uuid.NewString()
	if err := db.Create(&models.CollectionReleaseGroupArtist{
		ReleaseGroupMBID: link.ReleaseGroupMBID, ArtistMBID: guest, Position: 1,
	}).Error; err != nil {
		t.Fatalf("link guest: %v", err)
	}

	items, err := ArtistItems(db, guest)
	if err != nil {
		t.Fatalf("ArtistItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("guest items = %d, want 1 — a collaborator owns the files too", len(items))
	}
}

// Files that failed to correlate have nothing to re-tag from, so they stay out of
// the re-tag set even though they sit in the artist's folder.
func TestArtistItemsSkipsFailedFiles(t *testing.T) {
	db := testDB(t)
	root := "/music"
	library, artistMBID := artistOnDisk(t, db, root, filepath.Join(root, "Artist", "Album (2020)", "01 a.flac"))

	var ok models.LibraryItem
	if err := db.First(&ok).Error; err != nil {
		t.Fatalf("load item: %v", err)
	}
	broken := models.LibraryItem{
		LibraryID:   library.ID,
		Path:        filepath.Join(root, "Artist", "Album (2020)", "02 broken.flac"),
		MBReleaseID: ok.MBReleaseID,
		Status:      models.LibraryItemStatusError,
	}
	if err := db.Create(&broken).Error; err != nil {
		t.Fatalf("create broken item: %v", err)
	}

	ids, err := ArtistItemIDs(db, artistMBID)
	if err != nil {
		t.Fatalf("ArtistItemIDs: %v", err)
	}
	if len(ids) != 1 {
		t.Errorf("ids = %d, want 1 (the failed file has no correlation to write)", len(ids))
	}
}

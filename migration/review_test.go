package migration

import (
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
)

// The review payload exists because the stored row reads as a row of zeroes for the
// commonest case on a manager-fed install. These cover the three questions it has to
// answer: what is wrong, do I own this, and what does approving do.

func ghostGroup(t *testing.T, db *gorm.DB, mbID, artistMBID, title string, mutate func(*models.CollectionReleaseGroup)) models.MusicbrainzMigration {
	t.Helper()
	storeGroup(t, db, mbID, artistMBID, title, mutate)
	m := models.MusicbrainzMigration{
		EntityType: models.MigrationEntityReleaseGroup,
		OldMBID:    mbID,
		Kind:       models.MigrationKindDeleted,
		Status:     models.MigrationStatusPending,
		Name:       title,
	}
	if err := db.Create(&m).Error; err != nil {
		t.Fatalf("create migration: %v", err)
	}
	return m
}

func ownFiles(t *testing.T, db *gorm.DB, releaseMBID, releaseGroupMBID, artistMBID string, n int) {
	t.Helper()
	if err := db.Create(&models.CollectionRelease{
		MBID: releaseMBID, ReleaseGroupMBID: releaseGroupMBID, ArtistMBID: artistMBID,
	}).Error; err != nil {
		t.Fatalf("create edition: %v", err)
	}
	for i := 0; i < n; i++ {
		if err := db.Create(&models.LibraryItem{
			Path:        releaseMBID + string(rune('a'+i)) + ".flac",
			Status:      models.LibraryItemStatusOK,
			MBReleaseID: releaseMBID,
		}).Error; err != nil {
			t.Fatalf("create item: %v", err)
		}
	}
}

// TestReviewCountsFilesUnderAnAlbum: "do I have files for this?" is the question a
// retirement most needs to answer, and the one the stored row cannot — AffectedFiles is
// zero for every release-group migration because files are keyed by release. Counting
// through the album's editions is the only route from one to the other.
func TestReviewCountsFilesUnderAnAlbum(t *testing.T) {
	db := testDB(t)
	m := ghostGroup(t, db, "rg-ghost", "artist-1", "Heatstroke", func(rg *models.CollectionReleaseGroup) {
		rg.Owned = true
	})
	if err := db.Create(&models.CollectionArtist{MBID: "artist-1", Name: "Some Band"}).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	ownFiles(t, db, "rel-a", "rg-ghost", "artist-1", 2)
	ownFiles(t, db, "rel-b", "rg-ghost", "artist-1", 1)

	r := NewReview(db, m)
	if r.FilesOnDisk != 3 {
		t.Errorf("FilesOnDisk = %d, want 3", r.FilesOnDisk)
	}
	if r.Editions != 2 {
		t.Errorf("Editions = %d, want 2", r.Editions)
	}
	if r.ArtistName != "Some Band" {
		t.Errorf("ArtistName = %q, want the artist a refresh would target", r.ArtistName)
	}
	if !strings.Contains(r.Problem, "3 files") {
		t.Errorf("the problem must say what is on disk, got %q", r.Problem)
	}
}

// TestReviewOfBlockedAlbumAsksTheManager: the case the user hits. An album the manager
// still lists is not refused — approving asks the manager — so the review has to say
// so before the press, and name the artist that will be refreshed.
func TestReviewOfBlockedAlbumAsksTheManager(t *testing.T) {
	db := testDB(t)
	m := ghostGroup(t, db, "rg-ghost", "artist-1", "Heatstroke", func(rg *models.CollectionReleaseGroup) {
		rg.InCatalog = true
	})
	if err := db.Create(&models.CollectionArtist{MBID: "artist-1", Name: "Some Band"}).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}

	r := NewReview(db, m)
	if !r.NeedsManagerRefresh {
		t.Fatal("an album blocked only by the manager listing it must be repairable")
	}
	if r.Blocker == "" {
		t.Error("the blocker must be stated before approving, not after failing")
	}
	if !strings.Contains(r.Effect, "Some Band") {
		t.Errorf("the effect must name the artist the manager will re-read, got %q", r.Effect)
	}
	if r.FilesOnDisk != 0 || !strings.Contains(r.Problem, "no files") {
		t.Errorf("owning nothing must be said plainly, got %q", r.Problem)
	}
}

// TestReviewOfOwnedAlbumIsNotRepairable: files on disk are an objection a manager
// refresh cannot answer, so approving must not go and refresh anything. Getting this
// wrong would turn every owned ghost into a pointless write to the manager.
func TestReviewOfOwnedAlbumIsNotRepairable(t *testing.T) {
	db := testDB(t)
	m := ghostGroup(t, db, "rg-ghost", "artist-1", "Heatstroke", func(rg *models.CollectionReleaseGroup) {
		rg.Owned = true
		rg.InCatalog = true
	})

	r := NewReview(db, m)
	if r.NeedsManagerRefresh {
		t.Error("an owned album is blocked by the disk, which no refresh changes")
	}
	if !strings.Contains(r.Effect, "would not remove anything") {
		t.Errorf("the effect must say nothing will happen, got %q", r.Effect)
	}
}

// TestReviewOfRetirableAlbum: nothing claims it, so approving removes it — and must say
// that no file is touched, which is the fear the sentence exists to settle.
func TestReviewOfRetirableAlbum(t *testing.T) {
	db := testDB(t)
	m := ghostGroup(t, db, "rg-ghost", "artist-1", "Heatstroke", nil)

	r := NewReview(db, m)
	if r.Blocker != "" || r.NeedsManagerRefresh {
		t.Errorf("nothing claims this album, got blocker %q", r.Blocker)
	}
	if !strings.Contains(r.Effect, "No files are touched") {
		t.Errorf("the effect must rule out file changes, got %q", r.Effect)
	}
}

// TestReviewOfReleaseMerge: the merge case still has to explain itself, including the
// pin rule — a manual attachment following a merge is the least obvious behaviour here.
func TestReviewOfReleaseMerge(t *testing.T) {
	db := testDB(t)
	m := models.MusicbrainzMigration{
		EntityType:    models.MigrationEntityRelease,
		OldMBID:       "rel-old",
		NewMBID:       "rel-new",
		Kind:          models.MigrationKindRedirect,
		Status:        models.MigrationStatusPending,
		AffectedFiles: 4,
		TouchesPinned: true,
	}
	if err := db.Create(&m).Error; err != nil {
		t.Fatalf("create migration: %v", err)
	}

	r := NewReview(db, m)
	if !strings.Contains(r.Problem, "merged") {
		t.Errorf("a merge must be described as one, got %q", r.Problem)
	}
	if !strings.Contains(r.Effect, "4 files") {
		t.Errorf("the effect must state the impact, got %q", r.Effect)
	}
	if !strings.Contains(r.Effect, "manual attachment") {
		t.Errorf("the pin rule must be spelled out, got %q", r.Effect)
	}
}

// TestReviewOfRetiredAlbumDoesNotInventContext: an applied migration has no
// release-group row left to read. That is the state it wanted, not a failure, so the
// review renders with empty context rather than erroring or claiming a blocker.
func TestReviewOfRetiredAlbumDoesNotInventContext(t *testing.T) {
	db := testDB(t)
	m := models.MusicbrainzMigration{
		EntityType: models.MigrationEntityReleaseGroup,
		OldMBID:    "rg-gone",
		Kind:       models.MigrationKindDeleted,
		Status:     models.MigrationStatusApplied,
	}
	if err := db.Create(&m).Error; err != nil {
		t.Fatalf("create migration: %v", err)
	}

	r := NewReview(db, m)
	if r.Blocker != "" || r.NeedsManagerRefresh || r.FilesOnDisk != 0 {
		t.Errorf("a retired album has no context to report, got %+v", r)
	}
}

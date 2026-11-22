package collection

import (
	"errors"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TestRemoveArtist: an artist with nothing on disk goes with their wants and the
// stored albums only they are credited on. A collaboration also credited to someone
// else stays, unlinked from them; another artist's want on it is kept too.
func TestRemoveArtist(t *testing.T) {
	db := testDB(t)
	for _, row := range []any{
		&models.CollectionArtist{MBID: "gone", Name: "Gone", Origin: models.CollectionOriginManual, ManagedBy: models.ManagedByAutotaggerr},
		&models.CollectionArtist{MBID: "stays", Name: "Stays", Origin: models.CollectionOriginManual, ManagedBy: models.ManagedByAutotaggerr},
		// Only theirs, picked.
		&models.CollectionReleaseGroup{MBID: "rg-solo", ArtistMBID: "gone", Title: "Solo", PrimaryType: "Album"},
		&models.CollectionReleaseGroupArtist{ReleaseGroupMBID: "rg-solo", ArtistMBID: "gone"},
		&models.CollectionRelease{MBID: "rel-solo", ReleaseGroupMBID: "rg-solo", ArtistMBID: "gone"},
		&models.CollectionDesire{ArtistMBID: "gone", ReleaseGroupMBID: "rg-solo"},
		// A collaboration: also credited to someone else.
		&models.CollectionReleaseGroup{MBID: "rg-collab", ArtistMBID: "gone", Title: "Collab", PrimaryType: "Single"},
		&models.CollectionReleaseGroupArtist{ReleaseGroupMBID: "rg-collab", ArtistMBID: "gone"},
		&models.CollectionReleaseGroupArtist{ReleaseGroupMBID: "rg-collab", ArtistMBID: "stays", Position: 1},
		// Linked only to them, but someone else wants it.
		&models.CollectionReleaseGroup{MBID: "rg-wanted", ArtistMBID: "gone", Title: "Wanted", PrimaryType: "EP"},
		&models.CollectionReleaseGroupArtist{ReleaseGroupMBID: "rg-wanted", ArtistMBID: "gone"},
		&models.CollectionDesire{ArtistMBID: "stays", ReleaseGroupMBID: "rg-wanted"},
		&models.ManagerLink{ManagerID: uuid.New(), EntityType: models.ManagerLinkArtist, MBID: "gone"},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("seed %T: %v", row, err)
		}
	}

	got, err := RemoveArtist(db, "gone")
	if err != nil {
		t.Fatalf("RemoveArtist: %v", err)
	}
	if got.Name != "Gone" || got.Picks != 1 || got.Albums != 1 {
		t.Errorf("result = %+v, want Gone with 1 pick and 1 album", got)
	}

	count := func(model any, query string, args ...any) int64 {
		t.Helper()
		var n int64
		if err := db.Model(model).Where(query, args...).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, c := range []struct {
		what  string
		model any
		query string
		arg   string
		want  int64
	}{
		{"the artist", &models.CollectionArtist{}, "mb_id = ?", "gone", 0},
		{"the other artist", &models.CollectionArtist{}, "mb_id = ?", "stays", 1},
		{"their solo album", &models.CollectionReleaseGroup{}, "mb_id = ?", "rg-solo", 0},
		{"its edition", &models.CollectionRelease{}, "release_group_mb_id = ?", "rg-solo", 0},
		{"the collaboration", &models.CollectionReleaseGroup{}, "mb_id = ?", "rg-collab", 1},
		{"the album someone else wants", &models.CollectionReleaseGroup{}, "mb_id = ?", "rg-wanted", 1},
		{"their links", &models.CollectionReleaseGroupArtist{}, "artist_mb_id = ?", "gone", 0},
		{"the other artist's link", &models.CollectionReleaseGroupArtist{}, "artist_mb_id = ?", "stays", 1},
		{"their wants", &models.CollectionDesire{}, "artist_mb_id = ?", "gone", 0},
		{"the other artist's want", &models.CollectionDesire{}, "artist_mb_id = ?", "stays", 1},
		{"their manager link", &models.ManagerLink{}, "mb_id = ?", "gone", 0},
	} {
		if n := count(c.model, c.query, c.arg); n != c.want {
			t.Errorf("%s: %d rows, want %d", c.what, n, c.want)
		}
	}

	if _, err := RemoveArtist(db, "gone"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("removing twice = %v, want not found", err)
	}
}

// TestRemoveArtistRefuses: an artist files credit would be back on the next scan, and
// one Lidarr governs or lists would be back on the next sync, so neither is removed.
func TestRemoveArtistRefuses(t *testing.T) {
	db := testDB(t)
	lidarr := models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true}
	if err := db.Create(&lidarr).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range []any{
		&models.CollectionArtist{MBID: "owner", Name: "Owner", ManagedBy: models.ManagedByAutotaggerr},
		&models.CollectionReleaseGroup{MBID: "rg-owned", ArtistMBID: "owner", Owned: true},
		&models.CollectionReleaseGroupArtist{ReleaseGroupMBID: "rg-owned", ArtistMBID: "owner"},
		&models.CollectionArtist{MBID: "copied", Name: "Copied", ManagedBy: models.ManagedByAutotaggerr},
		&models.CollectionReleaseCopy{ReleaseMBID: "rel-c", LibraryID: uuid.New(), ReleaseGroupMBID: "rg-c", ArtistMBID: "copied", Files: 1},
		&models.CollectionArtist{MBID: "governed", Name: "Governed", ManagedBy: models.ManagedByLidarr},
		&models.CollectionArtist{MBID: "listed", Name: "Listed", ManagedBy: models.ManagedByAutotaggerr},
		&models.ManagerLink{ManagerID: lidarr.ID, EntityType: models.ManagerLinkArtist, MBID: "listed"},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("seed %T: %v", row, err)
		}
	}

	for mbid, want := range map[string]error{
		"owner":    ErrArtistHasFiles,
		"copied":   ErrArtistHasFiles,
		"governed": ErrArtistGovernedByLidarr,
		"listed":   ErrArtistGovernedByLidarr,
	} {
		if _, err := RemoveArtist(db, mbid); !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", mbid, err, want)
		}
		var n int64
		db.Model(&models.CollectionArtist{}).Where("mb_id = ?", mbid).Count(&n)
		if n != 1 {
			t.Errorf("%s: the refused artist was deleted", mbid)
		}
	}
}

package collection

import (
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
)

// TestRefreshTurnedOffAsksTheArtistsManager: with refresh off on the manager that
// holds the artist and on on another, the repair is held. The check used to ask every
// manager, found one willing, and offered a button whose repair the governing manager
// then skipped.
func TestRefreshTurnedOffAsksTheArtistsManager(t *testing.T) {
	db := testDB(t)
	off := models.Manager{Name: "Holds the artist", Type: models.ManagerTypeLidarr, Enabled: true,
		LidarrBaseURL: "http://a", LidarrAPIKey: "k", LidarrSkipArtistRefresh: true}
	on := models.Manager{Name: "Elsewhere", Type: models.ManagerTypeLidarr, Enabled: true,
		LidarrBaseURL: "http://b", LidarrAPIKey: "k"}
	for _, m := range []*models.Manager{&off, &on} {
		if err := db.Create(m).Error; err != nil {
			t.Fatal(err)
		}
	}
	lib := models.Library{Name: "Music", Path: "/music", ManagerID: &off.ID}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.CollectionReleaseCopy{ReleaseMBID: "rel-1", LibraryID: lib.ID,
		ReleaseGroupMBID: "rg-1", ArtistMBID: "art-1", Files: 1}).Error; err != nil {
		t.Fatal(err)
	}

	held, err := RefreshTurnedOff(db, "art-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(held) != 1 || held[0] != off.Name {
		t.Errorf("held by %v, want [%q]", held, off.Name)
	}

	// An artist with no files in a Lidarr library may be listed by any manager, and
	// one of them can be asked.
	if held, _ := RefreshTurnedOff(db, "art-without-files"); held != nil {
		t.Errorf("an artist with no files was held by %v", held)
	}
	// The same rule for a caller holding the files: a native library leaves every
	// manager in the running.
	native := models.Library{Name: "Native", Path: "/native"}
	if err := db.Create(&native).Error; err != nil {
		t.Fatal(err)
	}
	if held, _ := RefreshTurnedOffIn(db, []uuid.UUID{native.ID}); held != nil {
		t.Errorf("a native library's files were held by %v", held)
	}
	if held, _ := RefreshTurnedOffIn(db, []uuid.UUID{lib.ID, native.ID}); len(held) != 1 {
		t.Errorf("files in the governing library were held by %v", held)
	}
}

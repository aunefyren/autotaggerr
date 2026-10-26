package collection

import (
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TestSyncRecordsWhichManagerHoldsWhat: the mirror pass records each manager's
// artists and albums, and the links built from them go to that manager's web address
// — the API address when no other is set.
func TestSyncRecordsWhichManagerHoldsWhat(t *testing.T) {
	first := lidarrListing(t, bothListArtist, []models.LidarrAlbum{{ID: 10, ForeignAlbumID: "rg-a", Title: "A", AlbumType: "Album"}}, false)
	second := lidarrListing(t, []models.LidarrArtist{}, nil, false)
	db := twoManagers(t, first, second)
	if err := db.Model(&models.Manager{}).Where("name = ?", "First").
		Update("lidarr_web_url", "https://lidarr.example.com/").Error; err != nil {
		t.Fatal(err)
	}

	if _, err := SyncLidarr(db); err != nil {
		t.Fatal(err)
	}

	artist, err := ManagerLinks(db, models.ManagerLinkArtist, "art-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(artist) != 1 || artist[0].Name != "First" || artist[0].URL != "https://lidarr.example.com/artist/art-1" {
		t.Errorf("artist links = %+v, want First's web address only", artist)
	}
	album, _ := ManagerLinks(db, models.ManagerLinkReleaseGroup, "rg-a")
	if len(album) != 1 || album[0].URL != "https://lidarr.example.com/album/rg-a" || album[0].Kind != "lidarr" {
		t.Errorf("album links = %+v", album)
	}

	// The album leaves the manager: its link goes with the next listing.
	db2 := twoManagers(t, lidarrListing(t, bothListArtist, []models.LidarrAlbum{}, false), second)
	if err := db2.Create(&models.ManagerLink{ManagerID: managerID(t, db2, "First"), EntityType: models.ManagerLinkReleaseGroup,
		MBID: "rg-gone", ArtistMBID: "art-1"}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := SyncLidarr(db2); err != nil {
		t.Fatal(err)
	}
	if links, _ := ManagerLinks(db2, models.ManagerLinkReleaseGroup, "rg-gone"); len(links) != 0 {
		t.Errorf("an album the manager no longer lists still links: %+v", links)
	}

	// A disabled manager links nowhere.
	if err := db.Model(&models.Manager{}).Where("name = ?", "First").Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	if links, _ := ManagerLinks(db, models.ManagerLinkArtist, "art-1"); len(links) != 0 {
		t.Errorf("a disabled manager still links: %+v", links)
	}
}

func managerID(t *testing.T, db *gorm.DB, name string) uuid.UUID {
	t.Helper()
	var m models.Manager
	if err := db.Where("name = ?", name).First(&m).Error; err != nil {
		t.Fatal(err)
	}
	return m.ID
}

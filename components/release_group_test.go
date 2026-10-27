package components

import (
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
)

// TestRecordItemKeepsTheReleaseGroup: a correlation that knows the download's release
// group (Lidarr's track file) records it, even on a pinned file — it is where the file
// came from, not which track it is — and one that does not know leaves it alone rather
// than forgetting it.
func TestRecordItemKeepsTheReleaseGroup(t *testing.T) {
	db := testDB(t)
	lib := uuid.New()
	path := "/music/Artist/Album/01 Song.flac"
	group := "GRP"
	recordItem(db, lib, path, models.Correlation{MBReleaseID: "rel", MBReleaseTrackID: "trk", ReleaseGroup: &group}, true, "v1", models.ManagerTypeLidarr, nil)

	read := func() models.LibraryItem {
		var item models.LibraryItem
		if err := db.Where("path = ?", path).First(&item).Error; err != nil {
			t.Fatal(err)
		}
		return item
	}
	if item := read(); item.ReleaseGroup == nil || *item.ReleaseGroup != "GRP" {
		t.Fatalf("release group = %v, want GRP", item.ReleaseGroup)
	}

	recordItem(db, lib, path, models.Correlation{MBReleaseID: "rel", MBReleaseTrackID: "trk"}, true, "v1", models.ManagerTypeLidarr, nil)
	if item := read(); item.ReleaseGroup == nil || *item.ReleaseGroup != "GRP" {
		t.Errorf("after a source that does not know = %v, want GRP kept", item.ReleaseGroup)
	}

	db.Model(&models.LibraryItem{}).Where("path = ?", path).Update("pinned", true)
	other := "NEW"
	recordItem(db, lib, path, models.Correlation{MBReleaseID: "rel-2", MBReleaseTrackID: "trk-2", ReleaseGroup: &other}, true, "v1", models.ManagerTypeLidarr, nil)
	if item := read(); item.ReleaseGroup == nil || *item.ReleaseGroup != "NEW" || item.MBReleaseID != "rel" {
		t.Errorf("pinned = group %v, release %q; want the new group and the pinned release", item.ReleaseGroup, item.MBReleaseID)
	}
}

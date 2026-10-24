package components

import (
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

func namingRelease() models.MusicBrainzReleaseResponse {
	credit := []models.ArtistCredit{{Name: "Slint", Artist: models.Artist{ID: "a1", Name: "Slint"}}}
	return models.MusicBrainzReleaseResponse{
		ID: "rel-1", Title: "Spiderland", Date: "1991-03-27", ArtistCredit: credit,
		ReleaseGroup: models.ReleaseGroup{ID: "rg-1", Title: "Spiderland", FirstReleaseDate: "1991-03-27", ArtistCredit: credit},
		Media: []models.MusicBrainzMedia{{Position: 1, Format: "CD", Tracks: []models.Track{
			{ID: "t1", Position: 1, Title: "Breadcrumb Trail"},
		}}},
	}
}

// TestComputeItemPath: the PATH row is the library's profile naming rendered for the
// file's own correlation, relative to the library root — unchanged for a file already
// where its profile puts it, and marked as never acted on under Lidarr.
func TestComputeItemPath(t *testing.T) {
	db := testDB(t)
	meta := fakeMeta{release: func(string) (models.MusicBrainzReleaseResponse, error) { return namingRelease(), nil }}
	root := filepath.FromSlash("/music")

	profile := models.TaggerProfile{Name: "Lidarr-ish", WriteTags: true, NamingTrackFile: "{Artist Name} - {track:00} - {Track Title}"}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatal(err)
	}
	lidarr := models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true}
	if err := db.Create(&lidarr).Error; err != nil {
		t.Fatal(err)
	}
	native := models.Library{Name: "Native", Path: root, Enabled: true, TaggerProfileID: &profile.ID}
	managed := models.Library{Name: "Managed", Path: filepath.FromSlash("/lidarr"), Enabled: true, TaggerProfileID: &profile.ID, ManagerID: &lidarr.ID}
	for _, l := range []*models.Library{&native, &managed} {
		if err := db.Create(l).Error; err != nil {
			t.Fatal(err)
		}
	}

	item := func(library models.Library, rel string) models.LibraryItem {
		return models.LibraryItem{LibraryID: library.ID, Path: filepath.Join(library.Path, filepath.FromSlash(rel)), MBReleaseID: "rel-1", MBReleaseTrackID: "t1"}
	}

	got, err := ComputeItemPath(db, meta, item(native, "Slint/Spiderland (1991)/Slint - 01 - Breadcrumb Trail.flac"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Changed || got.Note != "" {
		t.Errorf("a file already in place should be unchanged: %+v", got)
	}

	got, err = ComputeItemPath(db, meta, item(native, "slint/spiderland/1 breadcrumb.flac"))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Changed || got.Current != "slint/spiderland/1 breadcrumb.flac" || got.Desired != "Slint/Spiderland (1991)/Slint - 01 - Breadcrumb Trail.flac" {
		t.Errorf("misplaced file: %+v", got)
	}
	if got.Note != "" {
		t.Errorf("a native library's proposal carries no note: %q", got.Note)
	}

	got, err = ComputeItemPath(db, meta, item(managed, "slint/spiderland/1 breadcrumb.flac"))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Changed || got.Note == "" {
		t.Errorf("under Lidarr the proposal must say it will not be acted on: %+v", got)
	}

	if _, err := ComputeItemPath(db, meta, models.LibraryItem{LibraryID: native.ID, Path: "/music/x.flac"}); err == nil {
		t.Error("an uncorrelated file has no proposed path")
	}
}

// A naming edit must not change the tagging fingerprint, or one template edit
// re-tags the whole library on the next run.
func TestNamingIsNotPartOfTheFingerprint(t *testing.T) {
	before := NewTagger(models.TaggerProfile{WriteTags: true}).Fingerprint()
	after := NewTagger(models.TaggerProfile{WriteTags: true, NamingAlbumFolder: "{Release Year} - {Album Title}"}).Fingerprint()
	if before != after {
		t.Error("changing a naming template changed the tagging fingerprint")
	}
}

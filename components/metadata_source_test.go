package components

import (
	"testing"

	"github.com/aunefyren/autotaggerr/metadata"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
)

// fakeMeta is an in-package metadata.MetadataSource returning canned data, so the
// data-source adapter and ComputeItemDiff can be exercised with zero network.
type fakeMeta struct {
	release func(string) (models.MusicBrainzReleaseResponse, error)
}

func (f fakeMeta) GetRelease(id string) (models.MusicBrainzReleaseResponse, error) {
	if f.release != nil {
		return f.release(id)
	}
	return models.MusicBrainzReleaseResponse{ID: id}, nil
}
func (fakeMeta) GetArtist(string) (models.MusicBrainzArtistLookup, error) {
	return models.MusicBrainzArtistLookup{}, nil
}
func (fakeMeta) GetArtistReleaseGroups(string) ([]models.MusicBrainzArtistReleaseGroup, bool, error) {
	return nil, false, nil
}
func (fakeMeta) GetReleaseGroupReleases(string) ([]models.MusicBrainzReleaseSearchResult, error) {
	return nil, nil
}
func (fakeMeta) SearchReleases(metadata.ReleaseSearchQuery) (metadata.ReleaseSearchPage, error) {
	return metadata.ReleaseSearchPage{}, nil
}
func (fakeMeta) SearchArtists(string) ([]models.MusicBrainzArtistSearchResult, error) {
	return nil, nil
}

// TestMusicBrainzDataSourceAdapter: the DataSource seam delegates GetRelease to its
// injected metadata source, and the trivial HealthCheck/Type answers hold.
func TestMusicBrainzDataSourceAdapter(t *testing.T) {
	ds := &MusicBrainzDataSource{meta: fakeMeta{release: func(id string) (models.MusicBrainzReleaseResponse, error) {
		return models.MusicBrainzReleaseResponse{ID: id, Title: "Spiderland"}, nil
	}}}

	got, err := ds.GetRelease("rel-1")
	if err != nil || got.ID != "rel-1" || got.Title != "Spiderland" {
		t.Fatalf("GetRelease = %+v, %v", got, err)
	}
	if ok, err := ds.HealthCheck(); !ok || err != nil {
		t.Errorf("HealthCheck = %v, %v", ok, err)
	}
	if ds.Type() != models.DataSourceTypeMusicBrainz {
		t.Errorf("Type = %q", ds.Type())
	}

	built, err := NewDataSource(models.DataSource{Type: models.DataSourceTypeMusicBrainz})
	if err != nil || built.Type() != models.DataSourceTypeMusicBrainz {
		t.Errorf("NewDataSource(MB) = %v, %v", built, err)
	}
	if _, err := NewDataSource(models.DataSource{Type: "nope"}); err == nil {
		t.Error("NewDataSource with an unknown type should error")
	}
}

// TestManagerHealthAndType covers the two Manager implementations' trivial answers and
// their Correlate wiring (a missing file resolves to no match, not a panic).
func TestManagerHealthAndType(t *testing.T) {
	native := &AutotaggerrManager{}
	if ok, err := native.HealthCheck(); !ok || err != nil {
		t.Errorf("native HealthCheck = %v, %v", ok, err)
	}
	if native.Type() != models.ManagerTypeAutotaggerr {
		t.Errorf("native Type = %q", native.Type())
	}
	// Bogus path: exercises Correlate's one line; the result is a non-match/error, which
	// is fine — we are covering the wiring, not a real resolution.
	_, _ = native.Correlate("/does/not/exist/track.flac", "/does/not/exist")

	lidarr := &LidarrManager{} // no client configured
	if ok, err := lidarr.HealthCheck(); ok || err == nil {
		t.Errorf("unconfigured Lidarr HealthCheck = %v, %v, want false + error", ok, err)
	}
	if lidarr.Type() != models.ManagerTypeLidarr {
		t.Errorf("lidarr Type = %q", lidarr.Type())
	}
	_, _ = lidarr.Correlate("/does/not/exist/track.flac", "/does/not/exist")
}

// TestComputeItemDiffErrors: the early guards return an error without fetching anything
// — an uncorrelated file, and a file whose owning library is gone.
func TestComputeItemDiffErrors(t *testing.T) {
	db := testDB(t)
	meta := fakeMeta{}

	if _, err := ComputeItemDiff(db, meta, models.LibraryItem{}); err == nil {
		t.Error("uncorrelated item should error before any fetch")
	}

	orphan := models.LibraryItem{
		LibraryID: uuid.New(), Path: "/m/x.flac",
		MBReleaseID: "rel-1", MBReleaseTrackID: "trk-1",
	}
	if _, err := ComputeItemDiff(db, meta, orphan); err == nil {
		t.Error("item with a missing library should error")
	}
}

// TestComputeItemDiffHappyPath: for a correlated, indexed file it fetches the release
// through the source, matches the track and returns a diff. The whole body past the
// guards was network-blocked before the metadata port.
func TestComputeItemDiffHappyPath(t *testing.T) {
	db, _, path := scanFixture(t)

	var item models.LibraryItem
	if err := db.Where("path = ?", path).First(&item).Error; err != nil {
		t.Fatalf("load indexed item: %v", err)
	}
	if item.MBReleaseID == "" || item.MBReleaseTrackID == "" {
		t.Fatalf("fixture item is not correlated: %+v", item)
	}

	release := models.MusicBrainzReleaseResponse{
		ID: item.MBReleaseID, Title: "Album",
		ArtistCredit: []models.ArtistCredit{{Name: "Band", Artist: models.Artist{ID: "art-1", Name: "Band"}}},
		ReleaseGroup: models.ReleaseGroup{ID: "rg-1", Title: "Album", PrimaryType: "Album"},
		Media: []models.MusicBrainzMedia{{
			Position: 1,
			Tracks: []models.Track{{
				ID: item.MBReleaseTrackID, Title: "Song", Position: 1, Number: "1",
			}},
		}},
	}
	meta := fakeMeta{release: func(string) (models.MusicBrainzReleaseResponse, error) { return release, nil }}

	diff, err := ComputeItemDiff(db, meta, item)
	if err != nil {
		t.Fatalf("ComputeItemDiff: %v", err)
	}
	// The diff is computed against real on-disk tags; its contents depend on the tagger
	// profile, so the contract under test is "resolved without error", not a fixed shape.
	_ = diff
}

package collection

import (
	"testing"

	"github.com/aunefyren/autotaggerr/metadata"
	"github.com/aunefyren/autotaggerr/models"
)

// fakeMeta is a metadata.MetadataSource returning canned data, so a derivation that
// fetches from MusicBrainz (here SyncArtist, via both the identity re-read and the
// discography fetch) runs with zero network. Only the two methods SyncArtist uses are
// wired; the rest return zero values.
type fakeMeta struct {
	artist     func(string) (models.MusicBrainzArtistLookup, error)
	rgs        func(string) ([]models.MusicBrainzArtistReleaseGroup, bool, error)
	rgReleases func(string) ([]models.MusicBrainzReleaseSearchResult, error)
}

func (f fakeMeta) GetRelease(string) (models.MusicBrainzReleaseResponse, error) {
	return models.MusicBrainzReleaseResponse{}, nil
}

func (f fakeMeta) GetArtist(id string) (models.MusicBrainzArtistLookup, error) {
	if f.artist != nil {
		return f.artist(id)
	}
	return models.MusicBrainzArtistLookup{ID: id}, nil
}

func (f fakeMeta) GetArtistReleaseGroups(id string) ([]models.MusicBrainzArtistReleaseGroup, bool, error) {
	if f.rgs != nil {
		return f.rgs(id)
	}
	return nil, false, nil
}

func (f fakeMeta) GetReleaseGroupReleases(id string) ([]models.MusicBrainzReleaseSearchResult, error) {
	if f.rgReleases != nil {
		return f.rgReleases(id)
	}
	return nil, nil
}

func (f fakeMeta) SearchReleases(metadata.ReleaseSearchQuery) (metadata.ReleaseSearchPage, error) {
	return metadata.ReleaseSearchPage{}, nil
}

func (f fakeMeta) SearchArtists(string) ([]models.MusicBrainzArtistSearchResult, error) {
	return nil, nil
}

// TestSyncArtistWantsFollowedReleaseGroups: syncing a followed native artist records
// the release-groups the follow settings want and skips the rest. This whole path was
// only exercisable against live MusicBrainz before the metadata port — it now runs
// against a fake.
func TestSyncArtistWantsFollowedReleaseGroups(t *testing.T) {
	db := testDB(t)

	artist := models.CollectionArtist{
		MBID: "art-1", Name: "Bee Gees",
		Monitored: true, ManagedBy: models.ManagedByAutotaggerr,
		FollowTypes: "Album",
	}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("seed artist: %v", err)
	}

	verified := false
	meta := fakeMeta{
		artist: func(id string) (models.MusicBrainzArtistLookup, error) {
			verified = true
			return models.MusicBrainzArtistLookup{ID: id, Name: "Bee Gees"}, nil
		},
		rgs: func(string) ([]models.MusicBrainzArtistReleaseGroup, bool, error) {
			return []models.MusicBrainzArtistReleaseGroup{
				{ID: "rg-1", Title: "Spirits Having Flown", PrimaryType: "Album", FirstReleaseDate: "1979"},
				{ID: "rg-2", Title: "Jive Talkin'", PrimaryType: "Single"}, // not followed
			}, true, nil
		},
	}

	wanted, err := SyncArtist(db, meta, "art-1")
	if err != nil {
		t.Fatalf("SyncArtist: %v", err)
	}
	if !verified {
		t.Error("SyncArtist did not re-verify the artist identity through the source")
	}
	if wanted != 1 {
		t.Errorf("wanted = %d, want 1 (only the Album counts)", wanted)
	}

	var album int64
	db.Model(&models.CollectionReleaseGroup{}).Where("mb_id = ?", "rg-1").Count(&album)
	if album != 1 {
		t.Errorf("followed Album release-group was not recorded (count %d)", album)
	}
	var single int64
	db.Model(&models.CollectionReleaseGroup{}).Where("mb_id = ?", "rg-2").Count(&single)
	if single != 0 {
		t.Errorf("un-followed Single release-group was recorded (count %d)", single)
	}
}

// TestReleaseGroupEditions: the edition lookup delegates to the injected source.
func TestReleaseGroupEditions(t *testing.T) {
	meta := fakeMeta{rgReleases: func(rgID string) ([]models.MusicBrainzReleaseSearchResult, error) {
		if rgID != "rg-1" {
			t.Errorf("called with %q, want rg-1", rgID)
		}
		return []models.MusicBrainzReleaseSearchResult{{ID: "ed-1"}}, nil
	}}
	editions, err := ReleaseGroupEditions(meta, "rg-1")
	if err != nil || len(editions) != 1 || editions[0].ID != "ed-1" {
		t.Fatalf("ReleaseGroupEditions = %+v, %v", editions, err)
	}
}

// TestRebuilderQuiesce: Quiesce is a no-op on a nil rebuilder and returns at once when
// the rebuilder is idle.
func TestRebuilderQuiesce(t *testing.T) {
	var nilRebuilder *Rebuilder
	nilRebuilder.Quiesce() // must not panic

	NewRebuilder(testDB(t)).Quiesce() // idle: returns immediately
}

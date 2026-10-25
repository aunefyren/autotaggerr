package collection

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// discography is an artist's MusicBrainz release-groups: three albums (one from 1990),
// an EP, a single and a live album.
func discography() []models.MusicBrainzArtistReleaseGroup {
	return []models.MusicBrainzArtistReleaseGroup{
		{ID: "a1", PrimaryType: "Album", FirstReleaseDate: "1990"},
		{ID: "a2", PrimaryType: "Album", FirstReleaseDate: "2005"},
		{ID: "a3", PrimaryType: "Album", FirstReleaseDate: "2020-03-01"},
		{ID: "e1", PrimaryType: "EP", FirstReleaseDate: "2010"},
		{ID: "s1", PrimaryType: "Single", FirstReleaseDate: "2011"},
		{ID: "l1", PrimaryType: "Album", SecondaryTypes: []string{"Live"}, FirstReleaseDate: "2012"},
	}
}

// The catalogue is counted by the artist's own follow shape, followed or not: albums
// and EPs by default, narrowed by the cutoff, widened by the types it takes in.
func TestRecordCatalogueCountsTheFollowShape(t *testing.T) {
	db := testDB(t)
	for _, tc := range []struct {
		name   string
		artist models.CollectionArtist
		want   int
	}{
		{"default shape", models.CollectionArtist{MBID: "x1"}, 4},
		{"from 2006", models.CollectionArtist{MBID: "x2", FollowFromYear: 2006}, 2},
		{"singles and live too", models.CollectionArtist{MBID: "x3", FollowTypes: "Album,EP,Single", FollowSecondary: true}, 6},
	} {
		if err := db.Create(&tc.artist).Error; err != nil {
			t.Fatal(err)
		}
		if err := RecordCatalogue(db, tc.artist, discography()); err != nil {
			t.Fatal(err)
		}
		var got models.CollectionArtist
		db.Where("mb_id = ?", tc.artist.MBID).First(&got)
		if got.CatalogueCount != tc.want || got.CatalogueReadAt == nil {
			t.Errorf("%s: catalogue = %d read %v, want %d", tc.name, got.CatalogueCount, got.CatalogueReadAt, tc.want)
		}
	}
}

// Following records the catalogue too, since the sync reads the discography anyway.
func TestSyncArtistRecordsTheCatalogue(t *testing.T) {
	db := testDB(t)
	db.Create(&models.CollectionArtist{MBID: "art", Monitored: true, ManagedBy: models.ManagedByAutotaggerr})
	meta := fakeMeta{rgs: func(string) ([]models.MusicBrainzArtistReleaseGroup, bool, error) { return discography(), true, nil }}
	if _, err := SyncArtist(db, meta, "art"); err != nil {
		t.Fatal(err)
	}
	var got models.CollectionArtist
	db.Where("mb_id = ?", "art").First(&got)
	if got.CatalogueCount != 4 {
		t.Errorf("catalogue after a sync = %d, want 4", got.CatalogueCount)
	}
}

type countingReader struct{ n atomic.Int32 }

func (c *countingReader) ReadCatalogues() { c.n.Add(1) }

// A new artist — from files, or added by hand — asks for its catalogue to be read; an
// artist the collection already had does not.
func TestNewArtistsAskForTheirCatalogue(t *testing.T) {
	db := testDB(t)
	reader := &countingReader{}
	SetCatalogueReader(reader)
	t.Cleanup(func() { SetCatalogueReader(nil) })

	if err := upsertArtist(db, "found", "Found", models.ManagedByAutotaggerr); err != nil {
		t.Fatal(err)
	}
	if err := upsertArtist(db, "found", "Found", models.ManagedByAutotaggerr); err != nil {
		t.Fatal(err)
	}
	if _, err := AddArtist(db, "added", "Added"); err != nil {
		t.Fatal(err)
	}
	if got := reader.n.Load(); got != 2 {
		t.Errorf("catalogue reads asked for = %d, want 2 (one per new artist)", got)
	}
}

// The policy decides whether an artist found in your files is followed — and never
// for an artist a manager governs.
func TestNewArtistPolicy(t *testing.T) {
	db := testDB(t)
	artistAfter := func(mbid, managedBy string) models.CollectionArtist {
		t.Helper()
		if err := upsertArtist(db, mbid, mbid, managedBy); err != nil {
			t.Fatal(err)
		}
		var a models.CollectionArtist
		db.Where("mb_id = ?", mbid).First(&a)
		return a
	}

	if a := artistAfter("off", models.ManagedByAutotaggerr); a.Monitored {
		t.Error("followed with the policy at its default")
	}

	if _, err := SavePolicy(db, models.CollectionPolicy{NewArtistFollow: models.NewArtistFollowNew, FollowTypes: "Album"}); err != nil {
		t.Fatal(err)
	}
	if a := artistAfter("new", models.ManagedByAutotaggerr); !a.Monitored || a.FollowFromYear != time.Now().Year() || a.FollowTypes != "Album" {
		t.Errorf("follow from now = %+v", a)
	}
	if a := artistAfter("lidarr", models.ManagedByLidarr); a.Monitored {
		t.Error("an artist Lidarr governs was followed")
	}

	if _, err := SavePolicy(db, models.CollectionPolicy{NewArtistFollow: models.NewArtistFollowAll}); err != nil {
		t.Fatal(err)
	}
	if a := artistAfter("all", models.ManagedByAutotaggerr); !a.Monitored || a.FollowFromYear != 0 {
		t.Errorf("follow everything = %+v", a)
	}

	// An existing artist is never re-decided by the policy.
	db.Model(&models.CollectionArtist{}).Where("mb_id = ?", "off").Update("monitored", false)
	if a := artistAfter("off", models.ManagedByAutotaggerr); a.Monitored {
		t.Error("the policy re-followed an existing artist")
	}

	if _, err := SavePolicy(db, models.CollectionPolicy{NewArtistFollow: "sometimes"}); err == nil {
		t.Error("an unknown policy was saved")
	}
}

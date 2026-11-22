package collection

import (
	"errors"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
)

func noLibrary(string) (uuid.UUID, error) { return uuid.Nil, nil }

// TestAddWantAlbumAddsArtistUnfollowed: wanting an album of an artist you own nothing
// of adds the artist — manual, not followed — and one any-edition, whole-album want.
func TestAddWantAlbumAddsArtistUnfollowed(t *testing.T) {
	db := testDB(t)
	res, err := AddWant(db, AddWantInput{
		ArtistMBID: "art-1", ArtistName: "Radiohead",
		ReleaseGroupMBID: "rg-1", Title: "OK Computer", PrimaryType: "Album",
	}, noLibrary)
	if err != nil {
		t.Fatalf("AddWant: %v", err)
	}
	if !res.ArtistAdded || res.Artist.Origin != models.CollectionOriginManual || res.Artist.Monitored {
		t.Errorf("artist = %+v (added %v), want a new manual unfollowed artist", res.Artist, res.ArtistAdded)
	}
	if len(res.Desires) != 1 || res.Desires[0].ReleaseMBID != "" || len(res.Desires[0].RecordingMBIDs) != 0 {
		t.Fatalf("desires = %+v, want one any-edition whole-album want", res.Desires)
	}
	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", "rg-1").First(&rg).Error; err != nil || rg.Title != "OK Computer" {
		t.Errorf("release-group not stored with its title: %+v (%v)", rg, err)
	}
}

// TestAddWantSpecificEditionsAndTracks: two editions are two rows, and one track of
// a fifteen-track edition is that edition narrowed to one recording.
func TestAddWantSpecificEditionsAndTracks(t *testing.T) {
	db := testDB(t)
	if _, err := AddWant(db, AddWantInput{
		ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1",
		Picks: []WantPick{{ReleaseMBID: "rel-a"}, {ReleaseMBID: "rel-b", RecordingMBIDs: []string{"rec-7"}}},
	}, noLibrary); err != nil {
		t.Fatalf("AddWant: %v", err)
	}
	var rows []models.CollectionDesire
	db.Where("release_group_mb_id = ?", "rg-1").Order("release_mb_id").Find(&rows)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want two", rows)
	}
	if rows[0].ReleaseMBID != "rel-a" || len(rows[0].RecordingMBIDs) != 0 {
		t.Errorf("rel-a = %+v, want the whole edition", rows[0])
	}
	if rows[1].ReleaseMBID != "rel-b" || len(rows[1].RecordingMBIDs) != 1 || rows[1].RecordingMBIDs[0] != "rec-7" {
		t.Errorf("rel-b = %+v, want one recording", rows[1])
	}
}

// TestAddWantIsIdempotentAndKeepsTheArtist: re-asking is the same want, and an
// artist already in the collection keeps their follow and provenance.
func TestAddWantIsIdempotentAndKeepsTheArtist(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionArtist{MBID: "art-1", Name: "Band", Monitored: true, Origin: models.CollectionOriginLibrary, ManagedBy: models.ManagedByAutotaggerr}).Error; err != nil {
		t.Fatalf("artist: %v", err)
	}
	in := AddWantInput{ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", Picks: []WantPick{{RecordingMBIDs: []string{"rec-1"}}}}
	for i := 0; i < 2; i++ {
		res, err := AddWant(db, in, noLibrary)
		if err != nil {
			t.Fatalf("AddWant #%d: %v", i, err)
		}
		if res.ArtistAdded {
			t.Errorf("#%d: reported adding an artist already present", i)
		}
	}
	var artists []models.CollectionArtist
	db.Find(&artists)
	if len(artists) != 1 || !artists[0].Monitored || artists[0].Origin != models.CollectionOriginLibrary {
		t.Errorf("artists = %+v, want the one artist untouched", artists)
	}
	var n int64
	db.Model(&models.CollectionDesire{}).Count(&n)
	if n != 1 {
		t.Errorf("desires = %d, want 1", n)
	}
}

// TestAddWantRefusesMixedPicks: "any edition" beside a specific edition is a
// contradiction SetDesire would settle by deleting one, so nothing is written.
func TestAddWantRefusesMixedPicks(t *testing.T) {
	db := testDB(t)
	_, err := AddWant(db, AddWantInput{
		ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1",
		Picks: []WantPick{{}, {ReleaseMBID: "rel-a"}},
	}, noLibrary)
	if !errors.Is(err, ErrMixedPicks) {
		t.Fatalf("err = %v, want ErrMixedPicks", err)
	}
	var n int64
	db.Model(&models.CollectionArtist{}).Count(&n)
	if n != 0 {
		t.Errorf("artists = %d, want none written", n)
	}
	if _, err := AddWant(db, AddWantInput{ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", Picks: []WantPick{{ReleaseMBID: "x"}, {ReleaseMBID: "x"}}}, noLibrary); err == nil {
		t.Error("an edition listed twice was accepted")
	}
}

// TestAddWantRefusedTargetWritesNothing: an existing artist whose wants are not
// Autotaggerr's to write is refused before any row changes.
func TestAddWantRefusedTargetWritesNothing(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionArtist{MBID: "art-1", Name: "Band", ManagedBy: models.ManagedByLidarr}).Error; err != nil {
		t.Fatalf("artist: %v", err)
	}
	refuse := func(string) (uuid.UUID, error) { return uuid.Nil, ErrNoNativeLibrary }
	if _, err := AddWant(db, AddWantInput{ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", Title: "Album"}, refuse); !errors.Is(err, ErrNoNativeLibrary) {
		t.Fatalf("err = %v, want the target's refusal", err)
	}
	var n int64
	db.Model(&models.CollectionDesire{}).Count(&n)
	if n != 0 {
		t.Errorf("desires = %d, want none", n)
	}
	db.Model(&models.CollectionReleaseGroup{}).Count(&n)
	if n != 0 {
		t.Errorf("release-groups = %d, want none", n)
	}
}

// TestAddWantNeedsANativeLibrary: where Lidarr manages every library, a new artist's
// album could be wanted but never fulfilled, as with Add artist.
func TestAddWantNeedsANativeLibrary(t *testing.T) {
	db := testDB(t)
	mgr := models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true}
	if err := db.Create(&mgr).Error; err != nil {
		t.Fatalf("manager: %v", err)
	}
	if err := db.Create(&models.Library{Name: "Music", Path: "/music", ManagerID: &mgr.ID, Enabled: true}).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	if _, err := AddWant(db, AddWantInput{ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1"}, noLibrary); !errors.Is(err, ErrAllLibrariesManaged) {
		t.Fatalf("err = %v, want ErrAllLibrariesManaged", err)
	}
}

// TestAddWantVariousArtists: a song from a compilation is stored under Various
// Artists, which is added like any artist and not followed.
func TestAddWantVariousArtists(t *testing.T) {
	db := testDB(t)
	res, err := AddWant(db, AddWantInput{
		ArtistMBID: models.VariousArtistsMBID, ArtistName: "Various Artists", ReleaseGroupMBID: "rg-comp",
		Picks: []WantPick{{RecordingMBIDs: []string{"rec-1"}}},
	}, noLibrary)
	if err != nil {
		t.Fatalf("AddWant: %v", err)
	}
	if res.Artist.Monitored || len(res.Desires) != 1 {
		t.Errorf("res = %+v, want an unfollowed artist and one want", res)
	}
}

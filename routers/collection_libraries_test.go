package routers

import (
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// TestCollectionSaysWhichLibraryHoldsAnAlbum: the artist page and the collection list
// both name the libraries an album lives in, from the copies the rebuild records.
func TestCollectionSaysWhichLibraryHoldsAnAlbum(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	music := models.Library{Name: "Music", Path: "/music"}
	sandbox := models.Library{Name: "Music Sandbox", Path: "/sandbox"}
	for _, l := range []*models.Library{&music, &sandbox} {
		if err := api.DB.Create(l).Error; err != nil {
			t.Fatalf("library: %v", err)
		}
	}
	artist := artistFixture(t, api.DB, "artist-1", "Compton's Most Wanted", models.ManagedByMixed, false)
	releaseGroupFixture(t, api.DB, artist.MBID, rgFixture{
		mbid: "rg-1", title: "Music to Driveby", primary: "Album",
		owned: true, ownedTracks: 12, totalTracks: 12,
	})
	releaseGroupFixture(t, api.DB, artist.MBID, rgFixture{
		mbid: "rg-2", title: "Straight Checkn 'Em", primary: "Album",
		owned: true, ownedTracks: 10, totalTracks: 10,
	})
	for _, c := range []models.CollectionReleaseCopy{
		{ReleaseMBID: "rel-1", ReleaseGroupMBID: "rg-1", ArtistMBID: artist.MBID, LibraryID: music.ID, OwnedTracks: 12, Files: 12},
		{ReleaseMBID: "rel-1", ReleaseGroupMBID: "rg-1", ArtistMBID: artist.MBID, LibraryID: sandbox.ID, OwnedTracks: 11, Files: 11},
		{ReleaseMBID: "rel-2", ReleaseGroupMBID: "rg-2", ArtistMBID: artist.MBID, LibraryID: music.ID, OwnedTracks: 10, Files: 10},
	} {
		if err := api.DB.Create(&c).Error; err != nil {
			t.Fatalf("copy: %v", err)
		}
	}

	type copyJSON struct {
		LibraryID   string `json:"library_id"`
		Name        string `json:"name"`
		OwnedTracks int    `json:"owned_tracks"`
	}
	detail := decodeJSON[struct {
		ReleaseGroups []struct {
			MBID      string     `json:"mb_id"`
			Libraries []copyJSON `json:"libraries"`
		} `json:"release_groups"`
	}](t, r, "GET", "/api/v1/artists/artist-1", token, nil)

	got := map[string][]copyJSON{}
	for _, rg := range detail.ReleaseGroups {
		got[rg.MBID] = rg.Libraries
	}
	if l := got["rg-1"]; len(l) != 2 || l[0].Name != "Music" || l[1].Name != "Music Sandbox" || l[1].OwnedTracks != 11 {
		t.Errorf("rg-1 libraries = %+v, want Music then Music Sandbox (11 tracks)", l)
	}
	if l := got["rg-2"]; len(l) != 1 || l[0].Name != "Music" {
		t.Errorf("rg-2 libraries = %+v, want Music alone", l)
	}

	list := decodeJSON[[]struct {
		LibraryIDs []string `json:"library_ids"`
	}](t, r, "GET", "/api/v1/artists", token, nil)
	if len(list) != 1 || len(list[0].LibraryIDs) != 2 {
		t.Fatalf("list = %+v, want one artist in both libraries", list)
	}
}

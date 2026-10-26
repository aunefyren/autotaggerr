package routers

import (
	"net/http"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
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

// TestArtistPageReadsOneLibrary: an artist Lidarr governs in one library and
// Autotaggerr in another reads as Lidarr's, frozen, under the first — and as the
// user's, editable and with its own ownership, under the second. Every library at
// once is read-only; an API call naming no library goes to the one native library.
func TestArtistPageReadsOneLibrary(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	api.Meta = &fakeMeta{}

	mgr := models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true}
	if err := api.DB.Create(&mgr).Error; err != nil {
		t.Fatalf("manager: %v", err)
	}
	music2 := models.Library{Name: "Music 2", Path: "/music2", ManagerID: &mgr.ID, Enabled: true}
	music3 := models.Library{Name: "Music 3", Path: "/music3", Enabled: true}
	for _, l := range []*models.Library{&music2, &music3} {
		if err := api.DB.Create(l).Error; err != nil {
			t.Fatalf("library: %v", err)
		}
	}
	artist := artistFixture(t, api.DB, "artist-1", "The Weeknd", models.ManagedByMixed, false)
	releaseGroupFixture(t, api.DB, artist.MBID, rgFixture{
		mbid: "rg-timeless", title: "Timeless", primary: "Single",
		owned: true, ownedTracks: 1, totalTracks: 1,
		inCatalog: true, catalogOwned: 1, catalogTotal: 1, catalogMonitored: true, catalogRelease: "rel-1",
	})
	releaseGroupFixture(t, api.DB, artist.MBID, rgFixture{
		mbid: "rg-other", title: "Other", primary: "Album",
		owned: true, ownedTracks: 9, totalTracks: 10,
	})
	for _, row := range []any{
		&models.CollectionRelease{MBID: "rel-1", ReleaseGroupMBID: "rg-timeless", ArtistMBID: artist.MBID, OwnedTracks: 1, TotalTracks: 1, Files: 2},
		&models.CollectionRelease{MBID: "rel-2", ReleaseGroupMBID: "rg-other", ArtistMBID: artist.MBID, OwnedTracks: 9, TotalTracks: 10, Files: 9},
		&models.CollectionReleaseCopy{ReleaseMBID: "rel-1", ReleaseGroupMBID: "rg-timeless", ArtistMBID: artist.MBID, LibraryID: music2.ID, OwnedTracks: 1, Files: 1},
		&models.CollectionReleaseCopy{ReleaseMBID: "rel-1", ReleaseGroupMBID: "rg-timeless", ArtistMBID: artist.MBID, LibraryID: music3.ID, OwnedTracks: 1, Files: 1},
		&models.CollectionReleaseCopy{ReleaseMBID: "rel-2", ReleaseGroupMBID: "rg-other", ArtistMBID: artist.MBID, LibraryID: music2.ID, OwnedTracks: 9, Files: 9},
		&models.CollectionDesire{ArtistMBID: artist.MBID, ReleaseGroupMBID: "rg-timeless", ReleaseMBID: "rel-1", Source: models.DesireSourceManager},
	} {
		if err := api.DB.Create(row).Error; err != nil {
			t.Fatalf("seed %T: %v", row, err)
		}
	}

	type rgJSON struct {
		MBID             string `json:"mb_id"`
		Owned            bool   `json:"owned"`
		Wanted           bool   `json:"wanted"`
		WantedSource     string `json:"wanted_source"`
		IdentityEditable bool   `json:"identity_editable"`
	}
	type detailJSON struct {
		Artist struct {
			IdentityEditable bool    `json:"identity_editable"`
			EditLibraryID    *string `json:"edit_library_id"`
			EditBlocked      string  `json:"edit_blocked"`
			FollowGoverns    bool    `json:"follow_governs"`
			Libraries        []struct {
				Name      string `json:"name"`
				Authority string `json:"authority"`
				Holds     bool   `json:"holds"`
			} `json:"libraries"`
		} `json:"artist"`
		ReleaseGroups []rgJSON `json:"release_groups"`
	}
	byMBID := func(d detailJSON) map[string]rgJSON {
		out := map[string]rgJSON{}
		for _, g := range d.ReleaseGroups {
			out[g.MBID] = g
		}
		return out
	}

	lidarr := decodeJSON[detailJSON](t, r, "GET", "/api/v1/artists/artist-1?library="+music2.ID.String(), token, nil)
	if lidarr.Artist.IdentityEditable || lidarr.Artist.EditBlocked != "lidarr" || lidarr.Artist.FollowGoverns {
		t.Errorf("in Lidarr's library the artist = %+v, want frozen as Lidarr's", lidarr.Artist)
	}
	if g := byMBID(lidarr)["rg-timeless"]; !g.Wanted || g.WantedSource != "manager" || g.IdentityEditable {
		t.Errorf("Timeless in Lidarr's library = %+v, want Lidarr's frozen want", g)
	}
	if len(lidarr.Artist.Libraries) != 2 || lidarr.Artist.Libraries[0].Authority != "lidarr" || lidarr.Artist.Libraries[1].Authority != "autotaggerr" {
		t.Errorf("libraries = %+v, want Music 2 (Lidarr) and Music 3 (native)", lidarr.Artist.Libraries)
	}

	native := decodeJSON[detailJSON](t, r, "GET", "/api/v1/artists/artist-1?library="+music3.ID.String(), token, nil)
	if !native.Artist.IdentityEditable || native.Artist.EditLibraryID == nil || *native.Artist.EditLibraryID != music3.ID.String() || !native.Artist.FollowGoverns {
		t.Errorf("in the native library the artist = %+v, want editable into Music 3", native.Artist)
	}
	rows := byMBID(native)
	if g := rows["rg-timeless"]; g.Wanted || !g.Owned || !g.IdentityEditable {
		t.Errorf("Timeless in Music 3 = %+v, want owned, not wanted, editable", g)
	}
	if g := rows["rg-other"]; g.Owned {
		t.Errorf("an album only Music 2 holds reads as owned in Music 3: %+v", g)
	}

	// Every library at once mixes Lidarr's wants with Music 3's, so it edits neither:
	// the page asks for a library to be chosen.
	overview := decodeJSON[detailJSON](t, r, "GET", "/api/v1/artists/artist-1", token, nil)
	if overview.Artist.IdentityEditable || overview.Artist.EditBlocked != "choose_library" {
		t.Errorf("overview artist = %+v, want read-only until a library is chosen", overview.Artist)
	}

	// Wanting into Lidarr's library is refused; an API want naming no library lands
	// in the one native library.
	want := map[string]any{"release_group_mb_id": "rg-other", "title": "Other", "library_id": music2.ID.String()}
	if w := do(r, "POST", "/api/v1/artists/artist-1/desires", token, want); w.Code != http.StatusConflict {
		t.Errorf("want into Lidarr's library = %d, want 409: %s", w.Code, w.Body.String())
	}
	delete(want, "library_id")
	if w := do(r, "POST", "/api/v1/artists/artist-1/desires", token, want); w.Code != http.StatusOK {
		t.Fatalf("want from the overview = %d: %s", w.Code, w.Body.String())
	}
	var d models.CollectionDesire
	if err := api.DB.Where("release_group_mb_id = ?", "rg-other").First(&d).Error; err != nil || d.LibraryID != music3.ID {
		t.Errorf("want = %+v (%v), want it for Music 3", d, err)
	}

	// So does a follow naming no library.
	if w := do(r, "POST", "/api/v1/artists/artist-1/follow", token, map[string]any{"monitored": true}); w.Code != http.StatusOK {
		t.Fatalf("follow = %d: %s", w.Code, w.Body.String())
	}
	var saved models.CollectionArtist
	api.DB.Where("mb_id = ?", "artist-1").First(&saved)
	if !saved.Monitored || saved.FollowLibraryID != music3.ID {
		t.Errorf("artist after follow = monitored %v into %s, want Music 3", saved.Monitored, saved.FollowLibraryID)
	}
	if w := do(r, "POST", "/api/v1/artists/artist-1/follow", token, map[string]any{"monitored": false}); w.Code != http.StatusOK {
		t.Fatalf("unfollow = %d: %s", w.Code, w.Body.String())
	}
	api.DB.Where("mb_id = ?", "artist-1").First(&saved)
	if saved.Monitored || saved.FollowLibraryID != uuid.Nil {
		t.Errorf("artist after unfollow = %+v, want the library cleared with it", saved)
	}
}

// TestAddArtistRefusedWhenLidarrManagesEveryLibrary: the add is a 409 naming where to
// add the artist instead, not a 400 — the request was fine, the install cannot use it.
func TestAddArtistRefusedWhenLidarrManagesEveryLibrary(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	mgr := models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true}
	if err := api.DB.Create(&mgr).Error; err != nil {
		t.Fatalf("manager: %v", err)
	}
	if err := api.DB.Create(&models.Library{Name: "Music", Path: "/music", ManagerID: &mgr.ID, Enabled: true}).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	w := do(r, "POST", "/api/v1/artists", token, map[string]any{"mb_id": "art-1", "name": "Band"})
	expectStatus(t, "lidarr only", w.Code, w.Body.String(), http.StatusConflict, "add the artist in Lidarr")
}

package routers

import (
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// TestMCPReadsOneLibrary: get_artist and get_album take a library, by name, and then
// read as the artist page does under that tab — Lidarr's wants under Lidarr's
// library, none of them under the native one. Without one they cover every library,
// list the libraries to choose from, and say why nothing can be written there.
func TestMCPReadsOneLibrary(t *testing.T) {
	_, api := setupAPI(t)
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
	session := mcpSession(t, api, mcpReader)

	albums := func(d mcpArtistDetail) map[string]mcpAlbumRow {
		out := map[string]mcpAlbumRow{}
		for _, a := range d.Albums {
			out[a.MBID] = a
		}
		return out
	}

	all := callMCP[mcpArtistDetail](t, session, "get_artist", map[string]any{"mbid": "artist-1"})
	if all.Library != "" || all.EditBlocked != "choose_library" || len(all.Libraries) != 2 {
		t.Errorf("overview = library %q, edit_blocked %q, libraries %+v; want every library, choose_library, two to choose from", all.Library, all.EditBlocked, all.Libraries)
	}
	authority := map[string]string{}
	for _, l := range all.Libraries {
		authority[l.Name] = l.Authority
	}
	if authority["Music 2"] != models.ManagedByLidarr || authority["Music 3"] != models.ManagedByAutotaggerr {
		t.Errorf("authorities = %v", authority)
	}

	lidarr := callMCP[mcpArtistDetail](t, session, "get_artist", map[string]any{"mbid": "artist-1", "library": "music 2"})
	if lidarr.Library != "Music 2" || lidarr.EditBlocked != "lidarr" {
		t.Errorf("Music 2 = library %q, edit_blocked %q", lidarr.Library, lidarr.EditBlocked)
	}
	if g := albums(lidarr)["rg-timeless"]; !g.Wanted || g.WantedSource != "manager" {
		t.Errorf("Music 2 rg-timeless = %+v, want Lidarr's want", g)
	}

	native := callMCP[mcpArtistDetail](t, session, "get_artist", map[string]any{"mbid": "artist-1", "library": music3.ID.String()})
	if native.Library != "Music 3" || native.EditBlocked != "" {
		t.Errorf("Music 3 = library %q, edit_blocked %q", native.Library, native.EditBlocked)
	}
	rows := albums(native)
	if g := rows["rg-timeless"]; g.Wanted || !g.Owned {
		t.Errorf("Music 3 rg-timeless = %+v, want held and not wanted", g)
	}
	if g := rows["rg-other"]; g.Owned {
		t.Errorf("Music 3 rg-other = %+v, want not held there", g)
	}

	album := callMCP[mcpAlbumDetail](t, session, "get_album", map[string]any{"artist_mbid": "artist-1", "album_mbid": "rg-timeless", "library": "Music 3"})
	if album.Library != "Music 3" || album.Album.Wanted {
		t.Errorf("get_album in Music 3 = library %q, album %+v", album.Library, album.Album)
	}

	expectMCPError(t, session, "get_artist", map[string]any{"mbid": "artist-1", "library": "Nowhere"}, "no library named")
	expectMCPError(t, session, "get_album", map[string]any{"artist_mbid": "artist-1", "album_mbid": "rg-timeless", "library": "Nowhere"}, "no library named")
}

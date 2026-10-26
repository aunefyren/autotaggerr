package routers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
)

// TestPlexSectionsAndTheLibrarysSection: the form lists the server's music sections,
// says when Plex is not configured or not answering, and a library stores its section
// and its opt-out.
func TestPlexSectionsAndTheLibrarysSection(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	read := func() plexSectionsView {
		t.Helper()
		w := do(r, http.MethodGet, "/api/v1/plex/sections", token, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("sections: %d %s", w.Code, w.Body.String())
		}
		var v plexSectionsView
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	if v := read(); v.Configured {
		t.Errorf("no Plex reported as configured: %+v", v)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<MediaContainer><Directory key="4" title="Music" type="artist"><Location path="/data/music"/></Directory></MediaContainer>`))
	}))
	t.Cleanup(srv.Close)
	api.Plex = modules.NewPlexClient(srv.URL, "tok")
	if v := read(); !v.Configured || len(v.Sections) != 1 || v.Sections[0].Key != "4" {
		t.Errorf("sections = %+v", v)
	}

	srv.Close()
	if v := read(); !v.Configured || v.Error == "" {
		t.Errorf("a Plex that does not answer = %+v, want the error in the body", v)
	}

	lib := models.Library{Name: "Music", Path: "/music", Enabled: true}
	if err := api.DB.Create(&lib).Error; err != nil {
		t.Fatal(err)
	}
	if w := do(r, http.MethodPut, "/api/v1/libraries/"+lib.ID.String(), token, map[string]any{
		"plex_section_key": " 4 ", "plex_skip": true,
	}); w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	var got models.Library
	if err := api.DB.First(&got, "id = ?", lib.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.PlexSectionKey != "4" || !got.PlexSkip {
		t.Errorf("library = section %q skip %v", got.PlexSectionKey, got.PlexSkip)
	}
}

// TestAlbumLinksIntoPlexAndLidarr: the album page links to the album Plex lists its
// files in, through app.plex.tv, once per library; and to the Lidarr that holds it.
func TestAlbumLinksIntoPlexAndLidarr(t *testing.T) {
	r, api := setupAPI(t)
	api.Meta = &fakeMeta{}
	token := loginToken(t, r)
	modules.ResetPlexAlbumCacheForTest(t)

	var release models.MusicBrainzReleaseResponse
	if err := json.Unmarshal([]byte(`{"id":"rel-1","title":"Album",
		"artist-credit":[{"name":"Artist","artist":{"id":"art-1","name":"Artist"}}],
		"media":[{"position":1,"tracks":[{"id":"trk-1","title":"Song","number":"1","position":1,"recording":{"id":"rec-1","title":"Song"}}]}]}`), &release); err != nil {
		t.Fatal(err)
	}
	modules.SeedMusicBrainzReleaseForTest(t, release)

	plex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/identity":
			_, _ = w.Write([]byte(`<MediaContainer machineIdentifier="machine-1"/>`))
		case r.URL.Path == "/library/sections":
			_, _ = w.Write([]byte(`<MediaContainer><Directory key="4" title="Music" type="artist"/></MediaContainer>`))
		case r.URL.Path == "/library/sections/4/all" && r.URL.Query().Get("type") == "9":
			_, _ = w.Write([]byte(`<MediaContainer><Directory key="/library/metadata/55/children" title="Album" parentTitle="Artist" type="album"/></MediaContainer>`))
		case r.URL.Path == "/library/metadata/55/children":
			_, _ = w.Write([]byte(`<MediaContainer><Track title="Song"><Media><Part file="/data/music/Artist/Album/01 Song.flac"/></Media></Track></MediaContainer>`))
		default:
			_, _ = w.Write([]byte(`<MediaContainer/>`))
		}
	}))
	t.Cleanup(plex.Close)
	api.Plex = modules.NewPlexClient(plex.URL, "tok")

	lidarr := models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true, LidarrBaseURL: "http://lidarr:8686", LidarrWebURL: "https://lidarr.example.com"}
	lib := models.Library{Name: "Music", Path: "/music", Enabled: true}
	skipped := models.Library{Name: "Elsewhere", Path: "/other", Enabled: true, PlexSkip: true}
	for _, row := range []any{&lidarr, &lib, &skipped,
		&models.CollectionArtist{MBID: "art-1", Name: "Artist"},
		&models.CollectionReleaseGroup{MBID: "rg-1", ArtistMBID: "art-1", Title: "Album"},
		&models.CollectionRelease{MBID: "rel-1", ReleaseGroupMBID: "rg-1", ArtistMBID: "art-1"},
	} {
		if err := api.DB.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, it := range []models.LibraryItem{
		{LibraryID: lib.ID, Path: "/music/Artist/Album/01 Song.flac", MBReleaseID: "rel-1", MBReleaseTrackID: "trk-1", Status: models.LibraryItemStatusOK},
		{LibraryID: skipped.ID, Path: "/other/Artist/Album/01 Song.flac", MBReleaseID: "rel-1", MBReleaseTrackID: "trk-1", Status: models.LibraryItemStatusOK},
	} {
		if err := api.DB.Create(&it).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := api.DB.Create(&models.ManagerLink{ManagerID: lidarr.ID, EntityType: models.ManagerLinkReleaseGroup, MBID: "rg-1", ArtistMBID: "art-1"}).Error; err != nil {
		t.Fatal(err)
	}

	w := do(r, http.MethodGet, "/api/v1/artists/art-1/release-groups/rg-1/plex", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("plex links: %d %s", w.Code, w.Body.String())
	}
	var plexLinks []struct{ Kind, Name, URL string }
	if err := json.Unmarshal(w.Body.Bytes(), &plexLinks); err != nil {
		t.Fatal(err)
	}
	want := "https://app.plex.tv/desktop/#!/server/machine-1/details?key=%2Flibrary%2Fmetadata%2F55"
	if len(plexLinks) != 1 || plexLinks[0].Name != "Music" || plexLinks[0].URL != want {
		t.Errorf("plex links = %+v, want one for Music only", plexLinks)
	}

	w = do(r, http.MethodGet, "/api/v1/artists/art-1/release-groups/rg-1", token, nil)
	var detail struct {
		Links []struct{ Kind, Name, URL string } `json:"links"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Links) != 1 || detail.Links[0].URL != "https://lidarr.example.com/album/rg-1" {
		t.Errorf("lidarr links = %+v", detail.Links)
	}

	// The same links reach an assistant: get_album carries both services, get_artist
	// the Lidarr that holds the artist.
	if err := api.DB.Create(&models.ManagerLink{ManagerID: lidarr.ID, EntityType: models.ManagerLinkArtist, MBID: "art-1"}).Error; err != nil {
		t.Fatal(err)
	}
	session := mcpSession(t, api, mcpReader)
	album := callMCP[mcpAlbumDetail](t, session, "get_album", map[string]any{"artist_mbid": "art-1", "album_mbid": "rg-1"})
	if len(album.Links) != 2 || album.Links[0].Kind != "lidarr" || album.Links[1].Kind != "plex" || album.Links[1].URL != want {
		t.Errorf("get_album links = %+v, want Lidarr then Plex", album.Links)
	}
	artist := callMCP[mcpArtistDetail](t, session, "get_artist", map[string]any{"mbid": "art-1"})
	if len(artist.Links) != 1 || artist.Links[0].URL != "https://lidarr.example.com/artist/art-1" {
		t.Errorf("get_artist links = %+v", artist.Links)
	}
}

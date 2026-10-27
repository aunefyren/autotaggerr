package routers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
)

// TestMediaServersAndTheLibraryLinks: an admin adds a server (the token is never
// returned), the form lists its sections — or says why it cannot — and a library is
// linked to it in one section, replaced as a set, and cleaned up with the server.
func TestMediaServersAndTheLibraryLinks(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<MediaContainer><Directory key="4" title="Music" type="artist"><Location path="/data/music"/></Directory></MediaContainer>`))
	}))
	t.Cleanup(srv.Close)

	if w := do(r, http.MethodPost, "/api/v1/media-servers", token, map[string]any{"name": "Home", "base_url": srv.URL, "token": ""}); w.Code != http.StatusBadRequest {
		t.Errorf("a server without a token = %d, want 400", w.Code)
	}
	w := do(r, http.MethodPost, "/api/v1/media-servers", token, map[string]any{"name": "Home", "base_url": srv.URL + "/", "token": "tok"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "tok\"") {
		t.Errorf("the token came back: %s", w.Body.String())
	}
	var server struct {
		ID       string `json:"id"`
		BaseURL  string `json:"base_url"`
		TokenSet bool   `json:"token_set"`
		Type     string `json:"type"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &server); err != nil {
		t.Fatal(err)
	}
	if server.BaseURL != srv.URL || !server.TokenSet || server.Type != models.MediaServerTypePlex {
		t.Errorf("created = %+v", server)
	}
	if w := do(r, http.MethodPost, "/api/v1/media-servers", token, map[string]any{"name": "Home", "base_url": srv.URL, "token": "x"}); w.Code != http.StatusConflict {
		t.Errorf("a second server named Home = %d, want 409", w.Code)
	}

	sections := func() mediaServerSectionsView {
		t.Helper()
		w := do(r, http.MethodGet, "/api/v1/media-servers/"+server.ID+"/sections", token, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("sections: %d %s", w.Code, w.Body.String())
		}
		var v mediaServerSectionsView
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := sections(); len(v.Sections) != 1 || v.Sections[0].Key != "4" {
		t.Errorf("sections = %+v", v)
	}

	lib := models.Library{Name: "Music", Path: "/music", Enabled: true}
	if err := api.DB.Create(&lib).Error; err != nil {
		t.Fatal(err)
	}
	link := func(body any) *httptest.ResponseRecorder {
		return do(r, http.MethodPut, "/api/v1/libraries/"+lib.ID.String(), token, map[string]any{"media_servers": body})
	}
	if w := link([]map[string]any{{"media_server_id": uuid.NewString()}}); w.Code != http.StatusBadRequest {
		t.Errorf("a link to no server = %d, want 400", w.Code)
	}
	if w := link([]map[string]any{{"media_server_id": server.ID}, {"media_server_id": server.ID}}); w.Code != http.StatusBadRequest {
		t.Errorf("one server linked twice = %d, want 400", w.Code)
	}
	w = link([]map[string]any{{"media_server_id": server.ID, "section_key": " 4 "}})
	if w.Code != http.StatusOK {
		t.Fatalf("link: %d %s", w.Code, w.Body.String())
	}
	var got models.Library
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.MediaServers) != 1 || got.MediaServers[0].SectionKey != "4" {
		t.Errorf("library links = %+v, want section 4", got.MediaServers)
	}
	// An edit that does not mention the links leaves them alone.
	if w := do(r, http.MethodPut, "/api/v1/libraries/"+lib.ID.String(), token, map[string]any{"name": "Music!"}); w.Code != http.StatusOK {
		t.Fatalf("rename: %d", w.Code)
	}
	var count int64
	api.DB.Model(&models.LibraryMediaServer{}).Where("library_id = ?", lib.ID).Count(&count)
	if count != 1 {
		t.Errorf("links after an unrelated edit = %d, want 1", count)
	}

	// The list carries the library count and never the token; an edit without a token
	// keeps the stored one, and the test answers 200 with the verdict in the body.
	w = do(r, http.MethodGet, "/api/v1/media-servers", token, nil)
	var listed []struct {
		Name      string `json:"name"`
		Libraries int    `json:"libraries"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil || len(listed) != 1 || listed[0].Libraries != 1 {
		t.Errorf("list = %s", w.Body.String())
	}
	if w := do(r, http.MethodPut, "/api/v1/media-servers/"+server.ID, token, map[string]any{"web_url": "plex.local"}); w.Code != http.StatusBadRequest {
		t.Errorf("a link address without a scheme = %d, want 400", w.Code)
	}
	if w := do(r, http.MethodPut, "/api/v1/media-servers/"+server.ID, token, map[string]any{"web_url": " https://plex.example.com/ "}); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"web_url":"https://plex.example.com"`) {
		t.Errorf("set link address = %d %s", w.Code, w.Body.String())
	}
	if w := do(r, http.MethodPut, "/api/v1/media-servers/"+server.ID, token, map[string]any{"name": "Home Plex", "enabled": false}); w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	var stored models.MediaServer
	api.DB.First(&stored, "id = ?", server.ID)
	if stored.Name != "Home Plex" || stored.Token != "tok" || stored.Enabled {
		t.Errorf("after edit = %+v, want renamed, disabled, token kept", stored)
	}
	if w := do(r, http.MethodGet, "/api/v1/media-servers/"+server.ID, token, nil); w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"token":`) {
		t.Errorf("get = %d %s", w.Code, w.Body.String())
	}

	srv.Close()
	w = do(r, http.MethodPost, "/api/v1/media-servers/"+server.ID+"/test", token, nil)
	var verdict struct {
		Healthy bool   `json:"healthy"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &verdict); err != nil || w.Code != http.StatusOK || verdict.Healthy || verdict.Error == "" {
		t.Errorf("test against a server that is gone = %d %s", w.Code, w.Body.String())
	}
	if v := sections(); v.Error == "" {
		t.Errorf("a server that does not answer = %+v, want the error in the body", v)
	}

	if w := do(r, http.MethodDelete, "/api/v1/media-servers/"+server.ID, token, nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", w.Code)
	}
	api.DB.Model(&models.LibraryMediaServer{}).Count(&count)
	if count != 0 {
		t.Errorf("links after the server went = %d, want 0", count)
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
	server := models.MediaServer{Name: "Plex", Type: models.MediaServerTypePlex, Enabled: true, BaseURL: plex.URL, Token: "tok"}
	if err := api.DB.Create(&server).Error; err != nil {
		t.Fatal(err)
	}

	lidarr := models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true, LidarrBaseURL: "http://lidarr:8686", LidarrWebURL: "https://lidarr.example.com"}
	lib := models.Library{Name: "Music", Path: "/music", Enabled: true}
	// Not linked to the server, so it gets no Plex link.
	skipped := models.Library{Name: "Elsewhere", Path: "/other", Enabled: true}
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
	if err := api.DB.Create(&models.LibraryMediaServer{LibraryID: lib.ID, MediaServerID: server.ID}).Error; err != nil {
		t.Fatal(err)
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

	// With a link address the link opens that server's own web app, still addressed by
	// its machine identifier, rather than going through app.plex.tv.
	if err := api.DB.Model(&server).Update("web_url", "https://plex.example.com").Error; err != nil {
		t.Fatal(err)
	}
	w = do(r, http.MethodGet, "/api/v1/artists/art-1/release-groups/rg-1/plex", token, nil)
	plexLinks = nil
	if err := json.Unmarshal(w.Body.Bytes(), &plexLinks); err != nil {
		t.Fatal(err)
	}
	want = "https://plex.example.com/web/index.html#!/server/machine-1/details?key=%2Flibrary%2Fmetadata%2F55"
	if len(plexLinks) != 1 || plexLinks[0].URL != want {
		t.Errorf("plex links with a link address = %+v, want %s", plexLinks, want)
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

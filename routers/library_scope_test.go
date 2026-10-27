package routers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// TestArtistActionsRespectTheLibrary: the artist page read in a library acts on that
// library's files — Tag files counts only them, a library without the artist is a 409
// that says so, a malformed library is a 400 — and its links lead only into that
// library's manager.
func TestArtistActionsRespectTheLibrary(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	lidarr := models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true, LidarrBaseURL: "http://lidarr:8686"}
	if err := api.DB.Create(&lidarr).Error; err != nil {
		t.Fatal(err)
	}
	music := models.Library{Name: "Music", Path: "/music", Enabled: true, ManagerID: &lidarr.ID}
	sandbox := models.Library{Name: "Sandbox", Path: "/sandbox", Enabled: true}
	empty := models.Library{Name: "Empty", Path: "/empty", Enabled: true}
	// The libraries first: the rows below read their IDs.
	for _, l := range []*models.Library{&music, &sandbox, &empty} {
		if err := api.DB.Create(l).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []any{
		&models.CollectionArtist{MBID: "art-1", Name: "Band", ManagedBy: models.ManagedByMixed},
		&models.CollectionReleaseGroupArtist{ReleaseGroupMBID: "rg-1", ArtistMBID: "art-1"},
		&models.CollectionRelease{MBID: "rel-1", ReleaseGroupMBID: "rg-1", ArtistMBID: "art-1"},
		&models.LibraryItem{LibraryID: music.ID, Path: "/music/Band/A/01.flac", MBReleaseID: "rel-1", Status: models.LibraryItemStatusOK},
		&models.LibraryItem{LibraryID: music.ID, Path: "/music/Band/A/02.flac", MBReleaseID: "rel-1", Status: models.LibraryItemStatusOK},
		&models.LibraryItem{LibraryID: sandbox.ID, Path: "/sandbox/Band/A/01.flac", MBReleaseID: "rel-1", Status: models.LibraryItemStatusOK},
		&models.ManagerLink{ManagerID: lidarr.ID, EntityType: models.ManagerLinkArtist, MBID: "art-1"},
	} {
		if err := api.DB.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}

	files := func(query string) int {
		t.Helper()
		w := do(r, http.MethodPost, "/api/v1/artists/art-1/retag"+query, token, nil)
		if w.Code != http.StatusAccepted {
			t.Fatalf("retag%s = %d %s", query, w.Code, w.Body.String())
		}
		var body struct{ Files int }
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return body.Files
	}
	if got := files(""); got != 3 {
		t.Errorf("every library = %d files, want 3", got)
	}
	if got := files("?library=" + sandbox.ID.String()); got != 1 {
		t.Errorf("sandbox = %d files, want 1", got)
	}

	if w := do(r, http.MethodPost, "/api/v1/artists/art-1/process?library="+empty.ID.String(), token, nil); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "that library") {
		t.Errorf("process in a library without the artist = %d %s", w.Code, w.Body.String())
	}
	if w := do(r, http.MethodPost, "/api/v1/artists/art-1/process?library=nope", token, nil); w.Code != http.StatusBadRequest {
		t.Errorf("malformed library = %d, want 400", w.Code)
	}

	links := func(query string) int {
		t.Helper()
		w := do(r, http.MethodGet, "/api/v1/artists/art-1"+query, token, nil)
		var body struct {
			Links []struct{ Kind string } `json:"links"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusOK {
			t.Fatalf("artist%s = %d %s", query, w.Code, w.Body.String())
		}
		return len(body.Links)
	}
	if got := links(""); got != 1 {
		t.Errorf("overview links = %d, want the Lidarr link", got)
	}
	if got := links("?library=" + music.ID.String()); got != 1 {
		t.Errorf("Lidarr library links = %d, want its Lidarr", got)
	}
	if got := links("?library=" + sandbox.ID.String()); got != 0 {
		t.Errorf("native library links = %d, want none", got)
	}
}

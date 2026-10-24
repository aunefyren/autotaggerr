package routers

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

func importSource(t *testing.T) (root, file string) {
	t.Helper()
	root = t.TempDir()
	file = filepath.Join(root, "Band - Album", "01 Song.flac")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, file
}

func TestImportRequiresAdmin(t *testing.T) {
	r, api := setupAPI(t)
	viewer := userToken(t, r, api.DB)
	for _, call := range []struct{ method, path string }{
		{"GET", "/api/v1/import/scan?path=/tmp"},
		{"GET", "/api/v1/import/folders?path=/tmp"},
		{"POST", "/api/v1/import/preview"},
		{"POST", "/api/v1/import"},
	} {
		if w := do(r, call.method, call.path, viewer, map[string]any{}); w.Code != http.StatusForbidden {
			t.Errorf("%s %s as a non-admin = %d, want 403", call.method, call.path, w.Code)
		}
	}
}

// The source must be a real folder outside every library: inside one, its files are
// already indexed; containing one, the library's own files would be offered.
func TestImportScanValidatesTheSource(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	root, _ := importSource(t)
	libPath := t.TempDir()
	if err := api.DB.Create(&models.Library{Name: "Music", Path: libPath, Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		path string
		code int
		msg  string
	}{
		{"relative/path", http.StatusBadRequest, "full path"},
		{filepath.Join(root, "missing"), http.StatusUnprocessableEntity, "cannot read"},
		{filepath.Join(libPath, "x"), http.StatusUnprocessableEntity, "cannot read"},
		{libPath, http.StatusBadRequest, "inside the library"},
		{filepath.Dir(libPath), http.StatusBadRequest, "contains the library"},
	} {
		w := do(r, "GET", "/api/v1/import/scan?path="+tc.path, token, nil)
		if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.msg) {
			t.Errorf("scan %q = %d %s, want %d mentioning %q", tc.path, w.Code, w.Body.String(), tc.code, tc.msg)
		}
	}

	page := decodeJSON[map[string]any](t, r, "GET", "/api/v1/import/scan?path="+root, token, nil)
	groups, _ := page["groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("groups = %v", page["groups"])
	}
	if g := groups[0].(map[string]any); g["artist"] != "Band" || g["album"] != "Album" || g["album_from"] != "folder" || len(g["files"].([]any)) != 1 {
		t.Errorf("group = %v", g)
	}
}

// A Lidarr-managed library cannot take an import: Lidarr decides identity there and
// tracks its files by path.
func TestImportRefusesALidarrLibrary(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	root, file := importSource(t)

	lidarr := models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true}
	if err := api.DB.Create(&lidarr).Error; err != nil {
		t.Fatal(err)
	}
	library := models.Library{Name: "Lidarr music", Path: t.TempDir(), Enabled: true, ManagerID: &lidarr.ID}
	if err := api.DB.Create(&library).Error; err != nil {
		t.Fatal(err)
	}
	w := do(r, "POST", "/api/v1/import/preview", token, map[string]any{
		"library_id": library.ID, "mb_release_id": "rel-1", "root": root, "sources": []string{file},
	})
	if w.Code != http.StatusConflict {
		t.Errorf("preview into a Lidarr library = %d, want 409: %s", w.Code, w.Body.String())
	}
}

// Preview proposes a mapping and plans it; import refuses a plan with a conflict the
// reviewer did not agree to, and rejects sources outside the root.
func TestImportPreviewAndRefusals(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	root, file := importSource(t)
	api.Meta = &fakeMeta{getRelease: func(string) (models.MusicBrainzReleaseResponse, error) {
		credit := []models.ArtistCredit{{Name: "Band", Artist: models.Artist{ID: "a", Name: "Band"}}}
		return models.MusicBrainzReleaseResponse{
			ID: "rel-1", Title: "Album", Date: "2001", ArtistCredit: credit,
			ReleaseGroup: models.ReleaseGroup{ID: "rg", Title: "Album", FirstReleaseDate: "2001", ArtistCredit: credit},
			Media:        []models.MusicBrainzMedia{{Position: 1, Tracks: []models.Track{{ID: "trk-1", Title: "Song", Position: 1, Number: "1"}}}},
		}, nil
	}}
	library := models.Library{Name: "Music", Path: t.TempDir(), Enabled: true}
	if err := api.DB.Create(&library).Error; err != nil {
		t.Fatal(err)
	}

	body := map[string]any{"library_id": library.ID, "mb_release_id": "rel-1", "root": root, "sources": []string{file}}
	preview := decodeJSON[map[string]any](t, r, "POST", "/api/v1/import/preview", token, body)
	rows, _ := preview["rows"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["target_rel"] != "Band/Album (2001)/01 Song.flac" || preview["problem"] != "" {
		t.Fatalf("preview = %v", preview)
	}

	// Something now sits at the target: the import needs consent to replace it.
	existing := filepath.Join(library.Path, "Band", "Album (2001)", "01 Song.flac")
	if err := os.MkdirAll(filepath.Dir(existing), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	importBody := map[string]any{
		"library_id": library.ID, "mb_release_id": "rel-1", "root": root, "mode": "move",
		"rows": []map[string]any{{"source": file, "mb_release_track_id": "trk-1"}},
	}
	if w := do(r, "POST", "/api/v1/import", token, importBody); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Replace") {
		t.Errorf("import over an unconsented conflict = %d %s, want 400", w.Code, w.Body.String())
	}

	importBody["mode"] = "hardlink"
	if w := do(r, "POST", "/api/v1/import", token, importBody); w.Code != http.StatusBadRequest {
		t.Errorf("an unknown mode = %d, want 400", w.Code)
	}

	twice := map[string]any{
		"library_id": library.ID, "mb_release_id": "rel-1", "root": root, "mode": "move",
		"rows": []map[string]any{{"source": file, "mb_release_track_id": "trk-1"}, {"source": file, "mb_release_track_id": "trk-1"}},
	}
	if w := do(r, "POST", "/api/v1/import", token, twice); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "twice") {
		t.Errorf("a file listed twice = %d %s, want 400", w.Code, w.Body.String())
	}

	outside := map[string]any{"library_id": library.ID, "mb_release_id": "rel-1", "root": root, "sources": []string{filepath.Join(t.TempDir(), "x.flac")}}
	if w := do(r, "POST", "/api/v1/import/preview", token, outside); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "not under") {
		t.Errorf("a source outside the root = %d %s, want 400", w.Code, w.Body.String())
	}
}

// Folder suggestions complete the typed path and mark the ones a scan would refuse.
func TestImportFolderSuggestions(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	parent := t.TempDir()
	for _, dir := range []string{"downloads", "music"} {
		if err := os.MkdirAll(filepath.Join(parent, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := api.DB.Create(&models.Library{Name: "Music", Path: filepath.Join(parent, "music"), Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}

	page := decodeJSON[map[string]any](t, r, "GET", "/api/v1/import/folders?path="+parent+string(filepath.Separator), token, nil)
	folders, _ := page["folders"].([]any)
	if len(folders) != 2 || page["separator"] != string(filepath.Separator) {
		t.Fatalf("page = %v", page)
	}
	byName := map[string]map[string]any{}
	for _, f := range folders {
		m := f.(map[string]any)
		byName[filepath.Base(m["path"].(string))] = m
	}
	if byName["music"]["library"] != "Music" || byName["downloads"]["library"] != nil {
		t.Errorf("library marks = %v", folders)
	}

	empty := decodeJSON[map[string]any](t, r, "GET", "/api/v1/import/folders?path=relative", token, nil)
	if got, _ := empty["folders"].([]any); len(got) != 0 {
		t.Errorf("a relative path completes to nothing, got %v", got)
	}
}

// Browsing an artist's albums works for any MusicBrainz artist, not only ones the
// collection holds — an import is how a new artist arrives.
func TestArtistReleaseGroupsForAnyArtist(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	api.Meta = &fakeMeta{getArtistRGs: func(id string) ([]models.MusicBrainzArtistReleaseGroup, bool, error) {
		return []models.MusicBrainzArtistReleaseGroup{{ID: "rg-1", Title: "Spiderland", PrimaryType: "Album", FirstReleaseDate: "1991-03-27"}}, true, nil
	}}
	got := decodeJSON[[]map[string]any](t, r, "GET", "/api/v1/search/artists/unknown-artist/release-groups", token, nil)
	if len(got) != 1 || got[0]["title"] != "Spiderland" {
		t.Errorf("release groups = %v", got)
	}
}

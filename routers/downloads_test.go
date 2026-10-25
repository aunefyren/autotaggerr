package routers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// qbittorrentStub answers the four qBittorrent requests a poll and a test make, for
// one finished torrent whose files are under savePath. Authentication is bypassed, as
// qBittorrent allows for a trusted host.
func qbittorrentStub(t *testing.T, savePath string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/app/version":
			_, _ = w.Write([]byte("v5.0.3"))
		case "/api/v2/torrents/info":
			if r.URL.Query().Get("category") != "music" {
				_, _ = w.Write([]byte("[]"))
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"hash": "abc", "name": "Band - Album", "category": "music", "state": "stalledUP", "progress": 1,
				"save_path": savePath, "content_path": filepath.Join(savePath, "Band - Album"), "completion_on": 1,
			}})
		case "/api/v2/torrents/files":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"name": "Band - Album/01 Song.flac", "size": 1, "progress": 1, "priority": 1}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestDownloadRoutesRequireAdmin(t *testing.T) {
	r, api := setupAPI(t)
	viewer := userToken(t, r, api.DB)
	for _, call := range []struct{ method, path string }{
		{"GET", "/api/v1/download-clients"},
		{"POST", "/api/v1/download-clients"},
		{"GET", "/api/v1/import/downloads"},
		{"POST", "/api/v1/import/downloads/poll"},
	} {
		if w := do(r, call.method, call.path, viewer, map[string]any{}); w.Code != http.StatusForbidden {
			t.Errorf("%s %s as a non-admin = %d, want 403", call.method, call.path, w.Code)
		}
	}
}

func TestDownloadClientValidation(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	library := models.Library{Name: "Music", Path: t.TempDir(), Enabled: true}
	api.DB.Create(&library)
	lidarr := models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true}
	api.DB.Create(&lidarr)
	lidarrLibrary := models.Library{Name: "Lidarr music", Path: t.TempDir(), Enabled: true, ManagerID: &lidarr.ID}
	api.DB.Create(&lidarrLibrary)

	base := func() map[string]any {
		return map[string]any{"name": "qBittorrent", "type": "qbittorrent", "base_url": "http://qbit:8080",
			"categories": []map[string]any{{"category": "music", "library_id": library.ID}}}
	}
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		msg    string
	}{
		{"bad url", func(b map[string]any) { b["base_url"] = "qbit:8080" }, "full http"},
		{"bad type", func(b map[string]any) { b["type"] = "utorrent" }, "unsupported"},
		{"no category name", func(b map[string]any) {
			b["categories"] = []map[string]any{{"category": " ", "library_id": library.ID}}
		}, "needs a name"},
		{"lidarr library", func(b map[string]any) {
			b["categories"] = []map[string]any{{"category": "music", "library_id": lidarrLibrary.ID}}
		}, "Lidarr manages"},
		{"relative mapping", func(b map[string]any) {
			b["path_mappings"] = []map[string]any{{"remote": "/downloads", "local": "mnt/x"}}
		}, "not a full path"},
	} {
		body := base()
		tc.change(body)
		if w := do(r, "POST", "/api/v1/download-clients", token, body); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tc.msg) {
			t.Errorf("%s = %d %s, want 400 mentioning %q", tc.name, w.Code, w.Body.String(), tc.msg)
		}
	}

	body := base()
	body["username"], body["password"] = "admin", "secret"
	body["categories"] = []map[string]any{{"category": "music", "library_id": library.ID, "auto_import": true}}
	w := do(r, "POST", "/api/v1/download-clients", token, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "secret") || !strings.Contains(w.Body.String(), `"password_set":true`) {
		t.Errorf("created client exposes or loses the password: %s", w.Body.String())
	}
	if w := do(r, "POST", "/api/v1/download-clients", token, base()); w.Code != http.StatusConflict {
		t.Errorf("a second client of the same name = %d, want 409", w.Code)
	}

	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	// An edit without categories or a password keeps both.
	if w := do(r, "PUT", "/api/v1/download-clients/"+created.ID, token, map[string]any{"name": "Seedbox"}); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"password_set":true`) || !strings.Contains(w.Body.String(), `"category":"music"`) ||
		!strings.Contains(w.Body.String(), `"auto_import":true`) {
		t.Errorf("rename = %d %s", w.Code, w.Body.String())
	}
	if w := do(r, "DELETE", "/api/v1/download-clients/"+created.ID, token, nil); w.Code != http.StatusNoContent {
		t.Errorf("delete = %d", w.Code)
	}
	var categories int64
	api.DB.Model(&models.DownloadClientCategory{}).Count(&categories)
	if categories != 0 {
		t.Errorf("%d categories outlived their client", categories)
	}
}

// TestDownloadsListScanDismiss: a client's finished torrent reaches the Downloads list
// through Check now, opens as an album scan, and can be set aside and brought back.
func TestDownloadsListScanDismiss(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	library := models.Library{Name: "Music", Path: t.TempDir(), Enabled: true}
	api.DB.Create(&library)
	save := t.TempDir()
	file := filepath.Join(save, "Band - Album", "01 Song.flac")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := qbittorrentStub(t, save)

	w := do(r, "POST", "/api/v1/download-clients", token, map[string]any{
		"name": "qBittorrent", "type": "qbittorrent", "base_url": server.URL,
		"categories": []map[string]any{{"category": "music", "library_id": library.ID}},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	var client struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &client)
	test := decodeJSON[map[string]any](t, r, "POST", "/api/v1/download-clients/"+client.ID+"/test", token, nil)
	if test["healthy"] != true {
		t.Errorf("test = %v", test)
	}

	type listing struct {
		Clients []struct {
			LastPollAt *string `json:"last_poll_at"`
		} `json:"clients"`
		Downloads []struct {
			ID     string   `json:"id"`
			State  string   `json:"state"`
			Files  []string `json:"files"`
			Albums int      `json:"albums"`
			Client string   `json:"client_name"`
		} `json:"downloads"`
	}
	list := decodeJSON[listing](t, r, "POST", "/api/v1/import/downloads/poll", token, nil)
	if len(list.Downloads) != 1 || list.Downloads[0].State != models.DownloadStateReady || list.Downloads[0].Albums != 1 || list.Downloads[0].Client != "qBittorrent" {
		t.Fatalf("downloads = %+v", list.Downloads)
	}
	if len(list.Clients) != 1 || list.Clients[0].LastPollAt == nil {
		t.Errorf("clients = %+v", list.Clients)
	}
	id := list.Downloads[0].ID

	scan := decodeJSON[map[string]any](t, r, "GET", "/api/v1/import/downloads/"+id+"/scan", token, nil)
	groups, _ := scan["groups"].([]any)
	if scan["root"] != filepath.Join(save, "Band - Album") || len(groups) != 1 || scan["seeding"] != true {
		t.Fatalf("scan = %v", scan)
	}
	if g := groups[0].(map[string]any); g["artist"] != "Band" || g["album"] != "Album" {
		t.Errorf("group = %v", g)
	}

	if w := do(r, "POST", "/api/v1/import/downloads/"+id+"/dismiss", token, nil); w.Code != http.StatusNoContent {
		t.Fatalf("dismiss = %d %s", w.Code, w.Body.String())
	}
	if w := do(r, "GET", "/api/v1/import/downloads/"+id+"/scan", token, nil); w.Code != http.StatusConflict {
		t.Errorf("scan of a dismissed download = %d, want 409", w.Code)
	}
	// Polled again, a dismissed download stays dismissed.
	list = decodeJSON[listing](t, r, "POST", "/api/v1/import/downloads/poll", token, nil)
	if list.Downloads[0].State != models.DownloadStateDismissed {
		t.Errorf("after a poll = %s", list.Downloads[0].State)
	}
	if w := do(r, "POST", "/api/v1/import/downloads/"+id+"/restore", token, nil); w.Code != http.StatusNoContent {
		t.Fatalf("restore = %d %s", w.Code, w.Body.String())
	}
	list = decodeJSON[listing](t, r, "GET", "/api/v1/import/downloads", token, nil)
	if list.Downloads[0].State != models.DownloadStateReady {
		t.Errorf("after restore = %s", list.Downloads[0].State)
	}
}

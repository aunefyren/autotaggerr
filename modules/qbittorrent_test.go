package modules

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// fakeQBittorrent is a qBittorrent Web API with the behaviour the client has to cope
// with: a session cookie from a form login, a Referer check, and sessions that can be
// expired from the test.
type fakeQBittorrent struct {
	t        *testing.T
	server   *httptest.Server
	username string
	password string
	// noAuth is qBittorrent's "bypass authentication for clients on localhost".
	noAuth bool
	// v52 answers a login the way qBittorrent 5.2 does: 204 with no body on
	// success, 401 on a wrong password.
	v52 bool

	mu       sync.Mutex
	sessions map[string]bool
	logins   int
	torrents map[string][]map[string]any // by category
	files    map[string][]map[string]any // by hash
	tagged   []string                    // "hash:tag"
	// categories qBittorrent has; moved records setCategory as "hash:category".
	categories map[string]bool
	moved      []string
	// limits answers torrents/info?hashes=…; deleted records torrents/delete.
	limits  []map[string]any
	deleted []string
}

func newFakeQBittorrent(t *testing.T) *fakeQBittorrent {
	f := &fakeQBittorrent{t: t, username: "admin", password: "secret", sessions: map[string]bool{},
		torrents: map[string][]map[string]any{}, files: map[string][]map[string]any{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeQBittorrent) expireSessions() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions = map[string]bool{}
}

func (f *fakeQBittorrent) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// CSRF protection: a request whose Referer is not this host is refused.
	if r.Header.Get("Referer") != f.server.URL {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.URL.Path == "/api/v2/auth/login" {
		f.logins++
		_ = r.ParseForm()
		if r.Form.Get("username") != f.username || r.Form.Get("password") != f.password {
			if f.v52 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte("Fails."))
			return
		}
		sid := "sid" + strings.Repeat("x", f.logins)
		f.sessions[sid] = true
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: sid, Path: "/"})
		if f.v52 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte("Ok."))
		return
	}
	if !f.noAuth {
		cookie, err := r.Cookie("SID")
		if err != nil || !f.sessions[cookie.Value] {
			w.WriteHeader(http.StatusForbidden)
			return
		}
	}
	switch r.URL.Path {
	case "/api/v2/app/version":
		_, _ = w.Write([]byte("v5.0.3"))
	case "/api/v2/torrents/info":
		if r.URL.Query().Get("hashes") != "" {
			_ = json.NewEncoder(w).Encode(f.limits)
			return
		}
		if r.URL.Query().Get("filter") != "completed" {
			f.t.Errorf("torrents/info filter = %q, want completed", r.URL.Query().Get("filter"))
		}
		_ = json.NewEncoder(w).Encode(f.torrents[r.URL.Query().Get("category")])
	case "/api/v2/torrents/files":
		_ = json.NewEncoder(w).Encode(f.files[r.URL.Query().Get("hash")])
	case "/api/v2/torrents/addTags":
		_ = r.ParseForm()
		f.tagged = append(f.tagged, r.Form.Get("hashes")+":"+r.Form.Get("tags"))
	case "/api/v2/torrents/createCategory":
		_ = r.ParseForm()
		if f.categories == nil {
			f.categories = map[string]bool{}
		}
		if f.categories[r.Form.Get("category")] {
			w.WriteHeader(http.StatusConflict)
			return
		}
		f.categories[r.Form.Get("category")] = true
	case "/api/v2/torrents/setCategory":
		_ = r.ParseForm()
		if !f.categories[r.Form.Get("category")] {
			w.WriteHeader(http.StatusConflict)
			return
		}
		f.moved = append(f.moved, r.Form.Get("hashes")+":"+r.Form.Get("category"))
	case "/api/v2/torrents/delete":
		_ = r.ParseForm()
		if r.Form.Get("deleteFiles") != "true" {
			f.t.Errorf("delete without its files: %v", r.Form)
		}
		f.deleted = append(f.deleted, r.Form.Get("hashes"))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestQBittorrentHealthCheckLogsInWithTheReferer(t *testing.T) {
	f := newFakeQBittorrent(t)
	ok, err := NewQBittorrentClient(f.server.URL+"/", "admin", "secret").HealthCheck()
	if !ok || err != nil {
		t.Fatalf("HealthCheck = %v, %v", ok, err)
	}
	if f.logins != 1 {
		t.Errorf("logins = %d, want 1", f.logins)
	}
}

// qBittorrent 5.2 answers a login with 204 and no body, and a wrong password with 401.
func TestQBittorrent52Login(t *testing.T) {
	f := newFakeQBittorrent(t)
	f.v52 = true
	if ok, err := NewQBittorrentClient(f.server.URL, "admin", "secret").HealthCheck(); !ok || err != nil {
		t.Errorf("a 5.2 login = %v, %v", ok, err)
	}
	_, err := NewQBittorrentClient(f.server.URL, "admin", "wrong").HealthCheck()
	if err == nil || !strings.Contains(err.Error(), "rejected the username or password") {
		t.Errorf("a 5.2 wrong password = %v", err)
	}
}

// A login answered by something other than qBittorrent — an authentication proxy's
// page — says so, rather than blaming the password.
func TestQBittorrentLoginAnsweredByAWebPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<!DOCTYPE html><title>Sign in</title>"))
	}))
	t.Cleanup(server.Close)
	_, err := NewQBittorrentClient(server.URL, "admin", "secret").HealthCheck()
	if err == nil || !strings.Contains(err.Error(), "web page") {
		t.Errorf("a proxy's login page = %v", err)
	}
}

func TestQBittorrentRejectedLoginIsReportedNotRetried(t *testing.T) {
	f := newFakeQBittorrent(t)
	ok, err := NewQBittorrentClient(f.server.URL, "admin", "wrong").HealthCheck()
	if ok || err == nil || !strings.Contains(err.Error(), "rejected the username or password") {
		t.Fatalf("HealthCheck = %v, %v; want a rejected login", ok, err)
	}
	if f.logins != 1 {
		t.Errorf("logins = %d, want exactly 1 — a retried bad login gets the address banned", f.logins)
	}
}

// A session ends when qBittorrent restarts; the client logs in again once rather than
// failing the poll.
func TestQBittorrentLogsInAgainWhenTheSessionExpires(t *testing.T) {
	f := newFakeQBittorrent(t)
	client := NewQBittorrentClient(f.server.URL, "admin", "secret")
	if _, err := client.HealthCheck(); err != nil {
		t.Fatal(err)
	}
	f.expireSessions()
	if _, err := client.HealthCheck(); err != nil {
		t.Fatalf("after the session expired: %v", err)
	}
	if f.logins != 2 {
		t.Errorf("logins = %d, want 2", f.logins)
	}
}

func TestQBittorrentWithoutCredentials(t *testing.T) {
	f := newFakeQBittorrent(t)
	if _, err := NewQBittorrentClient(f.server.URL, "", "").HealthCheck(); err == nil || !strings.Contains(err.Error(), "skip authentication") {
		t.Errorf("no credentials against an authenticating qBittorrent = %v", err)
	}
	f.noAuth = true
	if ok, err := NewQBittorrentClient(f.server.URL, "", "").HealthCheck(); !ok || err != nil {
		t.Errorf("no credentials against the auth bypass = %v, %v", ok, err)
	}
	if f.logins != 0 {
		t.Errorf("logins = %d, want none without a username", f.logins)
	}
}

// Finished keeps the settled torrents of exactly the categories asked for, and reads
// the imported tag.
func TestQBittorrentFinished(t *testing.T) {
	f := newFakeQBittorrent(t)
	torrent := func(hash, category, state string, progress float64, tags string) map[string]any {
		return map[string]any{
			"hash": hash, "name": "Band - Album " + hash, "category": category, "tags": tags, "state": state,
			"progress": progress, "amount_left": 0, "save_path": "/downloads/music",
			"content_path": "/downloads/music/Band - Album " + hash, "size": 1000, "completion_on": 1700000000,
		}
	}
	f.torrents["music"] = []map[string]any{
		torrent("a", "music", "stalledUP", 1, ""),
		torrent("b", "music", "checkingUP", 1, ""),                       // being checked
		torrent("c", "music", "moving", 1, ""),                           // being moved
		torrent("d", "music/flac", "uploading", 1, ""),                   // a subcategory
		torrent("e", "music", "stoppedUP", 1, "x, autotaggerr-imported"), // qBittorrent 5's paused
		torrent("f", "music", "uploading", 0.5, ""),                      // not finished
	}
	f.torrents["lidarr"] = []map[string]any{torrent("z", "lidarr", "uploading", 1, "")}

	got, err := NewQBittorrentClient(f.server.URL, "admin", "secret").Finished([]string{"music"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, d := range got {
		ids = append(ids, d.ID)
	}
	if !reflect.DeepEqual(ids, []string{"a", "e"}) {
		t.Fatalf("finished = %v, want [a e]", ids)
	}
	if got[0].Imported || !got[1].Imported {
		t.Errorf("imported = %v, %v; want false, true", got[0].Imported, got[1].Imported)
	}
	if !got[0].Seeding || got[0].FilesRoot != "/downloads/music" || got[0].Path != "/downloads/music/Band - Album a" || got[0].CompletedAt.Unix() != 1700000000 {
		t.Errorf("download = %+v", got[0])
	}
}

func TestQBittorrentFilesAndTag(t *testing.T) {
	f := newFakeQBittorrent(t)
	f.files["a"] = []map[string]any{
		{"name": "Album/01 One.flac", "size": 10, "progress": 1, "priority": 1},
		{"name": "Album/02 Two.flac", "size": 10, "progress": 1, "priority": 0}, // deselected
		{"name": "Album/03 Three.flac", "size": 10, "progress": 0.9, "priority": 1},
	}
	client := NewQBittorrentClient(f.server.URL, "admin", "secret")
	files, err := client.Files(Download{ID: "a", FilesRoot: "/downloads/music/"})
	if err != nil {
		t.Fatal(err)
	}
	want := []DownloadFile{
		{Path: "/downloads/music/Album/01 One.flac", Size: 10, Complete: true},
		{Path: "/downloads/music/Album/02 Two.flac", Size: 10},
		{Path: "/downloads/music/Album/03 Three.flac", Size: 10},
	}
	if !reflect.DeepEqual(files, want) {
		t.Errorf("files = %+v", files)
	}

	// A Windows-hosted client: its own separator.
	files, _ = client.Files(Download{ID: "a", FilesRoot: `D:\Torrents\`})
	if files[0].Path != `D:\Torrents\Album\01 One.flac` {
		t.Errorf("windows path = %q", files[0].Path)
	}

	if err := client.MarkImported("a", "music", ""); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.tagged, []string{"a:" + models.DownloadClientImportedTag}) {
		t.Errorf("tagged = %v", f.tagged)
	}
}

func TestNewDownloadClient(t *testing.T) {
	if _, err := NewDownloadClient(models.DownloadClient{Name: "q", Type: models.DownloadClientTypeQBittorrent}); err == nil {
		t.Error("a client without a base URL was built")
	}
	if _, err := NewDownloadClient(models.DownloadClient{Name: "q", Type: "deluge", BaseURL: "http://x"}); err == nil {
		t.Error("an unsupported type was built")
	}
	if _, err := NewDownloadClient(models.DownloadClient{Name: "s", Type: models.DownloadClientTypeSABnzbd, BaseURL: "http://x"}); err == nil {
		t.Error("SABnzbd without an API key was built")
	}
	for _, kind := range []string{models.DownloadClientTypeTransmission, models.DownloadClientTypeSABnzbd} {
		if c, err := NewDownloadClient(models.DownloadClient{Name: "c", Type: kind, BaseURL: "http://x", Password: "key"}); err != nil || c == nil {
			t.Errorf("%s = %v, %v", kind, c, err)
		}
	}
	if c, err := NewDownloadClient(models.DownloadClient{Name: "q", Type: models.DownloadClientTypeQBittorrent, BaseURL: "http://x"}); err != nil || c == nil {
		t.Errorf("qbittorrent = %v, %v", c, err)
	}
}

func TestMapRemotePath(t *testing.T) {
	local := filepath.FromSlash("/mnt/torrents")
	mappings := []models.PathMapping{
		{Remote: "/downloads", Local: filepath.FromSlash("/mnt/all")},
		{Remote: "/downloads/music/", Local: local},
		{Remote: `D:\Torrents`, Local: filepath.FromSlash("/mnt/win")},
	}
	for _, tc := range []struct{ remote, want string }{
		{"/downloads/music/Band - Album", filepath.Join(local, "Band - Album")}, // the longest prefix wins
		{"/downloads/music", local}, // the prefix itself
		{"/downloads/other/x.flac", filepath.FromSlash("/mnt/all/other/x.flac")},    // the shorter one
		{"/downloads2/x.flac", filepath.FromSlash("/downloads2/x.flac")},            // not a whole segment
		{`d:\Torrents\Album\01.flac`, filepath.FromSlash("/mnt/win/Album/01.flac")}, // drive letter case
		{`D:\torrents\Album`, filepath.FromSlash("D:/torrents/Album")},              // the rest is exact
		{"/elsewhere/Album", filepath.FromSlash("/elsewhere/Album")},                // unmapped
	} {
		if got := MapRemotePath(tc.remote, mappings); got != tc.want {
			t.Errorf("MapRemotePath(%q) = %q, want %q", tc.remote, got, tc.want)
		}
	}
}

// A single-file download's file sits in the category's folder, which names nothing;
// the download's own name is read instead.
func TestGroupImportFilesReadsTheGivenNameAboveTheRoot(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "music", "track.mp3")
	groups := GroupImportFiles(file, []string{file}, "Band - Single (2020) [WEB]")
	if len(groups) != 1 {
		t.Fatalf("groups = %+v", groups)
	}
	if g := groups[0]; g.Artist != "Band" || g.Album != "Single" || g.Year != 2020 || g.AlbumFrom != HintFromFolder {
		t.Errorf("group = %+v", g)
	}

	// Inside the root, the folder's own name still wins.
	root := filepath.Join(dir, "Other - Album")
	inside := filepath.Join(root, "01.mp3")
	if g := GroupImportFiles(root, []string{inside}, "Ignored - Name")[0]; g.Artist != "Other" || g.Album != "Album" {
		t.Errorf("inside the root = %+v", g)
	}
}

func TestImportGroupFolders(t *testing.T) {
	root := filepath.FromSlash("/dl/Box")
	files := []string{
		filepath.FromSlash("/dl/Box/CD 01/01.flac"),
		filepath.FromSlash("/dl/Box/CD 02/01.flac"),
		filepath.FromSlash("/dl/Box/Bonus/01.flac"),
	}
	want := []string{root, filepath.Join(root, "Bonus")}
	if got := ImportGroupFolders(root, files); !reflect.DeepEqual(got, want) {
		t.Errorf("folders = %v, want %v", got, want)
	}
}

// The post-import category is created when qBittorrent lacks it — and a 409 to
// creating one it already has is not a failure — then set.
func TestQBittorrentMarkImportedMovesToACategory(t *testing.T) {
	f := newFakeQBittorrent(t)
	client := NewQBittorrentClient(f.server.URL, "admin", "secret")
	for _, hash := range []string{"a", "b"} {
		if err := client.MarkImported(hash, "music", "music-done"); err != nil {
			t.Fatalf("MarkImported(%s): %v", hash, err)
		}
	}
	if !reflect.DeepEqual(f.moved, []string{"a:music-done", "b:music-done"}) {
		t.Errorf("moved = %v", f.moved)
	}
}

// Stopped is not done: a torrent paused by hand is stopped too. Done is stopped with a
// share limit set and reached; no limits is never done.
func TestQBittorrentSeedingDone(t *testing.T) {
	f := newFakeQBittorrent(t)
	f.limits = []map[string]any{
		{"hash": "ratio", "state": "stoppedUP", "ratio": 2.0, "max_ratio": 2.0, "seeding_time": 10, "max_seeding_time": -1},
		{"hash": "time", "state": "pausedUP", "ratio": 0.1, "max_ratio": -1, "seeding_time": 3600, "max_seeding_time": 60},
		{"hash": "manual", "state": "stoppedUP", "ratio": 0.5, "max_ratio": 2.0, "seeding_time": 10, "max_seeding_time": -1},
		{"hash": "nolimit", "state": "stoppedUP", "ratio": 9.0, "max_ratio": -1, "seeding_time": 99999, "max_seeding_time": -1},
		{"hash": "seeding", "state": "uploading", "ratio": 3.0, "max_ratio": 2.0, "seeding_time": 10, "max_seeding_time": -1},
	}
	client := NewQBittorrentClient(f.server.URL, "admin", "secret")
	done, err := client.SeedingDone([]string{"ratio", "time", "manual", "nolimit", "seeding", "gone"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"ratio": true, "time": true, "manual": false, "nolimit": false, "seeding": false}
	if !reflect.DeepEqual(done, want) {
		t.Errorf("done = %v, want %v", done, want)
	}
	if err := client.Remove("ratio"); err != nil || !reflect.DeepEqual(f.deleted, []string{"ratio"}) {
		t.Errorf("Remove = %v, deleted %v", err, f.deleted)
	}
}

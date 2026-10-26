package modules

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// fakeTransmission is a Transmission RPC with the handshake the client has to cope
// with: basic auth, and a 409 naming the session id to use until it is sent.
type fakeTransmission struct {
	t        *testing.T
	server   *httptest.Server
	mu       sync.Mutex
	torrents []map[string]any
	set      []map[string]any
	removed  []map[string]any
}

func newFakeTransmission(t *testing.T) *fakeTransmission {
	f := &fakeTransmission{t: t}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if user, pass, ok := r.BasicAuth(); !ok || user != "admin" || pass != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("X-Transmission-Session-Id") != "sess-1" {
			w.Header().Set("X-Transmission-Session-Id", "sess-1")
			w.WriteHeader(http.StatusConflict)
			return
		}
		var req struct {
			Method    string         `json:"method"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		reply := map[string]any{"result": "success", "arguments": map[string]any{}}
		switch req.Method {
		case "session-get":
			reply["arguments"] = map[string]any{"version": "4.0.6"}
		case "torrent-get":
			out := f.torrents
			if ids, ok := req.Arguments["ids"].([]any); ok {
				out = nil
				for _, t := range f.torrents {
					for _, id := range ids {
						if t["hashString"] == id {
							out = append(out, t)
						}
					}
				}
			}
			reply["arguments"] = map[string]any{"torrents": out}
		case "torrent-set":
			f.set = append(f.set, req.Arguments)
		case "torrent-remove":
			f.removed = append(f.removed, req.Arguments)
		default:
			reply["result"] = "method name not recognized"
		}
		_ = json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func TestTransmissionReadsLabelledFinishedTorrents(t *testing.T) {
	f := newFakeTransmission(t)
	f.torrents = []map[string]any{
		{"hashString": "a", "name": "Album", "labels": []string{"music"}, "downloadDir": "/downloads/music",
			"percentDone": 1.0, "leftUntilDone": 0, "status": 6, "doneDate": 1700000000, "totalSize": 100,
			"files":     []map[string]any{{"name": "Album/01.flac", "length": 10, "bytesCompleted": 10}, {"name": "Album/02.flac", "length": 10, "bytesCompleted": 4}, {"name": "Album/03.flac", "length": 10, "bytesCompleted": 10}},
			"fileStats": []map[string]any{{"wanted": true}, {"wanted": true}, {"wanted": false}}},
		{"hashString": "b", "name": "Downloading", "labels": []string{"music"}, "percentDone": 0.5, "status": 4},
		{"hashString": "c", "name": "Checking", "labels": []string{"music"}, "percentDone": 1.0, "status": 2},
		{"hashString": "d", "name": "Other", "labels": []string{"films"}, "percentDone": 1.0, "status": 6},
		{"hashString": "e", "name": "Done before", "labels": []string{"music", models.DownloadClientImportedTag}, "downloadDir": "/downloads/music", "percentDone": 1.0, "status": 0},
	}
	client := NewTransmissionClient(f.server.URL+"/", "admin", "secret")
	if ok, err := client.HealthCheck(); !ok || err != nil {
		t.Fatalf("HealthCheck = %v, %v", ok, err)
	}
	downloads, err := client.Finished([]string{"music"})
	if err != nil {
		t.Fatal(err)
	}
	if len(downloads) != 2 || downloads[0].ID != "a" || downloads[0].Path != "/downloads/music/Album" || !downloads[0].Seeding || downloads[0].CompletedAt.IsZero() {
		t.Fatalf("downloads = %+v, want a and e", downloads)
	}
	if downloads[0].Imported || !downloads[1].Imported {
		t.Errorf("imported marks = %v %v", downloads[0].Imported, downloads[1].Imported)
	}
	files, err := client.Files(downloads[0])
	if err != nil {
		t.Fatal(err)
	}
	complete := []bool{}
	for _, f := range files {
		complete = append(complete, f.Complete)
	}
	if files[0].Path != "/downloads/music/Album/01.flac" || !reflect.DeepEqual(complete, []bool{true, false, false}) {
		t.Errorf("files = %+v", files)
	}
}

// The imported mark is a label; a post-import category swaps the watched label for
// another, keeping the rest.
func TestTransmissionMarkImportedSwapsTheLabel(t *testing.T) {
	f := newFakeTransmission(t)
	f.torrents = []map[string]any{{"hashString": "a", "labels": []string{"music", "mine"}}}
	client := NewTransmissionClient(f.server.URL, "admin", "secret")
	if err := client.MarkImported("a", "music", "music-done"); err != nil {
		t.Fatal(err)
	}
	labels := f.set[0]["labels"]
	if !reflect.DeepEqual(labels, []any{"mine", models.DownloadClientImportedTag, "music-done"}) {
		t.Errorf("labels = %v", labels)
	}
	if err := client.MarkImported("a", "music", ""); err != nil {
		t.Fatal(err)
	}
	if got := f.set[1]["labels"]; !reflect.DeepEqual(got, []any{"music", "mine", models.DownloadClientImportedTag}) {
		t.Errorf("labels without a category = %v", got)
	}
}

// isFinished is set only by a seed limit; a stopped torrent without it was stopped by hand.
func TestTransmissionSeedingDone(t *testing.T) {
	f := newFakeTransmission(t)
	f.torrents = []map[string]any{
		{"hashString": "limit", "status": 0, "isFinished": true},
		{"hashString": "manual", "status": 0, "isFinished": false},
		{"hashString": "seeding", "status": 6, "isFinished": false},
	}
	client := NewTransmissionClient(f.server.URL, "admin", "secret")
	done, err := client.SeedingDone([]string{"limit", "manual", "seeding"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(done, map[string]bool{"limit": true, "manual": false, "seeding": false}) {
		t.Errorf("done = %v", done)
	}
	if err := client.Remove("limit"); err != nil || len(f.removed) != 1 || f.removed[0]["delete-local-data"] != true {
		t.Errorf("Remove = %v, %v", err, f.removed)
	}
}

func TestTransmissionReportsAuthAndAddressProblems(t *testing.T) {
	f := newFakeTransmission(t)
	if _, err := NewTransmissionClient(f.server.URL, "admin", "wrong").HealthCheck(); err == nil {
		t.Error("a wrong password passed")
	}
	if _, err := NewTransmissionClient("http://127.0.0.1:1", "", "").HealthCheck(); err == nil {
		t.Error("an unreachable daemon passed")
	}
	if c := NewTransmissionClient("http://host:9091/custom/rpc", "", ""); c.rpcURL != "http://host:9091/custom/rpc" {
		t.Errorf("rpc url = %s", c.rpcURL)
	}
}

package modules

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newFakeSABnzbd(t *testing.T, slots []map[string]any) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("apikey") != "key" {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": false, "error": "API Key Incorrect"})
			return
		}
		if q.Get("mode") != "history" || q.Get("output") != "json" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		out := []map[string]any{}
		for _, s := range slots {
			if q.Get("category") == "" || s["category"] == q.Get("category") {
				out = append(out, s)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"history": map[string]any{"slots": out}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A completed job is offered from its final folder, to be read from disk; nothing
// seeds, so an import may move it, and there is nothing to mark.
func TestSABnzbdReadsCompletedHistory(t *testing.T) {
	srv := newFakeSABnzbd(t, []map[string]any{
		{"nzo_id": "n1", "name": "Album", "category": "music", "status": "Completed", "storage": "/complete/music/Album", "bytes": 100, "completed": 1700000000},
		{"nzo_id": "n2", "name": "Failed", "category": "music", "status": "Failed", "storage": "/complete/music/Failed"},
		{"nzo_id": "n3", "name": "Unpacking", "category": "music", "status": "Extracting", "storage": ""},
	})
	client := NewSABnzbdClient(srv.URL, "key")
	if ok, err := client.HealthCheck(); !ok || err != nil {
		t.Fatalf("HealthCheck = %v, %v", ok, err)
	}
	downloads, err := client.Finished([]string{"music"})
	if err != nil {
		t.Fatal(err)
	}
	if len(downloads) != 1 {
		t.Fatalf("downloads = %+v, want the completed one", downloads)
	}
	d := downloads[0]
	if d.ID != "n1" || d.Path != "/complete/music/Album" || d.Seeding || !d.FilesOnDisk || d.CompletedAt.IsZero() {
		t.Errorf("download = %+v", d)
	}
	if err := client.MarkImported("n1", "music", "done"); err != nil {
		t.Errorf("MarkImported = %v, want a no-op", err)
	}
	if _, err := NewSABnzbdClient(srv.URL, "wrong").HealthCheck(); err == nil || err.Error() != "SABnzbd rejected the API key" {
		t.Errorf("wrong key = %v", err)
	}
}

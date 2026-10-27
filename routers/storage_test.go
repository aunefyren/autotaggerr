package routers

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/storage"
)

// TestStorageSplitsByRole: everyone gets the music and the disks; only an admin gets
// the breakdown of config/ — paths and database internals — and the actions.
func TestStorageSplitsByRole(t *testing.T) {
	r, api := setupAPI(t)
	admin := loginToken(t, r)
	viewer := userToken(t, r, api.DB)
	dir := t.TempDir()
	artwork := filepath.Join(dir, "artwork")
	if err := os.MkdirAll(artwork, 0o755); err != nil {
		t.Fatal(err)
	}
	api.Storage = storage.NewMeasurer(api.DB, storage.Paths{ConfigDir: dir, ArtworkDir: artwork})

	full := decodeJSON[storage.Report](t, r, "GET", "/api/v1/storage?refresh=true", admin, nil)
	if full.App == nil || full.App.Dir != dir {
		t.Errorf("admin report = %+v, want config/'s breakdown", full.App)
	}
	w := do(r, "GET", "/api/v1/storage", viewer, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("viewer = %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"app":{`) || strings.Contains(w.Body.String(), dir) {
		t.Errorf("a non-admin was sent config/'s breakdown or its path: %s", w.Body.String())
	}

	for _, path := range []string{"compact", "checkpoint", "prune-activity", "artwork-leftovers"} {
		if w := do(r, "POST", "/api/v1/storage/"+path, viewer, nil); w.Code != http.StatusForbidden {
			t.Errorf("%s as a non-admin = %d, want 403", path, w.Code)
		}
	}
	out := decodeJSON[storage.Outcome](t, r, "POST", "/api/v1/storage/artwork-leftovers", admin, nil)
	if !strings.Contains(out.Message, "Removed 0") {
		t.Errorf("leftovers = %+v", out)
	}
	if w := do(r, "POST", "/api/v1/storage/checkpoint", admin, nil); w.Code != http.StatusOK {
		t.Errorf("checkpoint = %d: %s", w.Code, w.Body.String())
	}
}

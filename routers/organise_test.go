package routers

import (
	"net/http"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// TestOrganiseRoutes: the dry run answers for a native library and refuses a Lidarr
// one and an empty scope; a reviewed plan with a clash is refused before it is queued.
func TestOrganiseRoutes(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	native := models.Library{Name: "Native", Path: t.TempDir(), Enabled: true}
	lidarr := models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true}
	for _, row := range []any{&native, &lidarr} {
		if err := api.DB.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	managed := models.Library{Name: "Managed", Path: t.TempDir(), Enabled: true, ManagerID: &lidarr.ID}
	if err := api.DB.Create(&managed).Error; err != nil {
		t.Fatal(err)
	}

	if w := do(r, http.MethodPost, "/api/v1/organise/preview", token, map[string]any{"library_id": native.ID}); w.Code != http.StatusOK {
		t.Errorf("preview of a native library = %d: %s", w.Code, w.Body.String())
	}
	if w := do(r, http.MethodPost, "/api/v1/organise/preview", token, map[string]any{"library_id": managed.ID}); w.Code != http.StatusConflict {
		t.Errorf("preview of a Lidarr library = %d: %s", w.Code, w.Body.String())
	}
	if w := do(r, http.MethodPost, "/api/v1/organise/preview", token, map[string]any{}); w.Code != http.StatusBadRequest {
		t.Errorf("preview of nothing = %d", w.Code)
	}
	clash := map[string]any{"library_id": native.ID, "rows": []map[string]any{
		{"from": "/a.flac", "to": "/same.flac"}, {"from": "/b.flac", "to": "/same.flac"},
	}}
	if w := do(r, http.MethodPost, "/api/v1/organise", token, clash); w.Code != http.StatusConflict {
		t.Errorf("a plan with a clash = %d: %s", w.Code, w.Body.String())
	}
}

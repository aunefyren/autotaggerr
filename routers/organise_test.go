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

// TestMCPOrganise: the dry run is a read that refuses a Lidarr library; the commit
// plans again from the scope, so a conflict the assistant did not skip is refused.
func TestMCPOrganise(t *testing.T) {
	_, api := setupAPI(t)
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

	reader := mcpSession(t, api, mcpReader)
	preview := callMCP[mcpOrganisePreview](t, reader, "preview_organise", map[string]any{"library": "Native"})
	if preview.MovesTotal != 0 || preview.Moves == nil {
		t.Errorf("preview of an empty native library = %+v", preview)
	}
	expectMCPError(t, reader, "preview_organise", map[string]any{"library": "Managed"}, "Lidarr")
	expectMCPError(t, reader, "preview_organise", map[string]any{}, "one library, one artist or one album")

	// Nothing to move: the commit is refused rather than queuing an empty job.
	writer := mcpSession(t, api, mcpWriter)
	expectMCPError(t, writer, "organise_files", map[string]any{"library": "Native"}, "every move is skipped")
	expectMCPError(t, writer, "organise_files", map[string]any{"library": "Native", "skip": []string{"nope"}}, "not one")
}

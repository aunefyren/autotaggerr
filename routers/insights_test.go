package routers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aunefyren/autotaggerr/insights"
	"github.com/aunefyren/autotaggerr/models"
)

// TestInsightsRoutes: the duplicates and stats reads answer inline, a bad library is
// a 400, and a check with nothing to check is refused with the reason.
func TestInsightsRoutes(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	lib := models.Library{Name: "music", Path: "/music", Enabled: true}
	api.DB.Create(&lib)
	api.DB.Create(&models.CollectionReleaseGroup{MBID: "rg", ArtistMBID: "a"})
	api.DB.Create(&models.CollectionRelease{MBID: "rel", ReleaseGroupMBID: "rg"})
	for _, p := range []string{"/music/01.flac", "/music/01.mp3"} {
		api.DB.Create(&models.LibraryItem{LibraryID: lib.ID, Path: p, Size: 10, MBReleaseID: "rel", MBRecordingID: "rec", Status: models.LibraryItemStatusOK})
	}

	w := do(r, http.MethodGet, "/api/v1/insights/duplicates", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("duplicates: %d %s", w.Code, w.Body.String())
	}
	var report insights.DuplicatesReport
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Groups) != 1 || report.Groups[0].Level != insights.LevelMixedFormat {
		t.Errorf("groups = %+v", report.Groups)
	}

	w = do(r, http.MethodGet, "/api/v1/insights/stats?library="+lib.ID.String(), token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("stats: %d %s", w.Code, w.Body.String())
	}
	var stats insights.Stats
	json.Unmarshal(w.Body.Bytes(), &stats)
	if stats.Files != 2 {
		t.Errorf("stats files = %d, want 2", stats.Files)
	}

	for _, path := range []string{"/api/v1/insights/duplicates?library=nope", "/api/v1/insights/stats?library=nope"} {
		if w := do(r, http.MethodGet, path, token, nil); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", path, w.Code)
		}
	}

	// A mixed-format pair has nothing to hash: two formats never share audio.
	w = do(r, http.MethodPost, "/api/v1/insights/duplicates/check", token, nil)
	expectStatus(t, "check", w.Code, w.Body.String(), http.StatusConflict, "no duplicate")

	api.Scan = nil
	w = do(r, http.MethodPost, "/api/v1/insights/duplicates/check", token, nil)
	expectStatus(t, "check without runner", w.Code, w.Body.String(), http.StatusServiceUnavailable, "")
}

// TestInsightsGraphRoute: the graph answers with its filters read from the query, and
// a malformed number is a 400 naming the parameter.
func TestInsightsGraphRoute(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)

	w := do(r, http.MethodGet, "/api/v1/insights/graph?roles=credit,writing&min_weight=1&min_tracks=0&hops=1&limit=10&special=1", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("graph: %d %s", w.Code, w.Body.String())
	}
	var graph insights.GraphReport
	if err := json.Unmarshal(w.Body.Bytes(), &graph); err != nil {
		t.Fatal(err)
	}
	if graph.Nodes == nil || graph.Edges == nil {
		t.Errorf("an empty graph should be empty lists, got %s", w.Body.String())
	}
	for _, path := range []string{"/api/v1/insights/graph?min_weight=x", "/api/v1/insights/graph?limit=-1", "/api/v1/insights/graph?library=nope"} {
		if w := do(r, http.MethodGet, path, token, nil); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", path, w.Code)
		}
	}
}

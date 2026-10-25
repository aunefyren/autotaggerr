package routers

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// seedFailures indexes one file in each state the dashboard and the Items filter tell
// apart: a failure that will retry, one that will not, an unmatched file waiting on a
// second answer from its manager, a held pin, and a healthy file.
func seedFailures(t *testing.T, api *API) {
	t.Helper()
	lib := models.Library{Name: "L", Path: "/m"}
	if err := api.DB.Create(&lib).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, it := range []models.LibraryItem{
		{Path: "/m/retry.flac", Status: models.LibraryItemStatusError, Error: "MusicBrainz down", LastErrorAt: &now, LastErrorTransient: true},
		{Path: "/m/broken.flac", Status: models.LibraryItemStatusError, Error: "permission denied", LastErrorAt: &now},
		{Path: "/m/waiting.flac", Status: models.LibraryItemStatusUnmatched, MBReleaseID: "rel-1", LastErrorAt: &now, LastErrorTransient: true},
		{Path: "/m/held.flac", Status: models.LibraryItemStatusOK, PinHeld: true},
		{Path: "/m/fine.flac", Status: models.LibraryItemStatusOK},
	} {
		it.LibraryID = lib.ID
		if err := api.DB.Create(&it).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// TestListLibraryItemsSplitsFailuresByWhetherTheyRetry: "what failed?" asked of files,
// with the answer split the way an outage needs it split.
func TestListLibraryItemsSplitsFailuresByWhetherTheyRetry(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	seedFailures(t, api)

	paths := func(query string) []string {
		t.Helper()
		w := do(r, "GET", "/api/v1/library-items?"+query, token, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("%s = %d: %s", query, w.Code, w.Body.String())
		}
		var page struct {
			Items []struct {
				Path string `json:"path"`
			} `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, it := range page.Items {
			out = append(out, it.Path)
		}
		return out
	}

	for query, want := range map[string]string{
		"status=error&transient=true":     "/m/retry.flac",
		"status=error&transient=false":    "/m/broken.flac",
		"status=unmatched&transient=true": "/m/waiting.flac",
	} {
		if got := paths(query); len(got) != 1 || got[0] != want {
			t.Errorf("%s = %v, want [%s]", query, got, want)
		}
	}
	if w := do(r, "GET", "/api/v1/library-items?transient=maybe", token, nil); w.Code != http.StatusBadRequest {
		t.Errorf("an unreadable transient filter = %d, want 400 rather than every row", w.Code)
	}
}

// TestHealthCountsWhatNeedsAttention: the dashboard's figures, one per Items filter.
func TestHealthCountsWhatNeedsAttention(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	seedFailures(t, api)

	w := do(r, "GET", "/api/v1/health", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("health = %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Attention map[string]int `json:"attention"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"errors": 1, "retrying": 1, "pins_held": 1, "unmatched_wait": 1}
	for k, v := range want {
		if body.Attention[k] != v {
			t.Errorf("attention[%s] = %d, want %d (%v)", k, body.Attention[k], v, body.Attention)
		}
	}
}

// TestCancelWithNothingRunning: a stale Stop button is told nothing is running rather
// than answered as though it stopped something.
func TestCancelWithNothingRunning(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)
	if w := do(r, "POST", "/api/v1/process/cancel", token, nil); w.Code != http.StatusConflict {
		t.Errorf("cancel when idle = %d, want 409", w.Code)
	}
}

// TestArtistListFollowsEveryWrite: the collection list is served from memory between
// writes, so the one thing it must never do is miss one — an insert, an update and a
// delete each change what it shows on the very next request.
func TestArtistListFollowsEveryWrite(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	list := func() []map[string]any {
		t.Helper()
		w := do(r, "GET", "/api/v1/artists", token, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("artists = %d: %s", w.Code, w.Body.String())
		}
		var out []map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	artist := models.CollectionArtist{MBID: "a-1", Name: "Band"}
	if err := api.DB.Create(&artist).Error; err != nil {
		t.Fatal(err)
	}
	if got := list(); len(got) != 1 {
		t.Fatalf("list = %v", got)
	}
	if fp := api.artists.fingerprint; fp == "" {
		t.Fatal("the list was not kept")
	}

	rg := models.CollectionReleaseGroup{MBID: "rg-1", ArtistMBID: "a-1", Title: "One"}
	if err := api.DB.Create(&rg).Error; err != nil {
		t.Fatal(err)
	}
	if got := list(); got[0]["missing_count"] != float64(1) {
		t.Errorf("after an insert: %v, want the new album counted", got[0])
	}

	if err := api.DB.Model(&rg).Update("owned", true).Error; err != nil {
		t.Fatal(err)
	}
	if got := list(); got[0]["owned_count"] != float64(1) {
		t.Errorf("after an update: %v, want the album owned", got[0])
	}

	if err := api.DB.Delete(&rg).Error; err != nil {
		t.Fatal(err)
	}
	if got := list(); got[0]["owned_count"] != float64(0) || got[0]["missing_count"] != float64(0) {
		t.Errorf("after a delete: %v, want the album gone", got[0])
	}
}

// TestHealthReportsSetup: the sidebar's setup steps, as counts. A library under Lidarr
// is not one a download can be imported into; a library with no manager is.
func TestHealthReportsSetup(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	setup := func() map[string]int {
		t.Helper()
		var body struct {
			Setup map[string]int `json:"setup"`
		}
		w := do(r, "GET", "/api/v1/health", token, nil)
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Setup
	}
	if got := setup(); got["managers"] != 0 || got["libraries"] != 0 || got["importable_libraries"] != 0 {
		t.Fatalf("fresh setup = %v", got)
	}

	lidarr := models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true}
	api.DB.Create(&lidarr)
	api.DB.Create(&models.Library{Name: "Lidarr music", Path: t.TempDir(), Enabled: true, ManagerID: &lidarr.ID})
	if got := setup(); got["managers"] != 1 || got["libraries"] != 1 || got["importable_libraries"] != 0 {
		t.Errorf("with a Lidarr library = %v", got)
	}

	api.DB.Create(&models.Library{Name: "Rips", Path: t.TempDir(), Enabled: true})
	if got := setup(); got["libraries"] != 2 || got["importable_libraries"] != 1 {
		t.Errorf("with a library of no manager = %v", got)
	}
}

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

package routers

import (
	"net/http"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// TestSetReleaseGroup: a native library's files take a hand-set release group, one or
// several at once, trimmed, and cleared to "none" by an empty value. A Lidarr library's
// file is refused and left as it was — Lidarr records its group — and the listing
// says which is which before the request is ever made.
func TestSetReleaseGroup(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	managed := lidarrLibrary(t, api.DB)
	native := models.Library{Name: "Native", Path: "/n", Enabled: true}
	if err := api.DB.Create(&native).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	one := models.LibraryItem{LibraryID: native.ID, Path: "/n/A/B/01.flac", Status: models.LibraryItemStatusOK}
	two := models.LibraryItem{LibraryID: native.ID, Path: "/n/A/B/02.flac", Status: models.LibraryItemStatusOK}
	for _, row := range []*models.LibraryItem{&one, &two} {
		if err := api.DB.Create(row).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	groupOf := func(item models.LibraryItem) *string {
		t.Helper()
		var got models.LibraryItem
		if err := api.DB.First(&got, "id = ?", item.ID).Error; err != nil {
			t.Fatalf("read %s: %v", item.Path, err)
		}
		return got.ReleaseGroup
	}

	listing := decodeJSON[struct {
		Items []struct {
			Path                 string `json:"path"`
			ReleaseGroupEditable bool   `json:"release_group_editable"`
		} `json:"items"`
	}](t, r, "GET", "/api/v1/library-items", token, nil)
	for _, it := range listing.Items {
		if want := !strings.HasPrefix(it.Path, "/l/"); it.ReleaseGroupEditable != want {
			t.Errorf("%s: release_group_editable = %v, want %v", it.Path, it.ReleaseGroupEditable, want)
		}
	}

	w := do(r, "PUT", "/api/v1/item-release-group", token, map[string]any{
		"item_ids": []string{one.ID.String(), two.ID.String()}, "release_group": "  GRP ",
	})
	expectStatus(t, "native", w.Code, w.Body.String(), http.StatusOK, `"updated":2`)
	for _, item := range []models.LibraryItem{one, two} {
		if got := groupOf(item); got == nil || *got != "GRP" {
			t.Errorf("%s: release group = %v, want GRP", item.Path, got)
		}
	}

	w = do(r, "PUT", "/api/v1/item-release-group", token, map[string]any{"item_ids": []string{one.ID.String()}, "release_group": ""})
	expectStatus(t, "clear", w.Code, w.Body.String(), http.StatusOK, `"updated":1`)
	if got := groupOf(one); got == nil || *got != "" {
		t.Errorf("cleared release group = %v, want empty", got)
	}

	// One Lidarr file in the set refuses the whole set: nothing is half-applied.
	w = do(r, "PUT", "/api/v1/item-release-group", token, map[string]any{
		"item_ids": []string{two.ID.String(), managed.ID.String()}, "release_group": "OTHER",
	})
	expectStatus(t, "lidarr", w.Code, w.Body.String(), http.StatusConflict, "managed by Lidarr")
	if got := groupOf(two); got == nil || *got != "GRP" {
		t.Errorf("refused set changed %s to %v", two.Path, got)
	}
	if got := groupOf(managed); got != nil {
		t.Errorf("refused set recorded %v on the Lidarr file", *got)
	}

	w = do(r, "PUT", "/api/v1/item-release-group", token, map[string]any{"item_ids": []string{}, "release_group": "GRP"})
	expectStatus(t, "no items", w.Code, w.Body.String(), http.StatusBadRequest, "item_ids")
	w = do(r, "PUT", "/api/v1/item-release-group", token, map[string]any{"item_ids": []string{one.ID.String()}, "release_group": strings.Repeat("x", 65)})
	expectStatus(t, "too long", w.Code, w.Body.String(), http.StatusBadRequest, "at most 64")
	w = do(r, "PUT", "/api/v1/item-release-group", token, map[string]any{"item_ids": []string{"00000000-0000-0000-0000-000000000001"}, "release_group": "GRP"})
	expectStatus(t, "missing", w.Code, w.Body.String(), http.StatusNotFound, "item not found")
}

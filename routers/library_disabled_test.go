package routers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// A disabled library's files are kept in the index but left out of every view of
// "my libraries" (#40).

// TestItemsLeaveOutDisabledLibraries: the unfiltered Items list and the overview's
// counts skip a disabled library's files; naming the library still lists them.
func TestItemsLeaveOutDisabledLibraries(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	on := models.Library{Name: "On", Path: "/on", Enabled: true}
	off := models.Library{Name: "Off", Path: "/off", Enabled: true}
	for _, l := range []*models.Library{&on, &off} {
		if err := api.DB.Create(l).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := api.DB.Model(&off).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	for _, it := range []models.LibraryItem{
		{LibraryID: on.ID, Path: "/on/a.flac", Status: models.LibraryItemStatusOK},
		{LibraryID: off.ID, Path: "/off/b.flac", Status: models.LibraryItemStatusError, Error: "permission denied"},
	} {
		if err := api.DB.Create(&it).Error; err != nil {
			t.Fatal(err)
		}
	}

	total := func(query string) int {
		t.Helper()
		w := do(r, "GET", "/api/v1/library-items?"+query, token, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("%s = %d: %s", query, w.Code, w.Body.String())
		}
		var page struct {
			Total int `json:"total"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page.Total
	}
	if got := total(""); got != 1 {
		t.Errorf("every library = %d files, want 1 (the disabled library's left out)", got)
	}
	if got := total("library_id=" + off.ID.String()); got != 1 {
		t.Errorf("the disabled library named = %d files, want 1", got)
	}

	w := do(r, "GET", "/api/v1/health", token, nil)
	var body struct {
		Counts    map[string]int `json:"counts"`
		Attention map[string]int `json:"attention"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Counts["library_items"] != 1 {
		t.Errorf("indexed files = %d, want 1, the count the Items page shows", body.Counts["library_items"])
	}
	if body.Attention["errors"] != 0 {
		t.Errorf("errors = %d, want 0: the failing file is in a disabled library", body.Attention["errors"])
	}
}

// TestTogglingALibraryRebuildsTheCollection: switching a library off or on changes
// what the collection owns, so the edit asks for the re-derive itself.
func TestTogglingALibraryRebuildsTheCollection(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	lib := models.Library{Name: "Music", Path: t.TempDir(), Enabled: true}
	if err := api.DB.Create(&lib).Error; err != nil {
		t.Fatal(err)
	}
	// An owned row no file backs: any rebuild clears it, so it shows whether one ran.
	stale := models.CollectionReleaseGroup{MBID: "rg-stale", Title: "Stale", Owned: true, OwnedTracks: 3}
	if err := api.DB.Create(&stale).Error; err != nil {
		t.Fatal(err)
	}

	if w := do(r, "PUT", "/api/v1/libraries/"+lib.ID.String(), token, map[string]any{"enabled": false}); w.Code != http.StatusOK {
		t.Fatalf("disable = %d: %s", w.Code, w.Body.String())
	}
	api.Rebuilder.Wait()
	var rg models.CollectionReleaseGroup
	if err := api.DB.Where("mb_id = ?", "rg-stale").First(&rg).Error; err == nil && rg.Owned {
		t.Error("disabling a library should have re-derived the collection")
	}
}

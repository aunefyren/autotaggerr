package routers

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
)

// The scan and item endpoints. What is worth pinning here is the paging contract
// (the UI shows "first–last of total", so a bad limit must not silently become an
// unbounded query) and that a diff for a file that cannot be read is reported as a
// per-item problem rather than a broken endpoint.

func TestScanStatusShape(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)

	status := decodeJSON[map[string]any](t, r, "GET", "/api/v1/scan/status", token, nil)
	// Before anything has run, the status is a real zeroed summary rather than an
	// error — the dashboard renders it on first load.
	if status["running"] != false {
		t.Errorf("running = %v, want false", status["running"])
	}
	for _, field := range []string{"processed", "unchanged", "changed", "tags_written", "errors"} {
		if _, ok := status[field]; !ok {
			t.Errorf("status is missing %q", field)
		}
	}
}

func TestTriggerSyncStarts(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)

	// Accepted, not OK: the drift sync runs in the background and the caller watches
	// Activity for the outcome.
	if w := do(r, "POST", "/api/v1/sync", token, nil); w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202: %s", w.Code, w.Body.String())
	}
}

func TestScanLibraryValidation(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	if w := do(r, "POST", "/api/v1/libraries/not-a-uuid/scan", token, nil); w.Code != http.StatusBadRequest {
		t.Errorf("bad id = %d, want 400", w.Code)
	}
	if w := do(r, "POST", "/api/v1/libraries/"+uuid.New().String()+"/scan", token, nil); w.Code != http.StatusNotFound {
		t.Errorf("absent library = %d, want 404", w.Code)
	}

	// Asking for one library by id scans it even when it is disabled, and that is the
	// intended split: `enabled` governs the *scheduled* sweep (POST /scan), while
	// naming a single library is an explicit act. Pinned here because the difference
	// is easy to "fix" in the wrong direction.
	lib := models.Library{Name: "Off", Path: t.TempDir(), Enabled: false}
	if err := api.DB.Create(&lib).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	if w := do(r, "POST", "/api/v1/libraries/"+lib.ID.String()+"/scan", token, nil); w.Code != http.StatusAccepted {
		t.Errorf("explicit scan of a disabled library = %d, want 202: %s", w.Code, w.Body.String())
	}
}

// TestLibraryItemsPaging: the list reports a total alongside the page, which is what
// lets the UI say "1–50 of 900" instead of leaving "not found" and "not on this
// page" indistinguishable.
func TestLibraryItemsPaging(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	lib := models.Library{Name: "Music", Path: "/music", Enabled: true}
	if err := api.DB.Create(&lib).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := api.DB.Create(&models.LibraryItem{
			LibraryID: lib.ID,
			Path:      "/music/Artist/Album (2020)/0" + string(rune('1'+i)) + " Track.flac",
			Status:    models.LibraryItemStatusOK,
		}).Error; err != nil {
			t.Fatalf("create item: %v", err)
		}
	}

	page := decodeJSON[map[string]any](t, r, "GET", "/api/v1/library-items?limit=2&offset=1", token, nil)
	if page["total"] != float64(5) {
		t.Errorf("total = %v, want 5 (the whole set, not the page)", page["total"])
	}
	items, _ := page["items"].([]any)
	if len(items) != 2 {
		t.Errorf("items = %d, want 2", len(items))
	}

	// A junk limit falls back to the default rather than becoming an unbounded
	// query or an error.
	fallback := decodeJSON[map[string]any](t, r, "GET", "/api/v1/library-items?limit=abc&offset=xyz", token, nil)
	if fallback["limit"] == float64(0) {
		t.Errorf("limit = %v, want the default", fallback["limit"])
	}
	if fallback["offset"] != float64(0) {
		t.Errorf("offset = %v, want 0", fallback["offset"])
	}
}

func TestLibraryItemsFilterByStatus(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	lib := models.Library{Name: "Music", Path: "/music", Enabled: true}
	if err := api.DB.Create(&lib).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	for _, status := range []string{models.LibraryItemStatusOK, models.LibraryItemStatusUnmatched, models.LibraryItemStatusUnmatched} {
		if err := api.DB.Create(&models.LibraryItem{
			LibraryID: lib.ID, Path: "/music/" + status + uuid.New().String() + ".flac", Status: status,
		}).Error; err != nil {
			t.Fatalf("create item: %v", err)
		}
	}

	page := decodeJSON[map[string]any](t, r, "GET", "/api/v1/library-items?status="+models.LibraryItemStatusUnmatched, token, nil)
	if page["total"] != float64(2) {
		t.Errorf("total = %v, want 2 unmatched items", page["total"])
	}
}

// TestItemTagsReportsUnreadableFiles: the diff needs the file on disk. A path that is
// gone is a 422 about that item, not a 500 — the row is still listed, and the user
// needs to be told which file could not be read.
func TestItemTagsReportsUnreadableFiles(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	lib := models.Library{Name: "Music", Path: "/music", Enabled: true}
	if err := api.DB.Create(&lib).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	item := models.LibraryItem{
		LibraryID: lib.ID, Path: "/music/gone/01 Missing.flac", Status: models.LibraryItemStatusOK,
	}
	if err := api.DB.Create(&item).Error; err != nil {
		t.Fatalf("create item: %v", err)
	}

	if w := do(r, "GET", "/api/v1/library-items/"+item.ID.String()+"/tags", token, nil); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422: %s", w.Code, w.Body.String())
	}

	if w := do(r, "GET", "/api/v1/library-items/not-a-uuid/tags", token, nil); w.Code != http.StatusBadRequest {
		t.Errorf("bad id = %d, want 400", w.Code)
	}
	if w := do(r, "GET", "/api/v1/library-items/"+uuid.New().String()+"/tags", token, nil); w.Code != http.StatusNotFound {
		t.Errorf("absent item = %d, want 404", w.Code)
	}
}

func TestScanEndpointsRequireAuth(t *testing.T) {
	r, _ := setupAPI(t)
	for _, path := range []string{"/api/v1/scan/status", "/api/v1/library-items"} {
		if w := do(r, "GET", path, "", nil); w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s = %d, want 401", path, w.Code)
		}
	}
	for _, path := range []string{"/api/v1/scan", "/api/v1/sync"} {
		if w := do(r, "POST", path, "", nil); w.Code != http.StatusUnauthorized {
			t.Errorf("POST %s = %d, want 401", path, w.Code)
		}
	}
}

// The per-artist actions. Each is a background job, so what the endpoint can be held
// to is its refusals: an artist that does not exist, and an artist with nothing on
// disk to act on. Both are cases where a 202 would be a lie.

func seedArtistWithFile(t *testing.T, api *API, root string) string {
	t.Helper()

	library := models.Library{Name: "L", Path: root, Enabled: true}
	if err := api.DB.Create(&library).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	if err := api.DB.Create(&models.CollectionArtist{MBID: "artist-1", Name: "Artist"}).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	if err := api.DB.Create(&models.CollectionReleaseGroupArtist{ReleaseGroupMBID: "rg-1", ArtistMBID: "artist-1"}).Error; err != nil {
		t.Fatalf("link release-group: %v", err)
	}
	if err := api.DB.Create(&models.CollectionRelease{MBID: "rel-1", ReleaseGroupMBID: "rg-1", ArtistMBID: "artist-1"}).Error; err != nil {
		t.Fatalf("create release: %v", err)
	}
	if err := api.DB.Create(&models.LibraryItem{
		LibraryID:   library.ID,
		Path:        filepath.Join(root, "Artist", "Album (2020)", "01 track.flac"),
		MBReleaseID: "rel-1",
		Status:      models.LibraryItemStatusOK,
	}).Error; err != nil {
		t.Fatalf("create item: %v", err)
	}
	return "artist-1"
}

func TestArtistActionsRejectUnknownArtist(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)

	for _, action := range []string{"scan", "refresh", "retag"} {
		if w := do(r, "POST", "/api/v1/artists/nope/"+action, token, nil); w.Code != http.StatusNotFound {
			t.Errorf("%s of an unknown artist = %d, want 404: %s", action, w.Code, w.Body.String())
		}
	}
}

// Scanning and re-tagging an artist with no indexed files is refused rather than
// started: there is no folder to walk and nothing to write, so a 202 would report
// work that never happens.
func TestArtistScanRefusesWithoutFiles(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	if err := api.DB.Create(&models.CollectionArtist{MBID: "artist-1", Name: "Nobody"}).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}

	for _, action := range []string{"scan", "retag"} {
		w := do(r, "POST", "/api/v1/artists/artist-1/"+action, token, nil)
		if w.Code != http.StatusConflict {
			t.Errorf("%s without files = %d, want 409: %s", action, w.Code, w.Body.String())
		}
	}

	// Refresh deliberately has no such refusal — an artist with no files still has a
	// catalogue to re-read, which is how a followed artist's new album is discovered.
	// It is not asserted here: accepting it starts a real discography fetch in the
	// background, and MusicBrainz cannot be stubbed from outside modules/ (see
	// docs/development.md). Only its refusals are covered — the work itself needs a
	// live session.
}

func TestArtistScanStarts(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	mbid := seedArtistWithFile(t, api, t.TempDir())

	w := do(r, "POST", "/api/v1/artists/"+mbid+"/scan", token, nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", w.Code, w.Body.String())
	}
	// The response names what will be walked, so the caller can see the scope was
	// resolved to a folder rather than to the whole library.
	var body struct {
		Artist  string   `json:"artist"`
		Folders []string `json:"folders"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Artist != "Artist" || len(body.Folders) != 1 {
		t.Errorf("body = %+v, want the artist and one folder", body)
	}
}

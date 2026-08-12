package routers

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

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

	status := decodeJSON[map[string]any](t, r, "GET", "/api/v1/process/status", token, nil)
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
	if w := do(r, "POST", "/api/v1/refresh", token, nil); w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202: %s", w.Code, w.Body.String())
	}
}

func TestScanLibraryValidation(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	if w := do(r, "POST", "/api/v1/libraries/not-a-uuid/process", token, nil); w.Code != http.StatusBadRequest {
		t.Errorf("bad id = %d, want 400", w.Code)
	}
	if w := do(r, "POST", "/api/v1/libraries/"+uuid.New().String()+"/process", token, nil); w.Code != http.StatusNotFound {
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
	if w := do(r, "POST", "/api/v1/libraries/"+lib.ID.String()+"/process", token, nil); w.Code != http.StatusAccepted {
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
	for _, path := range []string{"/api/v1/process/status", "/api/v1/library-items"} {
		if w := do(r, "GET", path, "", nil); w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s = %d, want 401", path, w.Code)
		}
	}
	for _, path := range []string{"/api/v1/process", "/api/v1/refresh"} {
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

// TestArtistScanAnswersInline: the fourth verb at artist scope. Unlike the other
// three it is not queued — it re-derives from the index and reports what it found in
// the response, so the page can update without waiting on the Activity feed.
func TestArtistScanAnswersInline(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	mbid := seedArtistWithFile(t, api, t.TempDir())

	out := decodeJSON[map[string]any](t, r, "POST", "/api/v1/artists/"+mbid+"/scan", token, nil)
	if out["artist"] != "Artist" {
		t.Errorf("response does not name the artist it scanned: %v", out)
	}
	if _, ok := out["owned_release_groups"]; !ok {
		t.Errorf("response should carry what the scan found: %v", out)
	}
}

// An artist with no files is a valid scan: nothing to derive is an answer, and unlike
// process/retag there is no folder or file the refusal would be protecting.
func TestArtistScanWithoutFilesIsFine(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	if err := api.DB.Create(&models.CollectionArtist{MBID: "artist-1", Name: "Nobody"}).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	if w := do(r, "POST", "/api/v1/artists/artist-1/scan", token, nil); w.Code != http.StatusOK {
		t.Errorf("scan of a fileless artist = %d, want 200: %s", w.Code, w.Body.String())
	}
}

// TestRetagAllRefusesEmptyIndex: Tag files at collection scope refuses when nothing
// is indexed, exactly as its per-library and per-artist twins do — a queued job that
// tags nothing reads as a silent failure.
func TestRetagAllRefusesEmptyIndex(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)
	if w := do(r, "POST", "/api/v1/retag", token, nil); w.Code != http.StatusConflict {
		t.Errorf("retag with an empty index = %d, want 409: %s", w.Code, w.Body.String())
	}
}

func TestRetagAllQueues(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	seedArtistWithFile(t, api, t.TempDir())

	if w := do(r, "POST", "/api/v1/retag", token, nil); w.Code != http.StatusAccepted {
		t.Errorf("retag = %d, want 202: %s", w.Code, w.Body.String())
	}
}

func TestArtistActionsRejectUnknownArtist(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)

	for _, action := range []string{"process", "scan", "refresh", "retag"} {
		if w := do(r, "POST", "/api/v1/artists/nope/"+action, token, nil); w.Code != http.StatusNotFound {
			t.Errorf("%s of an unknown artist = %d, want 404: %s", action, w.Code, w.Body.String())
		}
	}
}

// Scanning and re-tagging an artist with no indexed files is refused rather than
// started: there is no folder to walk and nothing to write, so a 202 would report
// work that never happens.
func TestArtistProcessRefusesWithoutFiles(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	if err := api.DB.Create(&models.CollectionArtist{MBID: "artist-1", Name: "Nobody"}).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}

	for _, action := range []string{"process", "retag"} {
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

func TestArtistProcessStarts(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	mbid := seedArtistWithFile(t, api, t.TempDir())

	w := do(r, "POST", "/api/v1/artists/"+mbid+"/process", token, nil)
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

// The verb grid is filled in for libraries as well as artists: the same two scoped
// actions, aimed at one library.
func TestLibraryScopedActions(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	lib := models.Library{Name: "L", Path: "/m", Enabled: true}
	if err := api.DB.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}

	// Refresh is not gated on indexed files — a library with nothing in it simply
	// has nothing to refresh, which is a clean no-op rather than a refusal.
	if w := do(r, "POST", "/api/v1/libraries/"+lib.ID.String()+"/refresh", token, nil); w.Code != http.StatusAccepted {
		t.Errorf("refresh = %d, want 202: %s", w.Code, w.Body.String())
	}

	// Re-tagging nothing is a refusal, not a no-op: it would report "0 files
	// tagged" and look like the action silently failed.
	if w := do(r, "POST", "/api/v1/libraries/"+lib.ID.String()+"/retag", token, nil); w.Code != http.StatusConflict {
		t.Errorf("retag with no indexed files = %d, want 409: %s", w.Code, w.Body.String())
	}

	if err := api.DB.Create(&models.LibraryItem{
		LibraryID: lib.ID, Path: "/m/a.flac", Status: models.LibraryItemStatusOK, MBReleaseID: "rel-1",
	}).Error; err != nil {
		t.Fatalf("item: %v", err)
	}
	if w := do(r, "POST", "/api/v1/libraries/"+lib.ID.String()+"/retag", token, nil); w.Code != http.StatusAccepted {
		t.Errorf("retag = %d, want 202: %s", w.Code, w.Body.String())
	}
}

// The guard counts with models.TaggableItems because the runner selects with it. If
// the two drift the failure is silent in both directions: a button that refuses work
// there is, or one that queues a run which then tags nothing.
func TestRetagGuardCountsWhatTheRunnerWouldTag(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	lib := models.Library{Name: "L", Path: "/m", Enabled: true}
	if err := api.DB.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}

	// A file whose last attempt failed is still taggable — it has an identity, and
	// the re-tag is what clears the error.
	if err := api.DB.Create(&models.LibraryItem{
		LibraryID: lib.ID, Path: "/m/broken.flac",
		Status: models.LibraryItemStatusError, MBReleaseID: "rel-1",
	}).Error; err != nil {
		t.Fatalf("item: %v", err)
	}
	for _, path := range []string{
		"/api/v1/libraries/" + lib.ID.String() + "/retag",
		"/api/v1/retag",
	} {
		if w := do(r, "POST", path, token, nil); w.Code != http.StatusAccepted {
			t.Errorf("%s = %d, want 202 — an errored file is still taggable: %s", path, w.Code, w.Body.String())
		}
	}
}

// The converse: files the manager has disowned, or that were never identified, are not
// work. Counting them would queue a run with nothing to write.
func TestRetagGuardRefusesUntaggableFiles(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	lib := models.Library{Name: "L", Path: "/m", Enabled: true}
	if err := api.DB.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	for _, item := range []models.LibraryItem{
		{LibraryID: lib.ID, Path: "/m/disowned.flac", Status: models.LibraryItemStatusUnmatched, MBReleaseID: "rel-1"},
		{LibraryID: lib.ID, Path: "/m/unknown.flac", Status: models.LibraryItemStatusUnmatched},
	} {
		if err := api.DB.Create(&item).Error; err != nil {
			t.Fatalf("item: %v", err)
		}
	}

	for _, path := range []string{
		"/api/v1/libraries/" + lib.ID.String() + "/retag",
		"/api/v1/retag",
	} {
		if w := do(r, "POST", path, token, nil); w.Code != http.StatusConflict {
			t.Errorf("%s = %d, want 409 — nothing here is fit to write: %s", path, w.Code, w.Body.String())
		}
	}
}

func TestLibraryScopedActionsUnknownLibrary(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)
	missing := uuid.New().String()

	for _, verb := range []string{"refresh", "retag"} {
		if w := do(r, "POST", "/api/v1/libraries/"+missing+"/"+verb, token, nil); w.Code != http.StatusNotFound {
			t.Errorf("%s on an unknown library = %d, want 404", verb, w.Code)
		}
	}
}

func TestLibraryScopedActionsRequireAuth(t *testing.T) {
	r, _ := setupAPI(t)
	for _, verb := range []string{"refresh", "retag"} {
		if w := do(r, "POST", "/api/v1/libraries/"+uuid.New().String()+"/"+verb, "", nil); w.Code != http.StatusUnauthorized {
			t.Errorf("%s without a token = %d, want 401", verb, w.Code)
		}
	}
}

// The per-artist refresh honours the cache by default and forces only when asked, the
// same way the collection-scoped one does. The response echoes which reading it parsed,
// because the two are otherwise indistinguishable from outside — and a force that
// silently downgraded to the cheap reading is the failure worth catching.
//
// The runner is shut down first, so nothing is actually queued. That is deliberate:
// refreshing an artist that *exists* starts a real MusicBrainz pass on a background
// goroutine, which would reach the network from the test suite and outlive the test
// that started it. What this asserts is the handler's reading of the request, which is
// the part that can regress silently; the work behind it is covered where it can be
// driven without a network — process.TestForcedArtistRefreshIsNotDedupedOntoTheCheapOne
// and mirror.TestOnlyAnExplicitForceIgnoresTheCache.
func TestArtistRefreshForcesOnlyWhenAsked(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	if err := api.DB.Create(&models.CollectionArtist{MBID: "artist-1", Name: "Talk Talk"}).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := api.Scan.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown runner: %v", err)
	}

	for path, want := range map[string]bool{
		"/api/v1/artists/artist-1/refresh":            false,
		"/api/v1/artists/artist-1/refresh?force=true": true,
		// Anything that is not the exact opt-in is the cheap reading: a typo must not
		// cost hours, and "force" is spelled one way.
		"/api/v1/artists/artist-1/refresh?force=1":     false,
		"/api/v1/artists/artist-1/refresh?force=false": false,
	} {
		w := do(r, "POST", path, token, nil)
		if w.Code != http.StatusAccepted {
			t.Errorf("POST %s = %d, want 202: %s", path, w.Code, w.Body.String())
			continue
		}
		var body struct {
			Force bool `json:"force"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if body.Force != want {
			t.Errorf("POST %s reported force=%v, want %v", path, body.Force, want)
		}
	}
}

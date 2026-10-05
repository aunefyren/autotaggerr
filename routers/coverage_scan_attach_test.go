package routers

import (
	"errors"
	"net/http"
	"strconv"
	"testing"

	"github.com/aunefyren/autotaggerr/metadata"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TestVerbsWithoutAProcessor: an API wired without a runner answers every queued verb
// with 503 rather than panicking on the nil. The checks run before any lookup, so the
// paths need no fixtures to exist.
func TestVerbsWithoutAProcessor(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	api.Scan = nil

	lib := uuid.New().String()
	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/v1/process"},
		{"POST", "/api/v1/refresh"},
		{"POST", "/api/v1/retag"},
		{"GET", "/api/v1/process/status"},
		{"POST", "/api/v1/libraries/" + lib + "/process"},
		{"POST", "/api/v1/libraries/" + lib + "/refresh"},
		{"POST", "/api/v1/libraries/" + lib + "/retag"},
		{"POST", "/api/v1/libraries/" + lib + "/recorrelate"},
		{"POST", "/api/v1/artists/art-1/process"},
		{"POST", "/api/v1/artists/art-1/scan"},
		{"POST", "/api/v1/artists/art-1/refresh"},
		{"POST", "/api/v1/artists/art-1/retag"},
		{"POST", "/api/v1/artists/art-1/recorrelate"},
		{"POST", "/api/v1/release-groups/rg-1/recorrelate"},
		{"POST", "/api/v1/migrations/verify"},
	} {
		w := do(r, tc.method, tc.path, token, nil)
		expectStatus(t, tc.method+" "+tc.path, w.Code, w.Body.String(), http.StatusServiceUnavailable, "unavailable")
	}
}

// TestArtistVerbsReportScopeFailures: when the artist's files cannot be resolved the
// verbs say so with a 500 — distinct from the 409 that means "nothing to do".
func TestArtistVerbsReportScopeFailures(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	artistFixture(t, api.DB, "art-1", "Band", models.ManagedByAutotaggerr, false)
	releaseGroupFixture(t, api.DB, "art-1", rgFixture{mbid: "rg-1", title: "A", primary: "Album", owned: true})
	dropTable(t, api.DB, &models.CollectionRelease{})

	for _, tc := range []struct{ path, want string }{
		{"/api/v1/artists/art-1/process", "failed to resolve what to process"},
		{"/api/v1/artists/art-1/retag", "failed to resolve what to tag"},
		{"/api/v1/artists/art-1/recorrelate", "failed to resolve what to re-correlate"},
		{"/api/v1/release-groups/rg-1/recorrelate", "failed to resolve what to re-correlate"},
	} {
		w := do(r, "POST", tc.path, token, nil)
		expectStatus(t, "POST "+tc.path, w.Code, w.Body.String(), http.StatusInternalServerError, tc.want)
	}
}

// TestRetagArtistQueues: an artist with an indexed, correlated file queues its re-tag
// and reports how many files it covers. The library's profile writes nothing, so the
// queued job finishes without touching a file or MusicBrainz.
func TestRetagArtistQueues(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	artistFixture(t, api.DB, "art-1", "Band", models.ManagedByAutotaggerr, false)

	profile := models.TaggerProfile{Name: "no-write", WriteTags: false}
	if err := api.DB.Create(&profile).Error; err != nil {
		t.Fatalf("profile: %v", err)
	}
	// gorm skips a false bool on create; force it so the default cannot win.
	api.DB.Model(&profile).Update("write_tags", false)
	lib := models.Library{Name: "L", Path: "/m", Enabled: true, TaggerProfileID: &profile.ID}
	if err := api.DB.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	if err := api.DB.Create(&models.CollectionRelease{
		MBID: "rel-x", ReleaseGroupMBID: "rg-1", ArtistMBID: "art-1",
	}).Error; err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := api.DB.Create(&models.LibraryItem{
		LibraryID: lib.ID, Path: "/m/Band/A/01.flac", MBReleaseID: "rel-x", Status: models.LibraryItemStatusOK,
	}).Error; err != nil {
		t.Fatalf("item: %v", err)
	}

	w := do(r, "POST", "/api/v1/artists/art-1/retag", token, nil)
	expectStatus(t, "retag artist", w.Code, w.Body.String(), http.StatusAccepted, `"files":1`)
}

// TestListLibraryItemsEdges covers the filter validation, the paging clamps, the
// count failure, and the fail-closed editable flag for an item whose library is gone.
func TestListLibraryItemsEdges(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	w := do(r, "GET", "/api/v1/library-items?library_id=nope", token, nil)
	expectStatus(t, "bad library_id", w.Code, w.Body.String(), http.StatusBadRequest, "invalid library_id")

	// An orphaned row: its library was deleted out from under it.
	if err := api.DB.Create(&models.LibraryItem{LibraryID: uuid.New(), Path: "/gone/01.flac", Status: models.LibraryItemStatusOK}).Error; err != nil {
		t.Fatalf("item: %v", err)
	}

	for _, tc := range []struct {
		query                string
		wantLimit, wantOffst int
	}{
		{"limit=0&offset=-5", defaultItemsLimit, 0},
		{"limit=100000", maxItemsLimit, 0},
	} {
		got := decodeJSON[struct {
			Limit  int `json:"limit"`
			Offset int `json:"offset"`
			Items  []struct {
				Path             string `json:"path"`
				IdentityEditable bool   `json:"identity_editable"`
			} `json:"items"`
		}](t, r, "GET", "/api/v1/library-items?"+tc.query, token, nil)
		if got.Limit != tc.wantLimit || got.Offset != tc.wantOffst {
			t.Errorf("%s: limit/offset = %d/%d, want %d/%d", tc.query, got.Limit, got.Offset, tc.wantLimit, tc.wantOffst)
		}
		if len(got.Items) != 1 || got.Items[0].IdentityEditable {
			t.Errorf("%s: items = %+v, want the orphan listed as not editable", tc.query, got.Items)
		}
	}

	dropTable(t, api.DB, &models.LibraryItem{})
	w = do(r, "GET", "/api/v1/library-items", token, nil)
	expectStatus(t, "count failure", w.Code, w.Body.String(), http.StatusInternalServerError, "failed to count items")
}

// TestPastedMBIDUpstreamFailures: a pasted artist or release-group URL that the source
// cannot answer for is a 502, the same as a failed search.
func TestPastedMBIDUpstreamFailures(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	api.Meta = &fakeMeta{
		searchRel: func(metadata.ReleaseSearchQuery) (metadata.ReleaseSearchPage, error) {
			return metadata.ReleaseSearchPage{}, errors.New("down")
		},
		getRGReleases: func(string) ([]models.MusicBrainzReleaseSearchResult, error) {
			return nil, errors.New("down")
		},
	}

	w := do(r, "GET", searchURL(map[string]string{"q": "https://musicbrainz.org/artist/" + testMBID}), token, nil)
	expectStatus(t, "artist URL", w.Code, w.Body.String(), http.StatusBadGateway, "MusicBrainz search failed")
	w = do(r, "GET", searchURL(map[string]string{"q": "https://musicbrainz.org/release-group/" + testMBID}), token, nil)
	expectStatus(t, "release-group URL", w.Code, w.Body.String(), http.StatusBadGateway, "could not load that release group")
}

// lidarrLibrary creates a library governed by an enabled Lidarr manager, plus one
// file in it, for the identity gate.
func lidarrLibrary(t *testing.T, db *gorm.DB) models.LibraryItem {
	t.Helper()
	m := models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true}
	if err := db.Create(&m).Error; err != nil {
		t.Fatalf("manager: %v", err)
	}
	lib := models.Library{Name: "LL", Path: "/l", Enabled: true, ManagerID: &m.ID}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	item := models.LibraryItem{LibraryID: lib.ID, Path: "/l/A/B/01.flac", Status: models.LibraryItemStatusUnmatched}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("item: %v", err)
	}
	return item
}

// TestAttachValidation covers the single attach's request checks and the identity
// gate in both of its refusals.
func TestAttachValidation(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	body := map[string]any{"mb_release_id": "rel-1", "mb_release_track_id": "t1"}

	w := do(r, "POST", "/api/v1/library-items/not-a-uuid/attach", token, body)
	expectStatus(t, "bad id", w.Code, w.Body.String(), http.StatusBadRequest, "invalid id")
	w = do(r, "POST", "/api/v1/library-items/"+uuid.New().String()+"/attach", token, "not-an-object")
	expectStatus(t, "malformed", w.Code, w.Body.String(), http.StatusBadRequest, "invalid body")
	w = do(r, "POST", "/api/v1/library-items/"+uuid.New().String()+"/attach", token, body)
	expectStatus(t, "absent", w.Code, w.Body.String(), http.StatusNotFound, "library item not found")

	lidarrItem := lidarrLibrary(t, api.DB)
	w = do(r, "POST", "/api/v1/library-items/"+lidarrItem.ID.String()+"/attach", token, body)
	expectStatus(t, "lidarr-managed", w.Code, w.Body.String(), http.StatusConflict, "managed by Lidarr")

	// A library pointing at a manager that no longer exists cannot be resolved, and
	// the gate fails closed.
	missing := uuid.New()
	lib := models.Library{Name: "Broken", Path: "/b", Enabled: true, ManagerID: &missing}
	if err := api.DB.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	broken := models.LibraryItem{LibraryID: lib.ID, Path: "/b/01.flac", Status: models.LibraryItemStatusUnmatched}
	if err := api.DB.Create(&broken).Error; err != nil {
		t.Fatalf("item: %v", err)
	}
	w = do(r, "POST", "/api/v1/library-items/"+broken.ID.String()+"/attach", token, body)
	expectStatus(t, "unresolvable manager", w.Code, w.Body.String(), http.StatusInternalServerError, "failed to resolve the library manager")
}

// TestAttachSaveFailure: a correlation the database refuses is a 500, and no tag write
// is attempted on its behalf.
func TestAttachSaveFailure(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	item := seedAttachFixtures(t, api.DB)
	failWrites(t, api.DB, &models.LibraryItem{}, "UPDATE")

	w := do(r, "POST", "/api/v1/library-items/"+item.ID.String()+"/attach", token,
		map[string]any{"mb_release_id": "rel-1", "mb_release_track_id": "t1"})
	expectStatus(t, "attach", w.Code, w.Body.String(), http.StatusInternalServerError, "failed to save the correlation")

	w = do(r, "DELETE", "/api/v1/library-items/"+item.ID.String()+"/attach", token, nil)
	expectStatus(t, "detach", w.Code, w.Body.String(), http.StatusInternalServerError, "failed to remove the pin")
	w = do(r, "DELETE", "/api/v1/library-items/not-a-uuid/attach", token, nil)
	expectStatus(t, "detach bad id", w.Code, w.Body.String(), http.StatusBadRequest, "invalid id")
}

// TestBulkValidation covers the request checks the preview and the attach share.
func TestBulkValidation(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	items := seedBulkFixtures(t, api.DB)

	tooMany := make([]uuid.UUID, maxBulkAttachItems+1)
	for i := range tooMany {
		tooMany[i] = uuid.New()
	}

	for _, tc := range []struct {
		name, path string
		body       any
		want       string
	}{
		{"preview malformed", "/api/v1/attach/preview", "not-an-object", "invalid body"},
		{"preview without release", "/api/v1/attach/preview", map[string]any{"item_ids": itemIDs(items)}, "mb_release_id is required"},
		{"preview without files", "/api/v1/attach/preview", map[string]any{"mb_release_id": "rel-bulk"}, "no files selected"},
		{"preview too many files", "/api/v1/attach/preview", map[string]any{"mb_release_id": "rel-bulk", "item_ids": tooMany}, "at most " + strconv.Itoa(maxBulkAttachItems)},
		{"attach malformed", "/api/v1/attach/bulk", "not-an-object", "invalid body"},
		{"attach duplicate file", "/api/v1/attach/bulk", map[string]any{
			"mb_release_id": "rel-bulk",
			"mappings": []map[string]any{
				{"item_id": items[0].ID, "mb_release_track_id": "t1"},
				{"item_id": items[0].ID, "mb_release_track_id": "t2"},
			},
		}, "appears twice"},
	} {
		w := do(r, "POST", tc.path, token, tc.body)
		expectStatus(t, tc.name, w.Code, w.Body.String(), http.StatusBadRequest, tc.want)
	}

	// The identity gate guards both halves.
	lidarrItem := lidarrLibrary(t, api.DB)
	w := do(r, "POST", "/api/v1/attach/preview", token, map[string]any{"mb_release_id": "rel-bulk", "item_ids": []uuid.UUID{lidarrItem.ID}})
	expectStatus(t, "preview lidarr", w.Code, w.Body.String(), http.StatusConflict, "managed by Lidarr")
	w = do(r, "POST", "/api/v1/attach/bulk", token, map[string]any{
		"mb_release_id": "rel-bulk",
		"mappings":      []map[string]any{{"item_id": lidarrItem.ID, "mb_release_track_id": "t1"}},
	})
	expectStatus(t, "attach lidarr", w.Code, w.Body.String(), http.StatusConflict, "managed by Lidarr")
}

// TestBulkAttachFailures covers what can go wrong after validation: the release
// cannot be fetched, a correlation cannot be saved, and the tag write fails per file
// while the correlations stand.
func TestBulkAttachFailures(t *testing.T) {
	mapping := func(items []models.LibraryItem) []map[string]any {
		// The fixture's disk order is 02, 01, 03.
		return []map[string]any{
			{"item_id": items[0].ID, "mb_release_track_id": "t2"},
			{"item_id": items[1].ID, "mb_release_track_id": "t1"},
		}
	}

	t.Run("release fetch fails", func(t *testing.T) {
		r, api := setupAPI(t)
		token := loginToken(t, r)
		items := seedBulkFixtures(t, api.DB)
		api.Meta = &fakeMeta{getRelease: func(string) (models.MusicBrainzReleaseResponse, error) {
			return models.MusicBrainzReleaseResponse{}, errors.New("down")
		}}
		w := do(r, "POST", "/api/v1/attach/bulk", token, map[string]any{"mb_release_id": "rel-bulk", "mappings": mapping(items)})
		expectStatus(t, "attach", w.Code, w.Body.String(), http.StatusBadGateway, "could not load that release")
	})
	t.Run("save fails", func(t *testing.T) {
		r, api := setupAPI(t)
		token := loginToken(t, r)
		items := seedBulkFixtures(t, api.DB)
		failWrites(t, api.DB, &models.LibraryItem{}, "UPDATE")
		w := do(r, "POST", "/api/v1/attach/bulk", token, map[string]any{"mb_release_id": "rel-bulk", "mappings": mapping(items)})
		expectStatus(t, "attach", w.Code, w.Body.String(), http.StatusInternalServerError, "failed to save the correlations")
	})
	t.Run("tagging fails per file", func(t *testing.T) {
		r, api := setupAPI(t)
		token := loginToken(t, r)
		items := seedBulkFixtures(t, api.DB)
		// Turn writing on: the fixture paths do not exist, so every write fails —
		// after the correlations are saved, from a release already in the cache.
		api.DB.Model(&models.TaggerProfile{}).Where("name = ?", "bulk-no-write").Update("write_tags", true)

		w := do(r, "POST", "/api/v1/attach/bulk", token, map[string]any{"mb_release_id": "rel-bulk", "mappings": mapping(items)})
		expectStatus(t, "attach", w.Code, w.Body.String(), http.StatusAccepted, "tagging failed for 2")

		var pinned int64
		api.DB.Model(&models.LibraryItem{}).Where("pinned = ?", true).Count(&pinned)
		if pinned != 2 {
			t.Errorf("pinned = %d, want both correlations kept despite the tag failure", pinned)
		}
	})
}

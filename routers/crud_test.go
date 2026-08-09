package routers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
)

// idOf extracts the "id" field from a JSON response body.
func idOf(t *testing.T, body []byte) string {
	t.Helper()
	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || resp.ID == "" {
		t.Fatalf("no id in response: %s", string(body))
	}
	return resp.ID
}

func TestDataSourceCRUD(t *testing.T) {
	r, _ := setupAPI(t)
	tok := loginToken(t, r)

	// create
	w := do(r, "POST", "/api/v1/data-sources", tok, map[string]any{"name": "MB2", "type": "musicbrainz", "rate_limit": 1})
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", w.Code, w.Body.String())
	}
	id := idOf(t, w.Body.Bytes())

	// get
	if w := do(r, "GET", "/api/v1/data-sources/"+id, tok, nil); w.Code != http.StatusOK {
		t.Errorf("get = %d", w.Code)
	}

	// update
	w = do(r, "PUT", "/api/v1/data-sources/"+id, tok, map[string]any{"enabled": false})
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`"enabled":false`)) {
		t.Errorf("update = %d: %s", w.Code, w.Body.String())
	}

	// delete
	if w := do(r, "DELETE", "/api/v1/data-sources/"+id, tok, nil); w.Code != http.StatusNoContent {
		t.Errorf("delete = %d", w.Code)
	}
	if w := do(r, "GET", "/api/v1/data-sources/"+id, tok, nil); w.Code != http.StatusNotFound {
		t.Errorf("get after delete = %d, want 404", w.Code)
	}
}

func TestDataSourceCreateValidation(t *testing.T) {
	r, _ := setupAPI(t)
	tok := loginToken(t, r)

	// missing type
	if w := do(r, "POST", "/api/v1/data-sources", tok, map[string]any{"name": "x"}); w.Code != http.StatusBadRequest {
		t.Errorf("missing type = %d, want 400", w.Code)
	}
	// bad type
	if w := do(r, "POST", "/api/v1/data-sources", tok, map[string]any{"name": "x", "type": "spotify"}); w.Code != http.StatusBadRequest {
		t.Errorf("bad type = %d, want 400", w.Code)
	}
}

// TestDataSourceSingletonTypes pins which types may exist more than once. There is
// one AcoustID service, one Cover Art Archive and one fanart.tv, and a duplicate row
// is never consulted — only the first match is. MusicBrainz is the deliberate
// exception, because a local mirror alongside the public service is a real setup.
func TestDataSourceSingletonTypes(t *testing.T) {
	r, _ := setupAPI(t)
	tok := loginToken(t, r)

	for _, dsType := range []string{"acoustid", "coverartarchive", "fanart"} {
		first := do(r, "POST", "/api/v1/data-sources", tok, map[string]any{"name": dsType + "-1", "type": dsType})
		if first.Code != http.StatusCreated {
			t.Fatalf("%s first create = %d: %s", dsType, first.Code, first.Body.String())
		}
		second := do(r, "POST", "/api/v1/data-sources", tok, map[string]any{"name": dsType + "-2", "type": dsType})
		if second.Code != http.StatusConflict {
			t.Errorf("%s second create = %d, want 409: %s", dsType, second.Code, second.Body.String())
		}
	}

	// Two MusicBrainz rows stay legal: public service plus a mirror.
	if w := do(r, "POST", "/api/v1/data-sources", tok, map[string]any{"name": "MB", "type": "musicbrainz"}); w.Code != http.StatusCreated {
		t.Fatalf("first musicbrainz = %d: %s", w.Code, w.Body.String())
	}
	if w := do(r, "POST", "/api/v1/data-sources", tok, map[string]any{"name": "MB mirror", "type": "musicbrainz"}); w.Code != http.StatusCreated {
		t.Errorf("second musicbrainz = %d, want 201 — a mirror is legitimate: %s", w.Code, w.Body.String())
	}
}

// TestLibraryDataSourceMustBeMetadata covers the confusion this fixes at the API
// level, not just in the UI: AcoustID and the artwork providers were accepted as a
// library's data source and then silently ignored, because only release metadata is
// ever read from it.
func TestLibraryDataSourceMustBeMetadata(t *testing.T) {
	r, _ := setupAPI(t)
	tok := loginToken(t, r)

	newSource := func(name, dsType string) string {
		t.Helper()
		w := do(r, "POST", "/api/v1/data-sources", tok, map[string]any{"name": name, "type": dsType})
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s source = %d: %s", dsType, w.Code, w.Body.String())
		}
		return idOf(t, w.Body.Bytes())
	}
	metadataID := newSource("MB", "musicbrainz")
	artworkID := newSource("fanart", "fanart")
	fingerprintID := newSource("AcoustID", "acoustid")

	// Rejected on create, one case per non-metadata category.
	for _, bad := range []struct{ label, id string }{
		{"artwork", artworkID},
		{"fingerprint", fingerprintID},
		{"unknown", uuid.New().String()},
	} {
		w := do(r, "POST", "/api/v1/libraries", tok, map[string]any{
			"name": "L-" + bad.label, "path": "/music/" + bad.label, "data_source_id": bad.id,
		})
		if w.Code != http.StatusBadRequest {
			t.Errorf("create with %s source = %d, want 400: %s", bad.label, w.Code, w.Body.String())
		}
	}

	// A metadata source is accepted.
	w := do(r, "POST", "/api/v1/libraries", tok, map[string]any{
		"name": "Music", "path": "/music", "data_source_id": metadataID,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create with metadata source = %d, want 201: %s", w.Code, w.Body.String())
	}
	libID := idOf(t, w.Body.Bytes())

	// And the same rule holds on update — the path a user actually hits, since the
	// complaint was about the edit-library modal.
	if w := do(r, "PUT", "/api/v1/libraries/"+libID, tok, map[string]any{"data_source_id": artworkID}); w.Code != http.StatusBadRequest {
		t.Errorf("update to artwork source = %d, want 400: %s", w.Code, w.Body.String())
	}
	// An update that does not mention the data source must still pass.
	if w := do(r, "PUT", "/api/v1/libraries/"+libID, tok, map[string]any{"enabled": false}); w.Code != http.StatusOK {
		t.Errorf("unrelated update = %d, want 200: %s", w.Code, w.Body.String())
	}

	// The library kept the source it was created with.
	w = do(r, "GET", "/api/v1/libraries/"+libID, tok, nil)
	if !bytes.Contains(w.Body.Bytes(), []byte(metadataID)) {
		t.Errorf("library lost its data source: %s", w.Body.String())
	}
}

func TestManagerCreateSecretNotReturned(t *testing.T) {
	r, _ := setupAPI(t)
	tok := loginToken(t, r)

	w := do(r, "POST", "/api/v1/managers", tok, map[string]any{
		"name": "L", "type": "lidarr", "lidarr_base_url": "http://x", "lidarr_api_key": "TOPSECRET",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create manager = %d: %s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("TOPSECRET")) {
		t.Errorf("create response leaked the API key: %s", w.Body.String())
	}
	id := idOf(t, w.Body.Bytes())

	// The secret must be stored even though it is never returned.
	w = do(r, "GET", "/api/v1/managers/"+id, tok, nil)
	if bytes.Contains(w.Body.Bytes(), []byte("TOPSECRET")) {
		t.Errorf("get response leaked the API key: %s", w.Body.String())
	}
}

// TestManagerTestConnection: a rejected probe is a *successful* test. The endpoint says
// so with 200 and a verdict in the body, because a non-2xx would make the UI report
// that the test failed for the case where it worked and the answer was no. The two
// `*_set` flags answer the question that costs the most time to get wrong — "were any
// credentials sent at all?" — without returning the secrets themselves.
func TestManagerTestConnection(t *testing.T) {
	r, _ := setupAPI(t)
	tok := loginToken(t, r)

	w := do(r, "POST", "/api/v1/managers", tok, map[string]any{
		"name": "L", "type": "lidarr", "lidarr_base_url": "http://127.0.0.1:1",
		"lidarr_api_key": "TOPSECRET", "lidarr_header_cookie": "session=COOKIEVALUE",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create manager = %d: %s", w.Code, w.Body.String())
	}
	id := idOf(t, w.Body.Bytes())

	w = do(r, "POST", "/api/v1/managers/"+id+"/test", tok, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("test unreachable manager = %d, want 200: %s", w.Code, w.Body.String())
	}
	for _, want := range []string{`"healthy":false`, `"api_key_set":true`, `"cookie_set":true`, `"error"`} {
		if !bytes.Contains(w.Body.Bytes(), []byte(want)) {
			t.Errorf("test response missing %s: %s", want, w.Body.String())
		}
	}
	for _, secret := range []string{"TOPSECRET", "COOKIEVALUE"} {
		if bytes.Contains(w.Body.Bytes(), []byte(secret)) {
			t.Errorf("test response leaked %s: %s", secret, w.Body.String())
		}
	}

	// A native manager has nothing to reach, and says so rather than erroring.
	w = do(r, "POST", "/api/v1/managers", tok, map[string]any{"name": "N", "type": "autotaggerr"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create native manager = %d: %s", w.Code, w.Body.String())
	}
	w = do(r, "POST", "/api/v1/managers/"+idOf(t, w.Body.Bytes())+"/test", tok, nil)
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`"healthy":true`)) {
		t.Errorf("test native manager = %d: %s", w.Code, w.Body.String())
	}
}

func TestLibraryValidation(t *testing.T) {
	r, _ := setupAPI(t)
	tok := loginToken(t, r)
	if w := do(r, "POST", "/api/v1/libraries", tok, map[string]any{"name": "NoPath"}); w.Code != http.StatusBadRequest {
		t.Errorf("library without path = %d, want 400", w.Code)
	}
}

func TestScanEndpoints(t *testing.T) {
	r, _ := setupAPI(t)
	tok := loginToken(t, r)

	// status starts idle
	w := do(r, "GET", "/api/v1/process/status", tok, nil)
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`"running":false`)) {
		t.Errorf("status = %d: %s", w.Code, w.Body.String())
	}

	// trigger returns 202 (no libraries -> a quick no-op run)
	if w := do(r, "POST", "/api/v1/process", tok, nil); w.Code != http.StatusAccepted {
		t.Errorf("trigger scan = %d, want 202", w.Code)
	}

	// scanning a nonexistent library -> 404
	if w := do(r, "POST", "/api/v1/libraries/"+uuid.New().String()+"/process", tok, nil); w.Code != http.StatusNotFound {
		t.Errorf("scan missing library = %d, want 404", w.Code)
	}
}

func TestEventsList(t *testing.T) {
	r, api := setupAPI(t)
	tok := loginToken(t, r)

	for _, st := range []string{"ok", "error"} {
		if err := api.DB.Create(&models.Event{Type: "scan", Status: st, Title: "Library scan", Summary: "done", Details: map[string]any{"errors": 0}}).Error; err != nil {
			t.Fatalf("create event: %v", err)
		}
	}

	w := do(r, "GET", "/api/v1/events", tok, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("events = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Total  int `json:"total"`
		Events []struct {
			ID      string         `json:"id"`
			Type    string         `json:"type"`
			Details map[string]any `json:"details"`
		} `json:"events"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 2 || len(resp.Events) != 2 {
		t.Errorf("expected 2 events, got total=%d len=%d", resp.Total, len(resp.Events))
	}

	// Filter by status.
	w = do(r, "GET", "/api/v1/events?status=error", tok, nil)
	if !bytes.Contains(w.Body.Bytes(), []byte(`"total":1`)) {
		t.Errorf("status filter wrong: %s", w.Body.String())
	}

	// Fetch one by id (details survive the round trip).
	id := resp.Events[0].ID
	w = do(r, "GET", "/api/v1/events/"+id, tok, nil)
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`"details"`)) {
		t.Errorf("get event = %d: %s", w.Code, w.Body.String())
	}
}

func TestLibraryItemsList(t *testing.T) {
	r, api := setupAPI(t)
	tok := loginToken(t, r)

	lib := models.Library{Name: "L", Path: "/music", Enabled: true}
	if err := api.DB.Create(&lib).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	for _, p := range []string{"/music/a.flac", "/music/b.mp3"} {
		if err := api.DB.Create(&models.LibraryItem{LibraryID: lib.ID, Path: p, Status: "ok", MBReleaseID: "r"}).Error; err != nil {
			t.Fatalf("create item: %v", err)
		}
	}

	w := do(r, "GET", "/api/v1/library-items?library_id="+lib.ID.String(), tok, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("items = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Total int `json:"total"`
		Items []struct {
			Path string `json:"path"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 2 || len(resp.Items) != 2 {
		t.Errorf("expected 2 items, got total=%d len=%d", resp.Total, len(resp.Items))
	}

	// path filter
	w = do(r, "GET", "/api/v1/library-items?q=a.flac", tok, nil)
	if !bytes.Contains(w.Body.Bytes(), []byte("a.flac")) || bytes.Contains(w.Body.Bytes(), []byte("b.mp3")) {
		t.Errorf("path filter wrong: %s", w.Body.String())
	}
}

// TestEventDetailItems covers the endpoint the Activity detail view reads: opening a
// single event returns its per-file rows with the tag diff intact, while the feed
// itself stays free of them (50 events must not drag thousands of rows behind them).
func TestEventDetailItems(t *testing.T) {
	r, api := setupAPI(t)
	tok := loginToken(t, r)

	ev := events.Begin(api.DB, models.EventTypeProcess, "Library scan")
	events.Finish(api.DB, ev, models.EventStatusOK, "1 changed", map[string]any{"changed": 1})
	events.AddItems(api.DB, ev, []models.EventItem{{
		Path:        "/music/A/Album (2020)/01 One.flac",
		Status:      models.EventItemStatusChanged,
		TagsWritten: 1,
		Changes:     []models.TagChange{{Field: "ARTIST", Old: "Wrong", New: "Right"}},
	}})

	w := do(r, "GET", "/api/v1/events/"+ev.ID.String(), tok, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get event = %d: %s", w.Code, w.Body.String())
	}
	var got models.Event
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("event carried %d detail rows, want 1: %s", len(got.Items), w.Body.String())
	}
	item := got.Items[0]
	if item.Path == "" || item.Status != models.EventItemStatusChanged {
		t.Errorf("detail row wrong: %+v", item)
	}
	if len(item.Changes) != 1 || item.Changes[0].Old != "Wrong" || item.Changes[0].New != "Right" {
		t.Errorf("tag diff did not survive the API: %+v", item.Changes)
	}

	// The list endpoint stays lean.
	w = do(r, "GET", "/api/v1/events", tok, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list events = %d", w.Code)
	}
	if bytes.Contains(w.Body.Bytes(), []byte(`"changes"`)) {
		t.Errorf("the feed should not carry per-file detail: %s", w.Body.String())
	}
}

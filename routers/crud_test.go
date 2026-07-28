package routers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

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
	w := do(r, "GET", "/api/v1/scan/status", tok, nil)
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`"running":false`)) {
		t.Errorf("status = %d: %s", w.Code, w.Body.String())
	}

	// trigger returns 202 (no libraries -> a quick no-op run)
	if w := do(r, "POST", "/api/v1/scan", tok, nil); w.Code != http.StatusAccepted {
		t.Errorf("trigger scan = %d, want 202", w.Code)
	}

	// scanning a nonexistent library -> 404
	if w := do(r, "POST", "/api/v1/libraries/"+uuid.New().String()+"/scan", tok, nil); w.Code != http.StatusNotFound {
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

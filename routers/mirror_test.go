package routers

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestMirrorStatusRequiresAuth(t *testing.T) {
	r, _ := setupAPI(t)
	if w := do(r, "GET", "/api/v1/mirror/status", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// The status endpoint is what the UI polls during a multi-hour pass, so coverage
// has to be reported even when no pass has ever run.
func TestMirrorStatusReportsCoverageWhenIdle(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)

	w := do(r, "GET", "/api/v1/mirror/status", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var body struct {
		Running bool           `json:"running"`
		Cached  map[string]int `json:"cached"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Running {
		t.Error("no pass should be running")
	}
	if body.Cached == nil {
		t.Error("expected cache coverage in the response")
	}
}

// A pass is far too long to hold a request open for, so the trigger answers 202
// and reports through the status endpoint instead.
func TestTriggerMirrorAcceptsAndCancels(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)

	if w := do(r, "POST", "/api/v1/mirror/sync", token, nil); w.Code != http.StatusAccepted {
		t.Fatalf("sync = %d, want 202: %s", w.Code, w.Body.String())
	}

	// Cancelling is safe whether or not the (empty-collection) pass already
	// finished — a pass keeps no cursor to corrupt.
	if w := do(r, "POST", "/api/v1/mirror/cancel", token, nil); w.Code != http.StatusOK {
		t.Fatalf("cancel = %d, want 200: %s", w.Code, w.Body.String())
	}
}

// With no runner wired the endpoints report unavailable rather than panicking:
// API is constructed field by field, so a nil Mirror is a reachable state.
func TestMirrorEndpointsWithoutRunner(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	api.Mirror = nil

	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/mirror/status"},
		{"POST", "/api/v1/mirror/sync"},
		{"POST", "/api/v1/mirror/cancel"},
	} {
		w := do(r, tc.method, tc.path, token, nil)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d, want 503", tc.method, tc.path, w.Code)
		}
	}
}

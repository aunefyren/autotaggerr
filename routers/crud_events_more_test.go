package routers

import (
	"net/http"
	"testing"

	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
)

// TestGetEventUnknownAndFound covers both branches of the single-event fetch: a missing
// id is a 404, and an existing event returns 200 with its detail rows attached.
func TestGetEventUnknownAndFound(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	if w := do(r, "GET", "/api/v1/events/"+uuid.New().String(), token, nil); w.Code != http.StatusNotFound {
		t.Fatalf("unknown event status = %d, want 404", w.Code)
	}

	ev := events.Begin(api.DB, models.EventTypeScan, "test scan")
	events.Finish(api.DB, ev, models.EventStatusOK, "done", nil)
	if w := do(r, "GET", "/api/v1/events/"+ev.ID.String(), token, nil); w.Code != http.StatusOK {
		t.Fatalf("existing event status = %d, want 200: %s", w.Code, w.Body.String())
	}
}

// TestListEvents: the feed endpoint returns the events list.
func TestListEvents(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	events.Finish(api.DB, events.Begin(api.DB, models.EventTypeScan, "s"), models.EventStatusOK, "done", nil)

	if w := do(r, "GET", "/api/v1/events", token, nil); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
}

// TestCreateManagerValidation covers the create-manager guards and the happy path.
func TestCreateManagerValidation(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)

	if w := do(r, "POST", "/api/v1/managers", token, "not-an-object"); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid body status = %d, want 400", w.Code)
	}
	if w := do(r, "POST", "/api/v1/managers", token, map[string]any{"name": ""}); w.Code != http.StatusBadRequest {
		t.Fatalf("missing fields status = %d, want 400", w.Code)
	}
	if w := do(r, "POST", "/api/v1/managers", token, map[string]any{"name": "Native", "type": "autotaggerr"}); w.Code != http.StatusCreated {
		t.Fatalf("valid create status = %d, want 201: %s", w.Code, w.Body.String())
	}
}

// TestCreateTaggerProfileValidation covers the name guard and the happy path.
func TestCreateTaggerProfileValidation(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)

	if w := do(r, "POST", "/api/v1/tagger-profiles", token, map[string]any{}); w.Code != http.StatusBadRequest {
		t.Fatalf("missing name status = %d, want 400", w.Code)
	}
	if w := do(r, "POST", "/api/v1/tagger-profiles", token, map[string]any{"name": "P"}); w.Code != http.StatusCreated {
		t.Fatalf("valid create status = %d, want 201: %s", w.Code, w.Body.String())
	}
}

// TestDismissMigrationUnknown: dismissing a migration that does not exist is a 400.
func TestDismissMigrationUnknown(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)
	if w := do(r, "POST", "/api/v1/migrations/"+uuid.New().String()+"/dismiss", token, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
}

// TestVerifyIdentities: the sweep is enqueued and answered with 202 immediately.
func TestVerifyIdentities(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)
	if w := do(r, "POST", "/api/v1/migrations/verify", token, nil); w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", w.Code, w.Body.String())
	}
}

package routers

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
)

// TestEventsFeedEdges covers the feed's paging clamps and its count failure.
func TestEventsFeedEdges(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	events.Finish(api.DB, events.Begin(api.DB, models.EventTypeProcess, "run"), models.EventStatusOK, "done", nil)

	for _, tc := range []struct {
		query                 string
		wantLimit, wantOffset int
	}{
		{"limit=0&offset=-1", defaultEventsLimit, 0},
		{"limit=100000", maxEventsLimit, 0},
	} {
		got := decodeJSON[struct {
			Limit  int `json:"limit"`
			Offset int `json:"offset"`
			Total  int `json:"total"`
		}](t, r, "GET", "/api/v1/events?"+tc.query, token, nil)
		if got.Limit != tc.wantLimit || got.Offset != tc.wantOffset || got.Total != 1 {
			t.Errorf("%s: got %+v, want limit %d offset %d total 1", tc.query, got, tc.wantLimit, tc.wantOffset)
		}
	}

	w := do(r, "GET", "/api/v1/events/not-a-uuid", token, nil)
	expectStatus(t, "bad event id", w.Code, w.Body.String(), http.StatusBadRequest, "invalid id")

	dropTable(t, api.DB, &models.Event{})
	w = do(r, "GET", "/api/v1/events", token, nil)
	expectStatus(t, "count failure", w.Code, w.Body.String(), http.StatusInternalServerError, "failed to count events")
}

// TestGetEventWithoutItems: an event whose detail rows cannot be read still opens —
// the rows are the reason to open it, but the event itself is still worth showing.
func TestGetEventWithoutItems(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	ev := events.Begin(api.DB, models.EventTypeProcess, "run")
	events.Finish(api.DB, ev, models.EventStatusOK, "done", nil)
	dropTable(t, api.DB, &models.EventItem{})

	w := do(r, "GET", "/api/v1/events/"+ev.ID.String(), token, nil)
	expectStatus(t, "get event", w.Code, w.Body.String(), http.StatusOK, `"run"`)
}

// TestMBFilesEdges: a blank identifier is a 400; an unreadable editions table still
// answers with the identifier itself as the only candidate; an unreadable index is a
// 500.
func TestMBFilesEdges(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	w := do(r, "GET", "/api/v1/mb/%20/files", token, nil)
	expectStatus(t, "blank", w.Code, w.Body.String(), http.StatusBadRequest, "an identifier is required")

	lib := models.Library{Name: "L", Path: "/m", Enabled: true}
	if err := api.DB.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	if err := api.DB.Create(&models.LibraryItem{LibraryID: lib.ID, Path: "/m/a.flac", MBReleaseID: "rel-1", Status: models.LibraryItemStatusOK}).Error; err != nil {
		t.Fatalf("item: %v", err)
	}
	dropTable(t, api.DB, &models.CollectionRelease{})
	w = do(r, "GET", "/api/v1/mb/rel-1/files", token, nil)
	expectStatus(t, "editions unreadable", w.Code, w.Body.String(), http.StatusOK, `"total":1`)

	dropTable(t, api.DB, &models.LibraryItem{})
	w = do(r, "GET", "/api/v1/mb/rel-1/files", token, nil)
	expectStatus(t, "index unreadable", w.Code, w.Body.String(), http.StatusInternalServerError, "failed to count files")
}

// TestMigrationsEdges covers the list's limit clamp and failure, and the approve and
// dismiss id checks — including approving without a runner, which skips the
// manager-refresh path and goes straight to applying.
func TestMigrationsEdges(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	got := decodeJSON[struct {
		Limit int `json:"limit"`
	}](t, r, "GET", "/api/v1/migrations?limit=0", token, nil)
	if got.Limit != maxMigrationsLimit {
		t.Errorf("limit = %d, want the cap %d for a non-positive limit", got.Limit, maxMigrationsLimit)
	}

	w := do(r, "POST", "/api/v1/migrations/not-a-uuid/approve", token, nil)
	expectStatus(t, "approve bad id", w.Code, w.Body.String(), http.StatusBadRequest, "invalid id")
	w = do(r, "POST", "/api/v1/migrations/not-a-uuid/dismiss", token, nil)
	expectStatus(t, "dismiss bad id", w.Code, w.Body.String(), http.StatusBadRequest, "invalid id")

	// Unknown id: nothing to apply, and nothing recorded in the feed for it.
	w = do(r, "POST", "/api/v1/migrations/"+uuid.New().String()+"/approve", token, nil)
	expectStatus(t, "approve unknown", w.Code, w.Body.String(), http.StatusBadRequest, "")
	api.Scan = nil
	w = do(r, "POST", "/api/v1/migrations/"+uuid.New().String()+"/approve", token, nil)
	expectStatus(t, "approve unknown without runner", w.Code, w.Body.String(), http.StatusBadRequest, "")
	var n int64
	api.DB.Model(&models.Event{}).Where("type = ?", models.EventTypeMigration).Count(&n)
	if n != 0 {
		t.Errorf("migration events = %d, want none for a row that does not exist", n)
	}

	dropTable(t, api.DB, &models.MusicbrainzMigration{})
	w = do(r, "GET", "/api/v1/migrations", token, nil)
	expectStatus(t, "list failure", w.Code, w.Body.String(), http.StatusInternalServerError, "failed to list migrations")
}

// TestRecordMigrationDecision: a refused application is recorded as an error event
// carrying the reason, and an API without a database records nothing (no panic).
func TestRecordMigrationDecision(t *testing.T) {
	_, api := setupAPI(t)

	(&API{}).recordMigrationDecision(models.MusicbrainzMigration{Base: models.Base{ID: uuid.New()}}, models.EventItemStatusError, "x")

	row := models.MusicbrainzMigration{Base: models.Base{ID: uuid.New()}, OldMBID: "old-1", EntityType: models.MigrationEntityArtist}
	api.recordMigrationDecision(row, models.EventItemStatusError, "target is gone")

	var ev models.Event
	if err := api.DB.Where("type = ?", models.EventTypeMigration).First(&ev).Error; err != nil {
		t.Fatalf("no event recorded: %v", err)
	}
	if ev.Status != models.EventStatusError {
		t.Errorf("status = %q, want error", ev.Status)
	}
	// The name falls back to the old MBID, and the reason follows the label.
	if !strings.Contains(ev.Summary, "Artist · old-1 — target is gone") {
		t.Errorf("summary = %q, want the label, the MBID fallback and the reason", ev.Summary)
	}
}

// TestEntityLabel: the words the feed uses for each entity type, with an unknown type
// passed through rather than blanked.
func TestEntityLabel(t *testing.T) {
	for in, want := range map[string]string{
		models.MigrationEntityReleaseGroup: "Album",
		models.MigrationEntityArtist:       "Artist",
		models.MigrationEntityRelease:      "Release",
		"recording":                        "recording",
	} {
		if got := entityLabel(in); got != want {
			t.Errorf("entityLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRebuildAfterMigrationSwallowsFailure: the migration is already committed, so a
// rebuild that cannot run is logged, never surfaced.
func TestRebuildAfterMigrationSwallowsFailure(t *testing.T) {
	_, api := setupAPI(t)
	dropTable(t, api.DB, &models.LibraryItem{})
	api.rebuildAfterMigration()
}

// TestBackgroundVerbsWithoutTheirRunners: the artwork and mirror routes answer 503
// when their runner is not wired, rather than dereferencing nil.
func TestBackgroundVerbsWithoutTheirRunners(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	api.Artwork = nil
	api.Mirror = nil

	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/artwork/status"},
		{"POST", "/api/v1/artwork/refresh"},
		{"POST", "/api/v1/artwork/cancel"},
		{"POST", "/api/v1/mirror/sync"},
	} {
		w := do(r, tc.method, tc.path, token, nil)
		expectStatus(t, tc.method+" "+tc.path, w.Code, w.Body.String(), http.StatusServiceUnavailable, "unavailable")
	}
}

// TestIdentifyEdges covers the availability reasons reachable without fpcalc and the
// per-item id checks.
func TestIdentifyEdges(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	// An enabled source with no key is its own reason, ahead of the fpcalc check.
	enableAcoustID(t, api.DB, "")
	got := decodeJSON[acoustidAvailability](t, r, "GET", "/api/v1/identify", token, nil)
	if got.Available || !strings.Contains(got.Reason, "no API key") {
		t.Errorf("availability = %+v, want the missing-key reason", got)
	}

	w := do(r, "POST", "/api/v1/library-items/not-a-uuid/identify", token, nil)
	expectStatus(t, "bad id", w.Code, w.Body.String(), http.StatusBadRequest, "invalid id")
	w = do(r, "POST", "/api/v1/library-items/"+uuid.New().String()+"/identify", token, nil)
	expectStatus(t, "absent", w.Code, w.Body.String(), http.StatusNotFound, "library item not found")
}

// TestOIDCRedirectURIDerivation: the callback URL sent to the provider is the
// configured override when there is one, and otherwise rebuilt from what the browser
// used — TLS and forwarded headers included, since behind a proxy the request's own
// scheme and host are the internal ones.
func TestOIDCRedirectURIDerivation(t *testing.T) {
	r, api, _, provider := oidcSetup(t, true)

	start := func(mutate func(*http.Request)) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/"+provider.ID.String()+"/start", nil)
		mutate(req)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusFound {
			t.Fatalf("start = %d, want 302: %s", w.Code, w.Body.String())
		}
		loc, err := url.Parse(w.Header().Get("Location"))
		if err != nil {
			t.Fatalf("parse Location: %v", err)
		}
		return loc.Query().Get("redirect_uri")
	}

	if got := start(func(req *http.Request) {
		req.Host = "app.internal"
		req.TLS = &tls.ConnectionState{}
	}); !strings.HasPrefix(got, "https://app.internal/") {
		t.Errorf("TLS redirect_uri = %q, want https and the request host", got)
	}
	if got := start(func(req *http.Request) {
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("X-Forwarded-Host", "music.example.com")
	}); !strings.HasPrefix(got, "https://music.example.com/") {
		t.Errorf("forwarded redirect_uri = %q, want the forwarded scheme and host", got)
	}

	api.DB.Model(&provider).Update("redirect_url", "https://fixed.example.com/cb")
	if got := start(func(*http.Request) {}); got != "https://fixed.example.com/cb" {
		t.Errorf("override redirect_uri = %q, want the configured one", got)
	}
}

// TestOIDCCallbackProviderError: a provider that reports a refusal in-band sends the
// browser back to the login page with a message, not to a session.
func TestOIDCCallbackProviderError(t *testing.T) {
	r, _, _, provider := oidcSetup(t, true)

	w := callback(t, r, provider.ID.String(), "error=access_denied", nil)
	if w.Code != http.StatusFound {
		t.Fatalf("callback = %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); strings.Contains(loc, "token=") || !strings.Contains(loc, "/login") {
		t.Errorf("Location = %q, want the login page and no token", loc)
	}
}

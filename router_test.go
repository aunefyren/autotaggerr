package main

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/database"
	"github.com/aunefyren/autotaggerr/mirror"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/scan"
	"github.com/aunefyren/autotaggerr/web"
	"github.com/gin-gonic/gin"
)

// The router serves two things from one binary without either shadowing the other:
// a JSON API under /api, and the embedded single-page app everywhere else.
//
// The SPA fallback is what makes a deep link like /collection/<mbid>/<rgid> work on a
// cold load. The collection pages are real routes with real URLs — that is a
// deliberate choice, since they are browsing destinations — so a 404 there breaks
// refreshing and link sharing, which is exactly the case a dev server never hits.

func testRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	cfg := models.ConfigStruct{AutotaggerrName: "Autotaggerr", AutotaggerrVersion: "test"}
	scanRunner := scan.NewRunner(db, nil, cfg)
	return initRouter(db, scanRunner, mirror.NewRunner(db, func() bool { return scanRunner.Status().Running }), cfg)
}

func routerGet(t *testing.T, r *gin.Engine, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestRouterServesTheAPI(t *testing.T) {
	r := testRouter(t)

	// Both the legacy and the versioned liveness probes, since health checks in the
	// wild point at either.
	for _, path := range []string{"/api/ping", "/api/v1/ping"} {
		if w := routerGet(t, r, path); w.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, w.Code)
		}
	}
}

// TestRouterNeverHijacksAPIRoutes: an unknown /api path must stay JSON. Falling
// through to the SPA would have a client parse index.html as an API response and
// report something unrecognisable instead of a 404.
func TestRouterNeverHijacksAPIRoutes(t *testing.T) {
	r := testRouter(t)

	for _, path := range []string{"/api", "/api/", "/api/nope", "/api/v1/not-a-route"} {
		w := routerGet(t, r, path)
		if w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, w.Code)
		}
		if body := strings.ToLower(w.Body.String()); strings.Contains(body, "<html") || strings.Contains(body, "<!doctype") {
			t.Errorf("GET %s served the SPA instead of a JSON 404: %s", path, truncateBody(w.Body.String()))
		}
	}
}

// TestRouterFallsBackToTheSPA: every client-side route resolves to the SPA entry
// point, which is what makes a shared or refreshed deep link work.
func TestRouterFallsBackToTheSPA(t *testing.T) {
	r := testRouter(t)

	for _, path := range []string{
		"/",
		"/collection",
		"/collection/4b585938-f271-45e2-b19a-91c634b5e396",
		"/collection/4b585938-f271-45e2-b19a-91c634b5e396/f5093c06-23e3-404f-aeaa-40f72885ee3a",
		"/libraries",
	} {
		w := routerGet(t, r, path)
		if w.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 (the SPA entry point)", path, w.Code)
			continue
		}
		if !strings.Contains(w.Body.String(), `id="root"`) {
			t.Errorf("GET %s did not serve the SPA entry point: %s", path, truncateBody(w.Body.String()))
		}
	}
}

// TestRouterKeepsQueryStringsOffTheFallback: the browsing pages keep sort/filter
// state in the query string, so a deep link carries one. It must not change which
// document is served.
func TestRouterFallbackIgnoresQueryStrings(t *testing.T) {
	r := testRouter(t)
	w := routerGet(t, r, "/collection?q=bush&sort=missing&dir=desc")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `id="root"`) {
		t.Errorf("GET with a query string = %d: %s", w.Code, truncateBody(w.Body.String()))
	}
}

// TestRouterServesHashedAssets: the bundle's own files must be served as themselves,
// with a usable content type. Falling back to index.html for a real asset would have
// the browser parse HTML as JavaScript.
func TestRouterServesHashedAssets(t *testing.T) {
	r := testRouter(t)

	distFS, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		t.Fatalf("open embedded assets: %v", err)
	}
	entries, err := fs.ReadDir(distFS, "assets")
	if err != nil {
		t.Skipf("no embedded assets directory to test against: %v", err)
	}

	var js, css string
	for _, entry := range entries {
		switch {
		case js == "" && strings.HasSuffix(entry.Name(), ".js"):
			js = "assets/" + entry.Name()
		case css == "" && strings.HasSuffix(entry.Name(), ".css"):
			css = "assets/" + entry.Name()
		}
	}
	if js == "" || css == "" {
		t.Skip("the embedded bundle has no js/css pair to test against")
	}

	for path, wantType := range map[string]string{js: "javascript", css: "css"} {
		w := routerGet(t, r, "/"+path)
		if w.Code != http.StatusOK {
			t.Errorf("GET /%s = %d, want 200", path, w.Code)
			continue
		}
		if ctype := w.Header().Get("Content-Type"); !strings.Contains(ctype, wantType) {
			t.Errorf("GET /%s content type = %q, want it to mention %q", path, ctype, wantType)
		}
		if strings.Contains(w.Body.String(), `id="root"`) {
			t.Errorf("GET /%s served index.html instead of the asset", path)
		}
	}
}

func truncateBody(s string) string {
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}

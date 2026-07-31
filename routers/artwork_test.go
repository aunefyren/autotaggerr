package routers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// The artwork route is the one public route that talks to the outside world, so
// what it does when a provider is absent, present, or lying matters as much as the
// happy path. The provider itself is a local stub: these tests assert the handler's
// contract (status codes, headers, provider resolution), never the Cover Art
// Archive's behaviour.

var artworkJPEG = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F'}

const artworkTestMBID = "f5093c06-23e3-404f-aeaa-40f72885ee3a"

// artworkAPI builds an API whose artwork cache is a temporary directory, so the
// disk cache never leaks between tests or into the source tree.
func artworkAPI(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	// modules.GetArtwork caches under ./config/artwork, relative to the working
	// directory. Without this the cache would be written into the package folder
	// and, worse, shared between test cases.
	t.Chdir(t.TempDir())
	modules.ResetArtworkNegativeCache()
	r, api := setupAPI(t)
	return r, api.DB
}

func addDataSource(t *testing.T, db *gorm.DB, sourceType, baseURL, apiKey string, enabled bool) {
	t.Helper()
	if err := db.Create(&models.DataSource{
		Name: sourceType, Type: sourceType, BaseURL: baseURL, APIKey: apiKey, Enabled: enabled,
	}).Error; err != nil {
		t.Fatalf("create %s data source: %v", sourceType, err)
	}
}

func getArtwork(r *gin.Engine, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestArtworkIsPublic(t *testing.T) {
	r, db := artworkAPI(t)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(artworkJPEG)
	}))
	defer stub.Close()
	addDataSource(t, db, models.DataSourceTypeCoverArtArchive, stub.URL, "", true)

	// No Authorization header, by design: an <img> tag cannot send one, so a 401
	// here would mean no cover ever renders.
	w := getArtwork(r, "/api/v1/artwork/release-group/"+artworkTestMBID)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "image/jpeg" {
		t.Errorf("content type = %q, want image/jpeg", got)
	}
	if got := w.Header().Get("Cache-Control"); got == "" {
		t.Error("no Cache-Control — a page of covers would re-ask on every navigation")
	}
	if w.Body.Len() != len(artworkJPEG) {
		t.Errorf("body = %d bytes, want %d", w.Body.Len(), len(artworkJPEG))
	}
}

func TestArtworkServesFromCacheOnRepeat(t *testing.T) {
	r, db := artworkAPI(t)
	calls := 0
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(artworkJPEG)
	}))
	defer stub.Close()
	addDataSource(t, db, models.DataSourceTypeCoverArtArchive, stub.URL, "", true)

	path := "/api/v1/artwork/release-group/" + artworkTestMBID
	if w := getArtwork(r, path); w.Code != http.StatusOK {
		t.Fatalf("first request: %d", w.Code)
	}
	second := getArtwork(r, path)
	if second.Code != http.StatusOK {
		t.Fatalf("second request: %d", second.Code)
	}
	if got := second.Header().Get("X-Artwork-Cache"); got != "hit" {
		t.Errorf("X-Artwork-Cache = %q, want hit", got)
	}
	if calls != 1 {
		t.Errorf("upstream calls = %d, want 1", calls)
	}
}

func TestArtworkWithoutProviderIs404(t *testing.T) {
	r, _ := artworkAPI(t)
	// No data source rows at all: unconfigured is "no image", which the UI renders
	// as a monogram. A 5xx here would light up an error state for a normal setup.
	w := getArtwork(r, "/api/v1/artwork/release-group/"+artworkTestMBID)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404: %s", w.Code, w.Body.String())
	}
}

func TestArtworkNegativeIsCacheable(t *testing.T) {
	r, _ := artworkAPI(t)
	// "No image" is a stable answer, and the browser must be told so: without a
	// Cache-Control header a coverless collection re-asks on every navigation, and a
	// reverse proxy watching for 404 floods can ban the client for its own traffic.
	w := getArtwork(r, "/api/v1/artwork/release-group/"+artworkTestMBID)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got == "" {
		t.Error("404 has no Cache-Control — the browser will re-ask on every navigation")
	}
}

func TestArtworkIgnoresDisabledProvider(t *testing.T) {
	r, db := artworkAPI(t)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a disabled data source was contacted")
	}))
	defer stub.Close()
	addDataSource(t, db, models.DataSourceTypeCoverArtArchive, stub.URL, "", false)

	if w := getArtwork(r, "/api/v1/artwork/release-group/"+artworkTestMBID); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestArtworkArtistNeedsFanart(t *testing.T) {
	r, db := artworkAPI(t)
	// The Cover Art Archive has no artist entity, so a cover source alone must not
	// make artist images look available.
	addDataSource(t, db, models.DataSourceTypeCoverArtArchive, "http://127.0.0.1:1", "", true)

	if w := getArtwork(r, "/api/v1/artwork/artist/"+artworkTestMBID); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestArtworkArtistThumbFromFanart(t *testing.T) {
	r, db := artworkAPI(t)
	images := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(artworkJPEG)
	}))
	defer images.Close()

	var sawKey string
	index := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		sawKey = req.URL.Query().Get("api_key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"artistthumb":[{"id":"1","url":"` + images.URL + `/t.jpg","likes":"5"}]}`))
	}))
	defer index.Close()
	addDataSource(t, db, models.DataSourceTypeFanart, index.URL, "secret-key", true)

	w := getArtwork(r, "/api/v1/artwork/artist/"+artworkTestMBID)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if sawKey != "secret-key" {
		t.Errorf("fanart api_key = %q, want secret-key", sawKey)
	}
	// The key is a server-side secret: it is sent to fanart.tv and must never come
	// back out towards the browser.
	if got := w.Header().Get("X-Api-Key"); got != "" {
		t.Errorf("response leaked a key header: %q", got)
	}
}

func TestArtworkRejectsMalformedRequests(t *testing.T) {
	r, db := artworkAPI(t)
	addDataSource(t, db, models.DataSourceTypeCoverArtArchive, "http://127.0.0.1:1", "", true)

	cases := []struct {
		name, path string
		want       int
	}{
		// Not a MusicBrainz ID: no provider is contacted, so this is the caller's
		// mistake and not a bad gateway.
		{"non-uuid", "/api/v1/artwork/release-group/nope", http.StatusBadRequest},
		{"impossible kind", "/api/v1/artwork/artist/" + artworkTestMBID + "?kind=front", http.StatusBadRequest},
		{"unknown entity", "/api/v1/artwork/label/" + artworkTestMBID, http.StatusBadRequest},
		{"negative size", "/api/v1/artwork/release-group/" + artworkTestMBID + "?size=-4", http.StatusBadRequest},
		{"non-numeric size", "/api/v1/artwork/release-group/" + artworkTestMBID + "?size=big", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if w := getArtwork(r, tc.path); w.Code != tc.want {
				t.Errorf("status = %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

func TestArtworkReportsProviderFailure(t *testing.T) {
	r, db := artworkAPI(t)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream exploded", http.StatusInternalServerError)
	}))
	defer stub.Close()
	addDataSource(t, db, models.DataSourceTypeCoverArtArchive, stub.URL, "", true)

	// A broken provider is distinguishable from one that simply has no image: the
	// UI falls back either way, but only one of them is worth investigating.
	w := getArtwork(r, "/api/v1/artwork/release-group/"+artworkTestMBID)
	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502: %s", w.Code, w.Body.String())
	}
}

func TestArtworkClampsHugeSize(t *testing.T) {
	r, db := artworkAPI(t)
	var requested string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requested = req.URL.Path
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(artworkJPEG)
	}))
	defer stub.Close()
	addDataSource(t, db, models.DataSourceTypeCoverArtArchive, stub.URL, "", true)

	if w := getArtwork(r, "/api/v1/artwork/release-group/"+artworkTestMBID+"?size=99999"); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	// Clamped to a size the archive actually serves, rather than passed through as
	// a request for an unbounded original.
	if want := "/release-group/" + artworkTestMBID + "/front-1200"; requested != want {
		t.Errorf("upstream path = %q, want %q", requested, want)
	}
}

func TestArtworkProvidersResolution(t *testing.T) {
	_, db := artworkAPI(t)

	if p := artworkProviders(db); p.CoverArtEnabled || p.FanartEnabled {
		t.Errorf("empty database resolved providers: %+v", p)
	}

	addDataSource(t, db, models.DataSourceTypeCoverArtArchive, "https://caa.example", "", true)
	addDataSource(t, db, models.DataSourceTypeFanart, "https://fanart.example", "k", true)
	p := artworkProviders(db)
	if !p.CoverArtEnabled || p.CoverArtBaseURL != "https://caa.example" {
		t.Errorf("cover art not resolved: %+v", p)
	}
	if !p.FanartEnabled || p.FanartAPIKey != "k" {
		t.Errorf("fanart not resolved: %+v", p)
	}
}

func TestArtworkCapabilities(t *testing.T) {
	// The endpoint is what lets the UI skip requests that could only 404, so it must
	// report exactly what the artwork handler can deliver: a disabled — or, for
	// fanart, keyless — provider is not a capability, however configured it looks.
	cases := []struct {
		name                  string
		setup                 func(t *testing.T, db *gorm.DB)
		wantCover, wantArtist bool
	}{
		{"nothing configured", func(_ *testing.T, _ *gorm.DB) {}, false, false},
		{"cover only", func(t *testing.T, db *gorm.DB) {
			addDataSource(t, db, models.DataSourceTypeCoverArtArchive, "https://caa.example", "", true)
		}, true, false},
		{"fanart with key", func(t *testing.T, db *gorm.DB) {
			addDataSource(t, db, models.DataSourceTypeFanart, "https://fanart.example", "k", true)
		}, false, true},
		{"fanart without key cannot serve", func(t *testing.T, db *gorm.DB) {
			addDataSource(t, db, models.DataSourceTypeFanart, "https://fanart.example", "", true)
		}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, db := artworkAPI(t)
			tc.setup(t, db)
			token := loginToken(t, r)

			w := do(r, "GET", "/api/v1/artwork-capabilities", token, nil)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
			}
			var got struct {
				Cover  bool `json:"cover"`
				Artist bool `json:"artist"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v (%s)", err, w.Body.String())
			}
			if got.Cover != tc.wantCover || got.Artist != tc.wantArtist {
				t.Errorf("caps = %+v, want cover=%v artist=%v", got, tc.wantCover, tc.wantArtist)
			}
		})
	}
}

func TestArtworkCapabilitiesRequiresAuth(t *testing.T) {
	r, _ := artworkAPI(t)
	if w := do(r, "GET", "/api/v1/artwork-capabilities", "", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

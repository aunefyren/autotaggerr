package modules

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// withMockMB points the MusicBrainz client at a local test server and resets the
// cache + rate limiter so tests are isolated and don't incur the 1s throttle.
func withMockMB(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	orig := musicbrainzBaseURL
	musicbrainzBaseURL = srv.URL
	t.Cleanup(func() { musicbrainzBaseURL = orig })

	musicbrainzReleaseCacheMu.Lock()
	musicbrainzReleaseCache = map[string]models.CachedMusicBrainzRelease{}
	musicbrainzReleaseCacheMu.Unlock()

	queryMutex.Lock()
	lastQueryTime = time.Time{} // ensure the next RateLimit() call doesn't sleep
	queryMutex.Unlock()

	return srv
}

func TestQueryMusicBrainzReleaseData(t *testing.T) {
	want := models.MusicBrainzReleaseResponse{
		ID:    "rel-1",
		Title: "OK Computer",
		Media: []models.MusicBrainzMedia{
			{Tracks: []models.Track{{ID: "trk-1", Title: "Airbag"}}},
		},
	}

	var gotUA, gotPath string
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(want)
	})

	got, err := QueryMusicBrainzReleaseData("rel-1", "9.9.9")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "rel-1" || got.Title != "OK Computer" {
		t.Errorf("parsed release = %+v", got)
	}
	if len(got.Media) != 1 || len(got.Media[0].Tracks) != 1 || got.Media[0].Tracks[0].ID != "trk-1" {
		t.Errorf("parsed media/tracks = %+v", got.Media)
	}
	if !strings.HasPrefix(gotUA, "Autotaggerr/9.9.9") {
		t.Errorf("User-Agent = %q, want prefix Autotaggerr/9.9.9", gotUA)
	}
	if !strings.Contains(gotPath, "/release/rel-1") {
		t.Errorf("request path = %q, want to contain /release/rel-1", gotPath)
	}
}

// TestGetMusicBrainzReleaseCaches confirms the release is fetched once and then
// served from the in-memory cache, and that the entry gets a future (jittered)
// expiry.
func TestGetMusicBrainzReleaseCaches(t *testing.T) {
	var hits int32
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_ = json.NewEncoder(w).Encode(models.MusicBrainzReleaseResponse{ID: "rel-2", Title: "Kid A"})
	})

	for i := 0; i < 3; i++ {
		got, err := GetMusicBrainzRelease("rel-2")
		if err != nil {
			t.Fatalf("call %d unexpected error: %v", i, err)
		}
		if got.Title != "Kid A" {
			t.Fatalf("call %d title = %q, want Kid A", i, got.Title)
		}
	}

	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("server hit %d times, want 1 (subsequent calls should be cached)", n)
	}

	musicbrainzReleaseCacheMu.RLock()
	entry, ok := musicbrainzReleaseCache["rel-2"]
	musicbrainzReleaseCacheMu.RUnlock()
	if !ok {
		t.Fatal("release not stored in cache")
	}
	if !entry.ExpiresAt.After(time.Now()) {
		t.Errorf("cache entry ExpiresAt %v is not in the future", entry.ExpiresAt)
	}
	// jitter keeps expiry within [now+7d, now+14d]
	if entry.ExpiresAt.After(time.Now().Add(15 * 24 * time.Hour)) {
		t.Errorf("cache entry ExpiresAt %v exceeds the jitter window", entry.ExpiresAt)
	}
}

func TestGetMusicBrainzReleaseHTTPError(t *testing.T) {
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	_, err := GetMusicBrainzRelease("rel-3")
	if err == nil {
		t.Fatal("expected error on HTTP 500, got nil")
	}
	// the real status must survive to the caller, not be replaced by a generic message
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error %q should mention the HTTP status", err.Error())
	}
}

// TestGetMusicBrainzReleaseNotFound covers the "stale Lidarr MB ID" case: a 404
// must produce a clearly distinguishable, actionable error.
func TestGetMusicBrainzReleaseNotFound(t *testing.T) {
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Not Found"}`))
	})

	_, err := GetMusicBrainzRelease("gone-id")
	if err == nil {
		t.Fatal("expected error on HTTP 404, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "not found") || !strings.Contains(msg, "stale") {
		t.Errorf("404 error %q should flag a not-found / stale MB ID", msg)
	}
	if !strings.Contains(msg, "gone-id") {
		t.Errorf("404 error %q should name the release ID", msg)
	}
}

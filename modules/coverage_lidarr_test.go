package modules

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// Each link in the Lidarr resolution chain (artist → trackfile → track → monitored
// release) can fail or come back empty, and the two must be told apart: a failure
// is an error naming the link, an empty answer is "try the next artist", and only
// when every artist is exhausted is the file unmatched (nil, nil).

func jsonRoute(v any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
}

func rawRoute(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// pinkFloydRoutes is a Lidarr that resolves the test file end to end; each case
// below breaks one route.
func pinkFloydRoutes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"/api/v1/artist": jsonRoute([]models.LidarrArtist{
			{ID: 2, Name: "Pink Floyd", Path: "/data/music/Pink Floyd"},
		}),
		"/api/v1/trackfile": jsonRoute([]models.LidarrTrackFile{
			{ID: 100, AlbumID: 42, ArtistID: 2, Path: "/data/music/Pink Floyd/The Wall (1979)/01 In the Flesh.flac"},
		}),
		"/api/v1/track": jsonRoute([]models.LidarrTrack{
			{ID: 499, Title: "No file"},
			{ID: 500, Title: "In the Flesh?", ForeignTrackID: "mbtrack-1", TrackFileID: i64ptr(100)},
		}),
		"/api/v1/album": jsonRoute([]models.LidarrAlbum{
			{ID: 42, ArtistID: 2, Releases: []models.LidarrAlbumRel{{ID: 2, Monitored: true, ForeignReleaseID: "mbrelease-1"}}},
		}),
	}
}

func lidarrRoutesServer(t *testing.T, routes map[string]http.HandlerFunc) *LidarrClient {
	t.Helper()
	mux := http.NewServeMux()
	for p, h := range routes {
		mux.HandleFunc(p, h)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return NewLidarrClient(srv.URL, "test-key", nil)
}

func TestResolveMetadataDetailsFromLidarrEachLink(t *testing.T) {
	root := filepath.Join("/", "music")
	trackPath := filepath.Join(root, "Pink Floyd", "The Wall (1979)", "01 In the Flesh.flac")

	cases := []struct {
		name     string
		override map[string]http.HandlerFunc
		wantErr  string // "" means no error
		wantNil  bool   // unmatched: (nil, nil)
	}{
		{"artist lookup fails", map[string]http.HandlerFunc{"/api/v1/artist": rawRoute(500, "down")}, "artist lookup for", false},
		{"trackfile lookup fails", map[string]http.HandlerFunc{"/api/v1/trackfile": rawRoute(500, "down")}, "track file lookup", false},
		{"no trackfiles", map[string]http.HandlerFunc{"/api/v1/trackfile": jsonRoute([]models.LidarrTrackFile{})}, "", true},
		{"track list fails", map[string]http.HandlerFunc{"/api/v1/track": rawRoute(500, "down")}, "track list lookup", false},
		{"track list null", map[string]http.HandlerFunc{"/api/v1/track": rawRoute(200, "null")}, "", true},
		{"track list empty", map[string]http.HandlerFunc{"/api/v1/track": jsonRoute([]models.LidarrTrack{})}, "", true},
		{"album lookup fails", map[string]http.HandlerFunc{"/api/v1/album": rawRoute(500, "down")}, "monitored release lookup", false},
		{"nothing monitored", map[string]http.HandlerFunc{"/api/v1/album": jsonRoute([]models.LidarrAlbum{
			{ID: 42, Title: "The Wall", Releases: []models.LidarrAlbumRel{{ID: 2, ForeignReleaseID: "x"}}},
		})}, "", true},
		{"album missing from answer", map[string]http.HandlerFunc{"/api/v1/album": jsonRoute([]models.LidarrAlbum{{ID: 7}})}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetLidarrCaches()
			t.Cleanup(resetLidarrCaches)
			routes := pinkFloydRoutes()
			for p, h := range tc.override {
				routes[p] = h
			}
			cli := lidarrRoutesServer(t, routes)

			details, err := ResolveMetadataDetailsFromLidarr(cli, trackPath, root)
			switch {
			case tc.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("err = %v, want it to name %q", err, tc.wantErr)
				}
			case tc.wantNil:
				if err != nil || details != nil {
					t.Errorf("got (%+v, %v), want unmatched (nil, nil)", details, err)
				}
			}
		})
	}
}

func TestResolveMetadataDetailsFromLidarrBadPath(t *testing.T) {
	cli := NewLidarrClient("http://127.0.0.1:1", "k", nil)
	// A file directly under the root has no artist folder to look up.
	_, err := ResolveMetadataDetailsFromLidarr(cli, filepath.Join("/", "music", "loose.flac"), filepath.Join("/", "music"))
	if err == nil || !strings.Contains(err.Error(), "could not read an artist folder") {
		t.Errorf("err = %v, want the path layout named", err)
	}
}

// Two Lidarr artists sharing a folder are both returned; the caller tries each.
func TestLidarrFindArtistByNameMultipleMatches(t *testing.T) {
	resetLidarrCaches()
	t.Cleanup(resetLidarrCaches)
	cli := lidarrRoutesServer(t, map[string]http.HandlerFunc{
		"/api/v1/artist": jsonRoute([]models.LidarrArtist{
			{ID: 1, Name: "Nirvana", Path: "/a/Nirvana"},
			{ID: 2, Name: "Nirvana (UK)", Path: "/b/Nirvana"},
			{ID: 3, Name: "Other", Path: "/a/Other"},
		}),
	})
	got, err := cli.FindArtistByName("Nirvana")
	if err != nil || len(got) != 2 {
		t.Fatalf("FindArtistByName = (%+v, %v), want both Nirvana folders", got, err)
	}
}

func TestLidarrTrackFilesAndTracksAreCached(t *testing.T) {
	resetLidarrCaches()
	t.Cleanup(resetLidarrCaches)
	var hits int64
	inner := pinkFloydRoutes()["/api/v1/trackfile"]
	cli := lidarrRoutesServer(t, map[string]http.HandlerFunc{
		"/api/v1/trackfile": func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt64(&hits, 1)
			inner(w, r)
		},
	})

	for i := 0; i < 2; i++ {
		files, err := cli.getTrackFilesByArtist(2)
		if err != nil || len(files) != 1 {
			t.Fatalf("getTrackFilesByArtist = (%v, %v)", files, err)
		}
	}
	if n := atomic.LoadInt64(&hits); n != 1 {
		t.Errorf("trackfile requests = %d, want 1 — the per-artist cache did not hold", n)
	}
}

// A cached album whose releases are all unmonitored is not an answer; it is
// re-asked, so an edition picked in Lidarr since is seen.
func TestLidarrGetMonitoredAlbumMBIDCachedButUnmonitored(t *testing.T) {
	resetLidarrCaches()
	t.Cleanup(resetLidarrCaches)
	lidarrAlbumsCacheMu.Lock()
	lidarrAlbumsCache["42"] = models.CachedLidarrAlbumRelease{
		Album:     models.LidarrAlbum{ID: 42, Releases: []models.LidarrAlbumRel{{ID: 1, ForeignReleaseID: "old"}}},
		Timestamp: time.Now(),
	}
	lidarrAlbumsCacheMu.Unlock()

	cli := lidarrRoutesServer(t, pinkFloydRoutes())
	got, err := cli.GetMonitoredAlbumMBID(2, 42)
	if err != nil || got == nil || *got != "mbrelease-1" {
		t.Errorf("GetMonitoredAlbumMBID = (%v, %v), want the freshly monitored release", got, err)
	}
}

func TestLidarrListEndpointsReportFailures(t *testing.T) {
	resetLidarrCaches()
	t.Cleanup(resetLidarrCaches)
	cli := lidarrRoutesServer(t, map[string]http.HandlerFunc{
		"/api/v1/artist": rawRoute(503, "busy"),
		"/api/v1/album":  rawRoute(503, "busy"),
	})
	if _, err := cli.GetArtists(); err == nil {
		t.Error("GetArtists swallowed a 503")
	}
	if _, err := cli.GetArtistAlbums(1); err == nil {
		t.Error("GetArtistAlbums swallowed a 503")
	}
}

func TestLidarrGetJSONLocalFailuresAndRateLimit(t *testing.T) {
	var dst any
	bad := NewLidarrClient("http://bad host", "k", nil)
	if err := bad.getJSON("/x", &dst); err == nil || !strings.Contains(err.Error(), "could not build request") {
		t.Errorf("unparseable URL err = %v", err)
	}

	srv := httptest.NewServer(http.NotFoundHandler())
	dead := NewLidarrClient(srv.URL, "k", nil)
	srv.Close()
	if err := dead.getJSON("/x", &dst); err == nil || !strings.Contains(err.Error(), "request failed") {
		t.Errorf("dead server err = %v", err)
	}

	cli := lidarrRoutesServer(t, map[string]http.HandlerFunc{"/ok": jsonRoute(map[string]int{"a": 1})})
	limited := 0
	cli.RateLimit = func(f func() error) error { limited++; return f() }
	if err := cli.getJSON("/ok", &dst); err != nil || limited != 1 {
		t.Errorf("getJSON via limiter = %v, limiter ran %d times", err, limited)
	}
}

func TestMediaFoldersAgreeOneSideEmpty(t *testing.T) {
	if mediaFoldersAgree("", "CD1") || mediaFoldersAgree("Disc 2", "") {
		t.Error("a media folder on only one side must not agree")
	}
	if !mediaFoldersAgree("CD2", "Disc 02") {
		t.Error("the same disc number in different spellings must agree")
	}
}

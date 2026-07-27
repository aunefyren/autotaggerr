package modules

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// resetLidarrCaches clears the package-global Lidarr cache maps so each test
// starts from a clean slate (these maps are shared process-wide).
func resetLidarrCaches() {
	lidarrArtistsCacheMu.Lock()
	lidarrArtistsCache = map[string]models.CachedLidarrArtistRelease{}
	lidarrArtistsCacheMu.Unlock()
	lidarrAlbumsCacheMu.Lock()
	lidarrAlbumsCache = map[string]models.CachedLidarrAlbumRelease{}
	lidarrAlbumsCacheMu.Unlock()
	lidarrTracksCacheMu.Lock()
	lidarrTracksCache = map[string]models.CachedLidarrTracksRelease{}
	lidarrTracksCacheMu.Unlock()
	lidarrTrackFilesCacheMu.Lock()
	lidarrTrackFilesCache = map[string]models.CachedLidarrTrackFilesRelease{}
	lidarrTrackFilesCacheMu.Unlock()
}

// lidarrMock is an httptest server that serves canned Lidarr API responses and
// counts hits per path so caching behavior can be asserted.
type lidarrMock struct {
	server *httptest.Server
	mu     sync.Mutex
	hits   map[string]int
}

func newLidarrMock(t *testing.T, routes map[string]any) *lidarrMock {
	t.Helper()
	m := &lidarrMock{hits: map[string]int{}}
	mux := http.NewServeMux()
	for path, body := range routes {
		body := body
		p := path
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
			m.mu.Lock()
			m.hits[p]++
			m.mu.Unlock()
			if r.Header.Get("X-Api-Key") == "" {
				t.Errorf("request to %s missing X-Api-Key header", p)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(body)
		})
	}
	m.server = httptest.NewServer(mux)
	t.Cleanup(m.server.Close)
	return m
}

func (m *lidarrMock) hitCount(path string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hits[path]
}

func i64ptr(v int64) *int64 { return &v }

func TestLidarrFindArtistByName(t *testing.T) {
	resetLidarrCaches()
	mock := newLidarrMock(t, map[string]any{
		"/api/v1/artist": []models.LidarrArtist{
			{ID: 1, Name: "Radiohead", Path: "/data/music/Radiohead"},
			{ID: 2, Name: "Pink Floyd", Path: "/data/music/Pink Floyd"},
		},
	})
	client := NewLidarrClient(mock.server.URL, "test-key", nil)

	got, err := client.FindArtistByName("Pink Floyd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("expected artist id 2, got %+v", got)
	}

	// Second call must be served from cache (no extra server hit) — this is the
	// behavior the inverted-cache bug used to break.
	if _, err := client.FindArtistByName("Pink Floyd"); err != nil {
		t.Fatalf("unexpected error on cached call: %v", err)
	}
	if n := mock.hitCount("/api/v1/artist"); n != 1 {
		t.Errorf("/api/v1/artist hit %d times, want 1 (second call should be cached)", n)
	}
}

func TestLidarrFindArtistByNameNotFound(t *testing.T) {
	resetLidarrCaches()
	mock := newLidarrMock(t, map[string]any{
		"/api/v1/artist": []models.LidarrArtist{{ID: 1, Name: "Radiohead", Path: "/data/music/Radiohead"}},
	})
	client := NewLidarrClient(mock.server.URL, "test-key", nil)

	if _, err := client.FindArtistByName("Nonexistent"); err == nil {
		t.Error("expected error for artist not found")
	}
}

func TestLidarrFindTrackFileByPath(t *testing.T) {
	resetLidarrCaches()
	root := filepath.Join("/", "music")
	trackPath := filepath.Join(root, "Pink Floyd", "The Wall (1979)", "01 In the Flesh.flac")

	mock := newLidarrMock(t, map[string]any{
		"/api/v1/trackfile": []models.LidarrTrackFile{
			{ID: 100, AlbumID: 42, ArtistID: 2, Path: "/data/music/Pink Floyd/The Wall (1979)/01 In the Flesh.flac"},
			{ID: 101, AlbumID: 42, ArtistID: 2, Path: "/data/music/Pink Floyd/The Wall (1979)/02 The Thin Ice.flac"},
		},
	})
	client := NewLidarrClient(mock.server.URL, "test-key", nil)

	tf, err := client.FindTrackFileByPath(2, trackPath, root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tf == nil || tf.ID != 100 {
		t.Fatalf("expected trackfile 100, got %+v", tf)
	}

	// per-artist cache: a second lookup for the same artist must not re-hit.
	if _, err := client.FindTrackFileByPath(2, trackPath, root); err != nil {
		t.Fatalf("unexpected error on cached call: %v", err)
	}
	if n := mock.hitCount("/api/v1/trackfile"); n != 1 {
		t.Errorf("/api/v1/trackfile hit %d times, want 1", n)
	}
}

// TestLidarrFindTrackFileByPathMediaFolder covers the layout with an extra media
// subdir (…/Album (2020)/CD1/track), where the album is the *grandparent* of the
// file rather than its immediate parent.
func TestLidarrFindTrackFileByPathMediaFolder(t *testing.T) {
	resetLidarrCaches()
	root := filepath.Join("/", "music")
	trackPath := filepath.Join(root, "Pink Floyd", "The Wall (1979)", "CD1", "01 In the Flesh.flac")

	mock := newLidarrMock(t, map[string]any{
		"/api/v1/trackfile": []models.LidarrTrackFile{
			{ID: 100, AlbumID: 42, ArtistID: 2, Path: "/data/music/Pink Floyd/The Wall (1979)/CD1/01 In the Flesh.flac"},
		},
	})
	client := NewLidarrClient(mock.server.URL, "test-key", nil)

	tf, err := client.FindTrackFileByPath(2, trackPath, root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tf == nil || tf.ID != 100 {
		t.Fatalf("expected trackfile 100 via media-folder match, got %+v", tf)
	}
}

func TestLidarrFindTrackFileByPathNoMatch(t *testing.T) {
	resetLidarrCaches()
	root := filepath.Join("/", "music")
	trackPath := filepath.Join(root, "Pink Floyd", "The Wall (1979)", "01 In the Flesh.flac")

	mock := newLidarrMock(t, map[string]any{
		"/api/v1/trackfile": []models.LidarrTrackFile{
			{ID: 100, AlbumID: 42, ArtistID: 2, Path: "/data/music/Pink Floyd/Wish You Were Here (1975)/03 Have a Cigar.flac"},
		},
	})
	client := NewLidarrClient(mock.server.URL, "test-key", nil)

	// No matching trackfile -> (nil, nil): not an error, just no match.
	tf, err := client.FindTrackFileByPath(2, trackPath, root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tf != nil {
		t.Errorf("expected no match (nil), got %+v", tf)
	}
}

func TestLidarrGetTracksByAlbumAndArtistIDCaches(t *testing.T) {
	resetLidarrCaches()
	mock := newLidarrMock(t, map[string]any{
		"/api/v1/track": []models.LidarrTrack{
			{ID: 500, Title: "In the Flesh?", ForeignTrackID: "mbtrack-1", TrackFileID: i64ptr(100)},
		},
	})
	client := NewLidarrClient(mock.server.URL, "test-key", nil)

	for i := 0; i < 3; i++ {
		tracks, err := client.GetTracksByAlbumAndArtistID(2, 42)
		if err != nil {
			t.Fatalf("call %d error: %v", i, err)
		}
		if len(tracks) != 1 || tracks[0].ID != 500 {
			t.Fatalf("call %d tracks = %+v", i, tracks)
		}
	}
	if n := mock.hitCount("/api/v1/track"); n != 1 {
		t.Errorf("/api/v1/track hit %d times, want 1 (cached after first)", n)
	}
}

func TestLidarrGetMonitoredAlbumMBID(t *testing.T) {
	resetLidarrCaches()
	mock := newLidarrMock(t, map[string]any{
		"/api/v1/album": []models.LidarrAlbum{
			{ID: 42, ArtistID: 2, Releases: []models.LidarrAlbumRel{
				{ID: 1, Monitored: false, ForeignReleaseID: "unmonitored-rel"},
				{ID: 2, Monitored: true, ForeignReleaseID: "monitored-rel"},
			}},
		},
	})
	client := NewLidarrClient(mock.server.URL, "test-key", nil)

	mbid, err := client.GetMonitoredAlbumMBID(2, 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mbid == nil || *mbid != "monitored-rel" {
		t.Fatalf("expected 'monitored-rel', got %v", mbid)
	}
}

// TestResolveMetadataDetailsFromLidarr exercises the full resolution chain
// (artist -> trackfile -> track -> monitored release) against a mock Lidarr — the
// core of the per-file pipeline.
func TestResolveMetadataDetailsFromLidarr(t *testing.T) {
	resetLidarrCaches()
	root := filepath.Join("/", "music")
	trackPath := filepath.Join(root, "Pink Floyd", "The Wall (1979)", "01 In the Flesh.flac")

	mock := newLidarrMock(t, map[string]any{
		"/api/v1/artist": []models.LidarrArtist{
			{ID: 2, Name: "Pink Floyd", Path: "/data/music/Pink Floyd"},
		},
		"/api/v1/trackfile": []models.LidarrTrackFile{
			{ID: 100, AlbumID: 42, ArtistID: 2, Path: "/data/music/Pink Floyd/The Wall (1979)/01 In the Flesh.flac"},
		},
		"/api/v1/track": []models.LidarrTrack{
			{ID: 500, Title: "In the Flesh?", ForeignTrackID: "mbtrack-1", ForeignRecordingID: "mbrec-1", TrackFileID: i64ptr(100)},
			{ID: 501, Title: "The Thin Ice", ForeignTrackID: "mbtrack-2", ForeignRecordingID: "mbrec-2", TrackFileID: i64ptr(101)},
		},
		"/api/v1/album": []models.LidarrAlbum{
			{ID: 42, ArtistID: 2, Releases: []models.LidarrAlbumRel{
				{ID: 2, Monitored: true, ForeignReleaseID: "mbrelease-1"},
			}},
		},
	})
	client := NewLidarrClient(mock.server.URL, "test-key", nil)

	details, err := ResolveMetadataDetailsFromLidarr(client, trackPath, root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if details == nil {
		t.Fatal("expected details, got nil")
	}
	if details.MBTrackID != "mbtrack-1" {
		t.Errorf("MBTrackID = %q, want mbtrack-1", details.MBTrackID)
	}
	if details.MBRecordingID != "mbrec-1" {
		t.Errorf("MBRecordingID = %q, want mbrec-1", details.MBRecordingID)
	}
	if details.MBReleaseID != "mbrelease-1" {
		t.Errorf("MBReleaseID = %q, want mbrelease-1", details.MBReleaseID)
	}
	if details.TrackTitle != "In the Flesh?" {
		t.Errorf("TrackTitle = %q, want 'In the Flesh?'", details.TrackTitle)
	}
}

func TestLidarrHealthCheck(t *testing.T) {
	resetLidarrCaches()
	mock := newLidarrMock(t, map[string]any{
		"/api/v1/system/status": map[string]any{"version": "2.0.0"},
	})
	client := NewLidarrClient(mock.server.URL, "test-key", nil)

	ok, err := client.HealthCheck()
	if err != nil || !ok {
		t.Errorf("HealthCheck = (%v, %v), want (true, nil)", ok, err)
	}
}

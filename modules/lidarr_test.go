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

// LidarrInvalidateCaches drops every in-memory Lidarr cache. It is what the force
// re-correlate verb calls so a release selection changed in Lidarr is re-fetched
// before the 1h TTL expires.
func TestLidarrInvalidateCaches(t *testing.T) {
	resetLidarrCaches()
	t.Cleanup(resetLidarrCaches)

	lidarrArtistsCacheMu.Lock()
	lidarrArtistsCache["a"] = models.CachedLidarrArtistRelease{}
	lidarrArtistsCacheMu.Unlock()
	lidarrAlbumsCacheMu.Lock()
	lidarrAlbumsCache["b"] = models.CachedLidarrAlbumRelease{}
	lidarrAlbumsCacheMu.Unlock()
	lidarrTracksCacheMu.Lock()
	lidarrTracksCache["c"] = models.CachedLidarrTracksRelease{}
	lidarrTracksCacheMu.Unlock()
	lidarrTrackFilesCacheMu.Lock()
	lidarrTrackFilesCache["d"] = models.CachedLidarrTrackFilesRelease{}
	lidarrTrackFilesCacheMu.Unlock()

	LidarrInvalidateCaches()

	lidarrArtistsCacheMu.RLock()
	lidarrAlbumsCacheMu.RLock()
	lidarrTracksCacheMu.RLock()
	lidarrTrackFilesCacheMu.RLock()
	n := len(lidarrArtistsCache) + len(lidarrAlbumsCache) + len(lidarrTracksCache) + len(lidarrTrackFilesCache)
	lidarrTrackFilesCacheMu.RUnlock()
	lidarrTracksCacheMu.RUnlock()
	lidarrAlbumsCacheMu.RUnlock()
	lidarrArtistsCacheMu.RUnlock()

	if n != 0 {
		t.Errorf("caches still hold %d entries after invalidation, want 0", n)
	}
}

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

// TestLidarrFindTrackFileByPathMultiDiscSameFilename reproduces the endless-retag
// bug: a multi-disc release where both discs carry a track file with the same
// basename (…/Album (2001)/CD1/01 Intro.flac and …/Album (2001)/CD2/01 Intro.flac).
// Matching on (album folder, basename) alone — ignoring the CD1/CD2 media folder —
// makes the lookup return whichever disc's trackfile appears first in Lidarr's list.
// So the CD2 file resolves to the CD1 trackfile (wrong MB track and disc/position),
// and since the tags never converge to the file's real disc, every scan rewrites it.
func TestLidarrFindTrackFileByPathMultiDiscSameFilename(t *testing.T) {
	resetLidarrCaches()
	root := filepath.Join("/", "music")
	// We are processing the CD2 copy of the track.
	trackPath := filepath.Join(root, "Some Artist", "Greatest Hits (2001)", "CD2", "01 Intro.flac")

	mock := newLidarrMock(t, map[string]any{
		"/api/v1/trackfile": []models.LidarrTrackFile{
			// CD1 comes first in the list, so an album+basename-only matcher returns it.
			{ID: 100, AlbumID: 42, ArtistID: 2, Path: "/data/music/Some Artist/Greatest Hits (2001)/CD1/01 Intro.flac"},
			{ID: 200, AlbumID: 42, ArtistID: 2, Path: "/data/music/Some Artist/Greatest Hits (2001)/CD2/01 Intro.flac"},
		},
	})
	client := NewLidarrClient(mock.server.URL, "test-key", nil)

	tf, err := client.FindTrackFileByPath(2, trackPath, root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tf == nil {
		t.Fatal("expected a trackfile match for the CD2 file, got nil")
	}
	if tf.ID != 200 {
		t.Fatalf("CD2 file resolved to trackfile %d, want 200 (the CD2 trackfile); "+
			"the media folder is being ignored, so the wrong disc's track was selected", tf.ID)
	}
}

// TestLidarrFindTrackFileByPathDiscFolderSpelling covers the same multi-disc release
// when the two sides spell the media folder differently — "CD 02" on disk against
// "CD2" in Lidarr's stored path. The disc *number* is the evidence, so the file must
// still resolve to its own disc rather than dropping to unmatched.
func TestLidarrFindTrackFileByPathDiscFolderSpelling(t *testing.T) {
	resetLidarrCaches()
	root := filepath.Join("/", "music")
	trackPath := filepath.Join(root, "Some Artist", "Greatest Hits (2001)", "CD 02", "01 Intro.flac")

	mock := newLidarrMock(t, map[string]any{
		"/api/v1/trackfile": []models.LidarrTrackFile{
			{ID: 100, AlbumID: 42, ArtistID: 2, Path: "/data/music/Some Artist/Greatest Hits (2001)/CD1/01 Intro.flac"},
			{ID: 200, AlbumID: 42, ArtistID: 2, Path: "/data/music/Some Artist/Greatest Hits (2001)/CD2/01 Intro.flac"},
		},
	})
	client := NewLidarrClient(mock.server.URL, "test-key", nil)

	tf, err := client.FindTrackFileByPath(2, trackPath, root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tf == nil || tf.ID != 200 {
		t.Fatalf("CD 02 file resolved to %+v, want trackfile 200 — the disc number should match across folder spellings", tf)
	}
}

// TestLidarrFindTrackFileByPathAmbiguous covers two trackfiles that both fit the
// (album, media, basename) triple — a stale Lidarr row left behind by a move, say.
// Picking the first is a coin flip between two discs, so the lookup must report no
// match and let the file surface as unmatched.
func TestLidarrFindTrackFileByPathAmbiguous(t *testing.T) {
	resetLidarrCaches()
	root := filepath.Join("/", "music")
	trackPath := filepath.Join(root, "Some Artist", "Greatest Hits (2001)", "CD2", "01 Intro.flac")

	mock := newLidarrMock(t, map[string]any{
		"/api/v1/trackfile": []models.LidarrTrackFile{
			{ID: 100, AlbumID: 42, ArtistID: 2, Path: "/data/music/Some Artist/Greatest Hits (2001)/CD2/01 Intro.flac"},
			{ID: 200, AlbumID: 42, ArtistID: 2, Path: "/data/music/Some Artist/Greatest Hits (2001)/CD 2/01 Intro.flac"},
		},
	})
	client := NewLidarrClient(mock.server.URL, "test-key", nil)

	tf, err := client.FindTrackFileByPath(2, trackPath, root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tf != nil {
		t.Fatalf("expected no match for an ambiguous pair, got trackfile %d", tf.ID)
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

// TestLidarrGetMonitoredAlbumMBIDNoneMonitored covers the Lidarr state where an
// album's releases are *all* unmonitored — no edition is selected. There is nothing
// to tag against, so the answer is "no release", never a guess at one: picking the
// first unmonitored edition would tag a whole album against a release the user did
// not choose, and Lidarr's own statistics already disagree with the files in that
// state (see models.DiscrepancyNoEdition).
func TestLidarrGetMonitoredAlbumMBIDNoneMonitored(t *testing.T) {
	resetLidarrCaches()
	mock := newLidarrMock(t, map[string]any{
		"/api/v1/album": []models.LidarrAlbum{
			{ID: 42, ArtistID: 2, Title: "Unselected Album", Releases: []models.LidarrAlbumRel{
				{ID: 1, Monitored: false, ForeignReleaseID: "rel-a"},
				{ID: 2, Monitored: false, ForeignReleaseID: "rel-b"},
			}},
		},
	})
	client := NewLidarrClient(mock.server.URL, "test-key", nil)

	mbid, err := client.GetMonitoredAlbumMBID(2, 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mbid != nil {
		t.Fatalf("expected no release when none is monitored, got %q", *mbid)
	}
}

// TestGetMonitoredAlbumMBIDNoCrossContamination reproduces the poisoning bug:
// the album endpoint (includeAllArtistAlbums=true) returns several of the artist's
// albums, and each must be cached under its own id — asking for one album must
// never return another album's release, even on the subsequent cache-hit path.
func TestGetMonitoredAlbumMBIDNoCrossContamination(t *testing.T) {
	resetLidarrCaches()
	mock := newLidarrMock(t, map[string]any{
		"/api/v1/album": []models.LidarrAlbum{
			{ID: 100, Title: "Other Soundtrack", Releases: []models.LidarrAlbumRel{
				{Monitored: true, ForeignReleaseID: "rel-other"},
			}},
			{ID: 49719, Title: "Planet Earth II", Releases: []models.LidarrAlbumRel{
				{Monitored: true, ForeignReleaseID: "rel-pe2"},
			}},
			{ID: 200, Title: "Third Soundtrack", Releases: []models.LidarrAlbumRel{
				{Monitored: true, ForeignReleaseID: "rel-third"},
			}},
		},
	})
	client := NewLidarrClient(mock.server.URL, "test-key", nil)

	// Request the middle album; must get ITS release, not a sibling's.
	mbid, err := client.GetMonitoredAlbumMBID(7, 49719)
	if err != nil || mbid == nil {
		t.Fatalf("GetMonitoredAlbumMBID: %v, %v", mbid, err)
	}
	if *mbid != "rel-pe2" {
		t.Fatalf("release = %q, want rel-pe2", *mbid)
	}

	// Cache-hit path must still return the correct release (not a poisoned sibling).
	mbid2, err := client.GetMonitoredAlbumMBID(7, 49719)
	if err != nil || mbid2 == nil || *mbid2 != "rel-pe2" {
		t.Fatalf("cached release = %v (%v), want rel-pe2", mbid2, err)
	}

	// A sibling album from the same response must resolve to its own release,
	// served from the cache the first call warmed (no extra HTTP hit).
	mbid3, err := client.GetMonitoredAlbumMBID(7, 200)
	if err != nil || mbid3 == nil || *mbid3 != "rel-third" {
		t.Fatalf("sibling release = %v (%v), want rel-third", mbid3, err)
	}
	if n := mock.hitCount("/api/v1/album"); n != 1 {
		t.Errorf("/api/v1/album hit %d times, want 1 (siblings warmed into cache)", n)
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
		"/api/v1/rootfolder": []map[string]any{{"id": 1, "path": "/music"}},
	})
	client := NewLidarrClient(mock.server.URL, "test-key", nil)

	ok, err := client.HealthCheck()
	if err != nil || !ok {
		t.Errorf("HealthCheck = (%v, %v), want (true, nil)", ok, err)
	}
}

// TestLidarrHealthCheckAuthGated reproduces the Authelia case: the status endpoint is
// whitelisted (200) but the authenticated endpoints are rejected (401). The health
// check must probe the authenticated path so it reports unhealthy, rather than being
// fooled green by the open status endpoint while every real scan lookup fails.
func TestLidarrHealthCheckAuthGated(t *testing.T) {
	resetLidarrCaches()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/system/status" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "2.0.0"})
			return
		}
		// everything else sits behind auth and is rejected
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`401 Unauthorized`))
	}))
	t.Cleanup(srv.Close)
	client := NewLidarrClient(srv.URL, "test-key", nil)

	ok, err := client.HealthCheck()
	if ok || err == nil {
		t.Errorf("HealthCheck = (%v, %v), want (false, non-nil) when authenticated endpoints are gated", ok, err)
	}
}

// TestLidarrGetArtists: the collection mirror matches Autotaggerr artists to Lidarr
// by MusicBrainz ID, so the ID has to survive the round trip — matching by name is
// exactly what the mirror is avoiding.
func TestLidarrGetArtists(t *testing.T) {
	resetLidarrCaches()
	mock := newLidarrMock(t, map[string]any{
		"/api/v1/artist": []models.LidarrArtist{
			{ID: 1, Name: "Radiohead", ForeignArtistID: "a74b1b7f-71a5-4011-9441-d0b5e4122711"},
			{ID: 2, Name: "Pink Floyd", ForeignArtistID: "83d91898-7763-47d7-b03b-b92132375c47"},
		},
	})
	client := NewLidarrClient(mock.server.URL, "test-key", nil)

	artists, err := client.GetArtists()
	if err != nil {
		t.Fatalf("GetArtists: %v", err)
	}
	if len(artists) != 2 {
		t.Fatalf("artists = %d, want 2", len(artists))
	}
	if artists[0].ForeignArtistID != "a74b1b7f-71a5-4011-9441-d0b5e4122711" {
		t.Errorf("first artist MBID = %q", artists[0].ForeignArtistID)
	}
}

// TestLidarrGetArtistAlbums: the mirror maps Lidarr's have/total statistics onto
// owned and wanted release-groups, so both the monitored flag and the counts matter.
func TestLidarrGetArtistAlbums(t *testing.T) {
	resetLidarrCaches()
	albums := []models.LidarrAlbum{
		{ID: 10, Title: "OK Computer", ForeignAlbumID: "rg-1", Monitored: true},
		{ID: 11, Title: "Kid A", ForeignAlbumID: "rg-2", Monitored: false},
	}
	mock := newLidarrMock(t, map[string]any{"/api/v1/album": albums})
	client := NewLidarrClient(mock.server.URL, "test-key", nil)

	got, err := client.GetArtistAlbums(1)
	if err != nil {
		t.Fatalf("GetArtistAlbums: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("albums = %d, want 2", len(got))
	}
	if got[0].ForeignAlbumID != "rg-1" || !got[0].Monitored {
		t.Errorf("first album = %+v", got[0])
	}
	if got[1].Monitored {
		t.Error("an unmonitored album came back monitored")
	}
}

// TestLidarrClientReportsHTTPFailures: an unreachable or unhappy Lidarr must be an
// error the caller can report, not an empty list that reads as "you own nothing".
func TestLidarrClientReportsHTTPFailures(t *testing.T) {
	resetLidarrCaches()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	client := NewLidarrClient(server.URL, "wrong-key", nil)

	if _, err := client.GetArtists(); err == nil {
		t.Error("a 401 from Lidarr was reported as success")
	}
	if _, err := client.GetArtistAlbums(1); err == nil {
		t.Error("a 401 from Lidarr was reported as success")
	}
}

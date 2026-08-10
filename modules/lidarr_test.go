package modules

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// TestLidarrFindArtistByNameNotFound: "Lidarr obviously has this artist, why does it
// say not found?" is answered by naming the comparison — our folder against the last
// segment of Lidarr's stored path, never name against name — so the error points at
// the mismatch instead of reading as though Lidarr were unreachable.
func TestLidarrFindArtistByNameNotFound(t *testing.T) {
	resetLidarrCaches()
	mock := newLidarrMock(t, map[string]any{
		"/api/v1/artist": []models.LidarrArtist{{ID: 1, Name: "Radiohead", Path: "/data/music/Radiohead"}},
	})
	client := NewLidarrClient(mock.server.URL, "test-key", nil)

	_, err := client.FindArtistByName("Nonexistent")
	if err == nil {
		t.Fatal("expected error for artist not found")
	}
	if !errors.Is(err, ErrLidarrArtistNotFound) {
		t.Errorf("error %q is not ErrLidarrArtistNotFound", err)
	}
	for _, want := range []string{"Nonexistent", "last path segment"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestLidarrFindArtistByNameFolderDiffersFromName pins the two branches of
// FindArtistByName to the same comparison. Lidarr's artist *name* and the folder it
// stores their files in routinely differ — a slash is not legal in a path, so `AC/DC`
// lives in `AC_DC` — and the lookup input is always a folder, taken from the file
// being processed. The cache branch used to match on the name, which made a cached
// answer and a fresh one disagree: the folder never matched, so every file re-fetched
// the whole artist list, and the name matched folders that did not exist on disk.
func TestLidarrFindArtistByNameFolderDiffersFromName(t *testing.T) {
	resetLidarrCaches()
	t.Cleanup(resetLidarrCaches)

	mock := newLidarrMock(t, map[string]any{
		"/api/v1/artist": []models.LidarrArtist{
			{ID: 1, Name: "AC/DC", Path: "/data/music/AC_DC"},
			{ID: 2, Name: "Radiohead", Path: "/data/music/Radiohead"},
		},
	})
	client := NewLidarrClient(mock.server.URL, "test-key", nil)

	got, err := client.FindArtistByName("AC_DC")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("expected artist id 1, got %+v", got)
	}

	// The point of the fix: the same folder is now served from the cache the first
	// call populated, instead of falling through to /api/v1/artist once per file.
	if _, err := client.FindArtistByName("AC_DC"); err != nil {
		t.Fatalf("unexpected error on cached call: %v", err)
	}
	if n := mock.hitCount("/api/v1/artist"); n != 1 {
		t.Errorf("/api/v1/artist hit %d times, want 1 (the cached lookup must match on the folder)", n)
	}

	// And the converse: the artist's name is not a folder any file can be under, so
	// a cache hit on it would answer for a path that does not exist.
	if _, err := client.FindArtistByName("AC/DC"); !errors.Is(err, ErrLidarrArtistNotFound) {
		t.Errorf("lookup by artist name returned %v, want ErrLidarrArtistNotFound", err)
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

// TestLidarrClientReportsLoginPage is the failure this diagnostic work exists for: an
// authentication proxy answers the API call with its own login page, status 200. The
// decoder's "invalid character '<'" names nothing an operator can act on, so the error
// has to say it got HTML, from where, and that a proxy is the usual reason.
func TestLidarrClientReportsLoginPage(t *testing.T) {
	resetLidarrCaches()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!DOCTYPE html><html><head><title>Authelia</title></head><body>Sign in</body></html>"))
	}))
	t.Cleanup(server.Close)
	cookie := "authelia_session=abc"
	client := NewLidarrClient(server.URL, "test-key", &cookie)

	_, err := client.GetArtists()
	if err == nil {
		t.Fatal("an HTML login page was decoded as a successful artist list")
	}
	for _, want := range []string{"not JSON", "text/html", "<!DOCTYPE html", "/api/v1/artist"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestLidarrClientReportsCrossHostRedirect covers the case where the cookie is valid
// and still never arrives: Go strips the Cookie header when a redirect crosses to
// another host, so the proxy's portal answers unauthenticated. The error must name the
// redirect, because from the caller's side this is indistinguishable from a bad cookie.
func TestLidarrClientReportsCrossHostRedirect(t *testing.T) {
	resetLidarrCaches()
	var cookieReached atomic.Bool
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookieReached.Store(r.Header.Get("Cookie") != "")
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>login</html>"))
	}))
	t.Cleanup(portal.Close)

	// The redirect has to target a different *hostname* for Go to drop the cookie —
	// same host on another port keeps it — so the portal is addressed as localhost
	// while the client starts at 127.0.0.1. That is the shape of the real case
	// (lidarr.example.com redirecting to auth.example.com).
	portalURL := strings.Replace(portal.URL, "127.0.0.1", "localhost", 1)
	lidarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, portalURL+"/login", http.StatusFound)
	}))
	t.Cleanup(lidarr.Close)

	cookie := "authelia_session=abc"
	client := NewLidarrClient(lidarr.URL, "test-key", &cookie)

	_, err := client.GetArtists()
	if err == nil {
		t.Fatal("a redirect to a login portal was reported as success")
	}
	if cookieReached.Load() {
		t.Skip("this environment forwarded the cookie across the redirect; the drop cannot be reproduced here")
	}
	if !strings.Contains(err.Error(), "redirected to") || !strings.Contains(err.Error(), "crosses to another host") {
		t.Errorf("error %q does not explain the redirect and the dropped cookie", err)
	}
}

// TestResolveCorrelationKeepsLidarrCause guards the wrapping that makes all of the
// above visible: recordItem stores this error verbatim on the library item, so a cause
// dropped here is a cause the Items page and the Activity feed can never show.
func TestResolveCorrelationKeepsLidarrCause(t *testing.T) {
	resetLidarrCaches()
	mock := newLidarrMock(t, map[string]any{
		"/api/v1/artist": []models.LidarrArtist{{ID: 1, Name: "Other", Path: "/music/Other"}},
	})
	client := NewLidarrClient(mock.server.URL, "test-key", nil)

	root := filepath.Join("/music")
	_, err := ResolveCorrelation(filepath.Join(root, "Radiohead", "OK Computer (1997)", "01 Airbag.flac"), client, root, false)
	if err == nil {
		t.Fatal("an unknown artist resolved successfully")
	}
	if !errors.Is(err, ErrLidarrArtistNotFound) {
		t.Errorf("error %q lost the Lidarr cause", err)
	}
	if !strings.Contains(err.Error(), "Radiohead") {
		t.Errorf("error %q does not name the artist folder that failed to match", err)
	}
}

// TestLidarrInvalidateArtistCaches: the scoped drop has to take the artist's entries
// and nothing else. The whole-cache flush is what a per-artist button must not do —
// repairing one artist by making every other artist re-fetch is the cost this exists
// to avoid.
func TestLidarrInvalidateArtistCaches(t *testing.T) {
	resetLidarrCaches()
	t.Cleanup(resetLidarrCaches)

	now := time.Now()
	lidarrArtistsCache["1"] = models.CachedLidarrArtistRelease{Timestamp: now}
	lidarrArtistsCache["2"] = models.CachedLidarrArtistRelease{Timestamp: now}
	lidarrTrackFilesCache["1"] = models.CachedLidarrTrackFilesRelease{Timestamp: now}
	lidarrTrackFilesCache["2"] = models.CachedLidarrTrackFilesRelease{Timestamp: now}
	lidarrAlbumsCache["10"] = models.CachedLidarrAlbumRelease{Timestamp: now}
	lidarrAlbumsCache["20"] = models.CachedLidarrAlbumRelease{Timestamp: now}
	lidarrTracksCache["10"] = models.CachedLidarrTracksRelease{Timestamp: now}
	lidarrTracksCache["20"] = models.CachedLidarrTracksRelease{Timestamp: now}

	LidarrInvalidateArtistCaches(1, []int64{10})

	for _, tc := range []struct {
		name    string
		present bool
		got     bool
	}{
		{"artist 1", false, mapHas(lidarrArtistsCache, "1")},
		{"artist 2", true, mapHas(lidarrArtistsCache, "2")},
		{"trackfiles 1", false, mapHas(lidarrTrackFilesCache, "1")},
		{"trackfiles 2", true, mapHas(lidarrTrackFilesCache, "2")},
		{"album 10", false, mapHas(lidarrAlbumsCache, "10")},
		{"album 20", true, mapHas(lidarrAlbumsCache, "20")},
		{"tracks 10", false, mapHas(lidarrTracksCache, "10")},
		{"tracks 20", true, mapHas(lidarrTracksCache, "20")},
	} {
		if tc.got != tc.present {
			t.Errorf("%s present = %t, want %t", tc.name, tc.got, tc.present)
		}
	}
}

func mapHas[T any](m map[string]T, key string) bool {
	_, ok := m[key]
	return ok
}

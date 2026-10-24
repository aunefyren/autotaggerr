package modules

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// upgradableLidarr is a Lidarr whose answers can change between calls, which is the
// whole of what an upgrade is from Autotaggerr's side: the same artist and album, a
// new trackfile under a new ID, and track rows re-pointed at it.
type upgradableLidarr struct {
	mu         sync.Mutex
	trackFiles []models.LidarrTrackFile
	tracks     []models.LidarrTrack
	hits       map[string]int
	server     *httptest.Server
}

func newUpgradableLidarr(t *testing.T) *upgradableLidarr {
	t.Helper()
	l := &upgradableLidarr{hits: map[string]int{}}
	mux := http.NewServeMux()
	serve := func(path string, body func() any) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			l.mu.Lock()
			l.hits[path]++
			payload := body()
			l.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(payload)
		})
	}
	serve("/api/v1/artist", func() any {
		return []models.LidarrArtist{{ID: 2, Name: "Pink Floyd", Path: "/data/music/Pink Floyd"}}
	})
	serve("/api/v1/trackfile", func() any { return l.trackFiles })
	serve("/api/v1/track", func() any { return l.tracks })
	serve("/api/v1/album", func() any {
		return []models.LidarrAlbum{{ID: 42, ArtistID: 2, Releases: []models.LidarrAlbumRel{
			{ID: 2, Monitored: true, ForeignReleaseID: "mbrelease-1"},
		}}}
	})
	l.server = httptest.NewServer(mux)
	t.Cleanup(l.server.Close)
	return l
}

// upgrade replaces the album's one file the way Lidarr does: a new trackfile ID at
// path, and the track row re-pointed at it.
func (l *upgradableLidarr) upgrade(id int64, path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.trackFiles = []models.LidarrTrackFile{{ID: id, AlbumID: 42, ArtistID: 2, Path: path}}
	l.tracks = []models.LidarrTrack{{ID: 500, Title: "In the Flesh?", ForeignTrackID: "mbtrack-1", ForeignRecordingID: "mbrec-1", TrackFileID: i64ptr(id)}}
}

func (l *upgradableLidarr) hitCount(path string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.hits[path]
}

// ageLidarrCaches backdates every cached Lidarr entry, standing in for the minutes that
// pass between a scan filling the caches and Lidarr upgrading something.
func ageLidarrCaches(by time.Duration) {
	lidarrTrackFilesCacheMu.Lock()
	for k, v := range lidarrTrackFilesCache {
		v.Timestamp = v.Timestamp.Add(-by)
		lidarrTrackFilesCache[k] = v
	}
	lidarrTrackFilesCacheMu.Unlock()
	lidarrTracksCacheMu.Lock()
	for k, v := range lidarrTracksCache {
		v.Timestamp = v.Timestamp.Add(-by)
		lidarrTracksCache[k] = v
	}
	lidarrTracksCacheMu.Unlock()
	lidarrAlbumsCacheMu.Lock()
	for k, v := range lidarrAlbumsCache {
		v.Timestamp = v.Timestamp.Add(-by)
		lidarrAlbumsCache[k] = v
	}
	lidarrAlbumsCacheMu.Unlock()
}

const (
	lidarrAlbumPath = "/data/music/Pink Floyd/The Wall (1979)/"
	recheckTrack    = "01 In the Flesh"
)

func wallPath(root, ext string) string {
	return filepath.Join(root, "Pink Floyd", "The Wall (1979)", recheckTrack+ext)
}

// The production case: an MP3 is upgraded to FLAC. The new path is absent from the
// trackfile list cached before the upgrade, and that used to make the file unmatched for
// up to an hour. It is now asked about again and resolves.
func TestResolveRechecksANewPathAgainstAnAgedCache(t *testing.T) {
	resetLidarrCaches()
	t.Cleanup(resetLidarrCaches)
	root := filepath.Join("/", "music")
	l := newUpgradableLidarr(t)
	client := NewLidarrClient(l.server.URL, "test-key", nil)

	l.upgrade(100, lidarrAlbumPath+recheckTrack+".mp3")
	if d, err := ResolveMetadataDetailsFromLidarr(client, wallPath(root, ".mp3"), root); err != nil || d == nil {
		t.Fatalf("priming resolve = %v, %v; want details", d, err)
	}

	l.upgrade(101, lidarrAlbumPath+recheckTrack+".flac")
	ageLidarrCaches(2 * lidarrRecheckAfter)

	d, err := ResolveMetadataDetailsFromLidarr(client, wallPath(root, ".flac"), root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d == nil || d.MBTrackID != "mbtrack-1" || d.MBReleaseID != "mbrelease-1" {
		t.Fatalf("upgraded file resolved to %+v, want mbtrack-1 on mbrelease-1", d)
	}
	if got := l.hitCount("/api/v1/trackfile"); got != 2 {
		t.Errorf("trackfile list fetched %d times, want 2 (prime + one recheck)", got)
	}
}

// The quieter half of the same failure: the path did not change (a FLAC replaced by a
// better FLAC), so the file is found in a freshly fetched trackfile list under its new
// ID — but the album's track list is still the cached one, pointing at the old ID. The
// two caches disagree, and the recheck has to refetch the track list, not only the file
// list, or it asks again and gets the same disagreement.
func TestResolveRechecksTracksThatPointAtAReplacedTrackfile(t *testing.T) {
	resetLidarrCaches()
	t.Cleanup(resetLidarrCaches)
	root := filepath.Join("/", "music")
	l := newUpgradableLidarr(t)
	client := NewLidarrClient(l.server.URL, "test-key", nil)

	path := lidarrAlbumPath + recheckTrack + ".flac"
	l.upgrade(100, path)
	if d, err := ResolveMetadataDetailsFromLidarr(client, wallPath(root, ".flac"), root); err != nil || d == nil {
		t.Fatalf("priming resolve = %v, %v; want details", d, err)
	}

	l.upgrade(101, path)
	ageLidarrCaches(2 * lidarrRecheckAfter)
	// The trackfile list expired on its own and was refetched; the track list did not.
	lidarrTrackFilesCacheMu.Lock()
	delete(lidarrTrackFilesCache, client.cacheKey(2))
	lidarrTrackFilesCacheMu.Unlock()

	d, err := ResolveMetadataDetailsFromLidarr(client, wallPath(root, ".flac"), root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d == nil || d.MBTrackID != "mbtrack-1" {
		t.Fatalf("resolved to %+v, want mbtrack-1", d)
	}
	if got := l.hitCount("/api/v1/track"); got != 2 {
		t.Errorf("track list fetched %d times, want 2 (prime + one recheck)", got)
	}
}

// A miss against answers fetched moments ago is believed. A file Lidarr really does not
// know about misses every time, and re-downloading the artist's trackfile list once per
// such file is the cost the floor exists to prevent.
func TestResolveBelievesAMissAgainstAFreshCache(t *testing.T) {
	resetLidarrCaches()
	t.Cleanup(resetLidarrCaches)
	root := filepath.Join("/", "music")
	l := newUpgradableLidarr(t)
	client := NewLidarrClient(l.server.URL, "test-key", nil)
	l.upgrade(100, lidarrAlbumPath+recheckTrack+".flac")

	for i := 0; i < 3; i++ {
		d, err := ResolveMetadataDetailsFromLidarr(client, wallPath(root, ".mp3"), root)
		if err != nil || d != nil {
			t.Fatalf("resolve of a file Lidarr does not know = %+v, %v; want nil, nil", d, err)
		}
	}
	if got := l.hitCount("/api/v1/trackfile"); got != 1 {
		t.Errorf("trackfile list fetched %d times, want 1", got)
	}
}

// One recheck per window, not one per file: after the refetch the entries are fresh,
// so the next unknown file of the same artist is believed.
func TestResolveRechecksOncePerWindow(t *testing.T) {
	resetLidarrCaches()
	t.Cleanup(resetLidarrCaches)
	root := filepath.Join("/", "music")
	l := newUpgradableLidarr(t)
	client := NewLidarrClient(l.server.URL, "test-key", nil)
	l.upgrade(100, lidarrAlbumPath+recheckTrack+".flac")

	if d, _ := ResolveMetadataDetailsFromLidarr(client, wallPath(root, ".flac"), root); d == nil {
		t.Fatal("priming resolve found nothing")
	}
	ageLidarrCaches(2 * lidarrRecheckAfter)

	for i := 0; i < 3; i++ {
		if d, _ := ResolveMetadataDetailsFromLidarr(client, wallPath(root, ".mp3"), root); d != nil {
			t.Fatalf("unknown file resolved to %+v", d)
		}
	}
	if got := l.hitCount("/api/v1/trackfile"); got != 2 {
		t.Errorf("trackfile list fetched %d times, want 2 (prime + one recheck)", got)
	}
}

// The history read asks from the given moment, in UTC, with the artist included — the
// artist is how a record names the MusicBrainz artist it is about.
func TestLidarrGetHistorySince(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/history/since" {
			http.NotFound(w, r)
			return
		}
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":9,"artistId":2,"albumId":42,"date":"2026-10-24T10:00:00Z","eventType":"trackFileImported","artist":{"id":2,"foreignArtistId":"mb-artist","artistName":"Pink Floyd"}}]`))
	}))
	t.Cleanup(srv.Close)

	since := time.Date(2026, 10, 24, 11, 0, 0, 0, time.FixedZone("CEST", 2*3600))
	records, err := NewLidarrClient(srv.URL, "k", nil).GetHistorySince(since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotQuery != "date=2026-10-24T09%3A00%3A00Z&includeArtist=true" {
		t.Errorf("query = %q", gotQuery)
	}
	if len(records) != 1 || records[0].EventType != models.LidarrHistoryTrackFileImported ||
		records[0].Artist == nil || records[0].Artist.ForeignArtistID != "mb-artist" {
		t.Errorf("records = %+v", records)
	}
}

// The stale-but-consistent case: Lidarr replaced the file at the same path with a
// different edition's track. Every cached entry still describes the old file and they
// agree, so an ordinary resolve finds nothing to doubt and answers the old identity.
// A fresh resolve — what a file whose bytes changed gets — rechecks the hit and
// answers the new one.
func TestResolveFreshRechecksAStaleButConsistentHit(t *testing.T) {
	resetLidarrCaches()
	t.Cleanup(resetLidarrCaches)
	root := filepath.Join("/", "music")
	l := newUpgradableLidarr(t)
	client := NewLidarrClient(l.server.URL, "test-key", nil)

	path := lidarrAlbumPath + recheckTrack + ".flac"
	l.upgrade(100, path)
	if d, err := ResolveMetadataDetailsFromLidarr(client, wallPath(root, ".flac"), root); err != nil || d == nil {
		t.Fatalf("priming resolve = %v, %v; want details", d, err)
	}

	l.mu.Lock()
	l.tracks[0].ForeignTrackID = "mbtrack-deluxe"
	l.mu.Unlock()
	ageLidarrCaches(2 * lidarrRecheckAfter)

	if d, _ := ResolveMetadataDetailsFromLidarr(client, wallPath(root, ".flac"), root); d == nil || d.MBTrackID != "mbtrack-1" {
		t.Fatalf("ordinary resolve = %+v; want the cached mbtrack-1 (nothing missed)", d)
	}
	d, err := ResolveMetadataDetailsFromLidarrFresh(client, wallPath(root, ".flac"), root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d == nil || d.MBTrackID != "mbtrack-deluxe" {
		t.Fatalf("fresh resolve = %+v; want mbtrack-deluxe", d)
	}

	// Fresh again inside the window: the entries are new, so nothing is refetched.
	tracksBefore := l.hitCount("/api/v1/track")
	if _, err := ResolveMetadataDetailsFromLidarrFresh(client, wallPath(root, ".flac"), root); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := l.hitCount("/api/v1/track"); got != tracksBefore {
		t.Errorf("track list fetched again inside the window (%d → %d)", tracksBefore, got)
	}
}

func TestLidarrTagWritingAdvice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/config/metadataprovider" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1,"metadataSource":"","writeAudioTags":"sync","scrubAudioTags":false}`))
	}))
	t.Cleanup(srv.Close)

	cfg, err := NewLidarrClient(srv.URL, "k", nil).GetMetadataProviderConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if advice := LidarrTagWritingAdvice(cfg.WriteAudioTags); !strings.Contains(advice, "(Sync)") || !strings.Contains(advice, "For new downloads only") {
		t.Errorf("advice = %q", advice)
	}
	if advice := LidarrTagWritingAdvice(models.LidarrWriteAudioTagsNewFiles); advice != "" {
		t.Errorf("advice for the recommended setting = %q, want none", advice)
	}
}

// Two Lidarrs number their artists and albums independently, and the caches are
// process-wide. Each instance's answers must stay its own: resolving a file through
// one must never be answered from what the other said about the same IDs.
func TestLidarrCachesAreSeparatePerInstance(t *testing.T) {
	resetLidarrCaches()
	t.Cleanup(resetLidarrCaches)
	root := filepath.Join("/", "music")

	main := newUpgradableLidarr(t)
	main.upgrade(100, lidarrAlbumPath+recheckTrack+".flac")
	lossless := newUpgradableLidarr(t)
	lossless.upgrade(100, lidarrAlbumPath+recheckTrack+".flac")
	lossless.mu.Lock()
	lossless.tracks[0].ForeignTrackID = "mbtrack-lossless-edition"
	lossless.mu.Unlock()

	mainClient := NewLidarrClient(main.server.URL, "k", nil)
	losslessClient := NewLidarrClient(lossless.server.URL, "k", nil)

	d, err := ResolveMetadataDetailsFromLidarr(mainClient, wallPath(root, ".flac"), root)
	if err != nil || d == nil || d.MBTrackID != "mbtrack-1" {
		t.Fatalf("main Lidarr resolved %+v, %v", d, err)
	}
	d, err = ResolveMetadataDetailsFromLidarr(losslessClient, wallPath(root, ".flac"), root)
	if err != nil || d == nil || d.MBTrackID != "mbtrack-lossless-edition" {
		t.Fatalf("lossless Lidarr resolved %+v, %v — answered from the other instance's cache", d, err)
	}
	for name, l := range map[string]*upgradableLidarr{"main": main, "lossless": lossless} {
		if got := l.hitCount("/api/v1/artist"); got != 1 {
			t.Errorf("%s Lidarr's artist list fetched %d times, want 1 (each asked once, for itself)", name, got)
		}
	}
}

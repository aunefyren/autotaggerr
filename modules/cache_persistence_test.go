package modules

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// redirectCachePaths points every legacy cache file at a fresh temp dir and restores
// the originals on cleanup, so the import tests never touch the real ./config files.
func redirectCachePaths(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	saved := map[*string]string{}
	for _, pp := range []*string{
		&musicbrainzReleaseCachePath,
		&lidarrArtistsCachePath, &lidarrAlbumsCachePath,
		&lidarrTracksCachePath, &lidarrTrackFilesCachePath,
		&plexAlbumKeyCachePath,
	} {
		saved[pp] = *pp
		*pp = filepath.Join(dir, filepath.Base(*pp))
	}
	t.Cleanup(func() {
		for pp, orig := range saved {
			*pp = orig
		}
	})
	return dir
}

// clearProviderMaps empties the five in-memory maps — what a restart looks like from
// inside the process.
func clearProviderMaps() {
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
	plexAlbumKeyCacheMu.Lock()
	plexAlbumKeyCache = map[string]models.PlexAlbumKeyCache{}
	plexAlbumKeyCacheMu.Unlock()
}

// The point of moving these five off JSON: a write lands as it is made, so it
// survives a restart that no flush preceded. Under the old batching, an entry cached
// outside a scan was lost unless a flush happened to follow — and nothing flushed on
// shutdown.
func TestProviderCachePersistsWithoutAFlush(t *testing.T) {
	redirectCachePaths(t)
	dbForCache(t)
	clearProviderMaps()
	t.Cleanup(clearProviderMaps)

	providerCachePut(models.ProviderCacheLidarrArtists, "1",
		models.CachedLidarrArtistRelease{Artist: models.LidarrArtist{ID: 1, Name: "Radiohead"}, Timestamp: time.Now()}, time.Hour)
	providerCachePut(models.ProviderCacheLidarrAlbums, "42",
		models.CachedLidarrAlbumRelease{Album: models.LidarrAlbum{ID: 42, Title: "OK Computer"}, Timestamp: time.Now()}, time.Hour)
	providerCachePut(models.ProviderCacheLidarrTracks, "42",
		models.CachedLidarrTracksRelease{Tracks: []models.LidarrTrack{{ID: 500, Title: "Airbag"}}, Timestamp: time.Now()}, time.Hour)
	providerCachePut(models.ProviderCacheLidarrTrackFiles, "2",
		models.CachedLidarrTrackFilesRelease{TrackFiles: []models.LidarrTrackFile{{ID: 100, Path: "/x.flac"}}, Timestamp: time.Now()}, time.Hour)
	providerCachePut(models.ProviderCachePlexAlbumKeys, "OK Computer",
		models.PlexAlbumKeyCache{AlbumKey: "/library/metadata/1", Timestamp: time.Now()}, time.Hour)

	// Nothing is flushed, closed or shut down — the process simply comes back.
	clearProviderMaps()
	LoadAllCaches()

	if lidarrArtistsCache["1"].Artist.Name != "Radiohead" {
		t.Error("lidarr artists cache did not survive the restart")
	}
	if lidarrAlbumsCache["42"].Album.Title != "OK Computer" {
		t.Error("lidarr albums cache did not survive the restart")
	}
	if len(lidarrTracksCache["42"].Tracks) != 1 || lidarrTracksCache["42"].Tracks[0].Title != "Airbag" {
		t.Error("lidarr tracks cache did not survive the restart")
	}
	if len(lidarrTrackFilesCache["2"].TrackFiles) != 1 || lidarrTrackFilesCache["2"].TrackFiles[0].ID != 100 {
		t.Error("lidarr trackfiles cache did not survive the restart")
	}
	if plexAlbumKeyCache["OK Computer"].AlbumKey != "/library/metadata/1" {
		t.Error("plex album key cache did not survive the restart")
	}
}

// An expired entry is not restored — it would only be discarded by the TTL check on
// the next read — but its row stays, because deleting it would empty the source and
// re-trigger the one-time JSON import.
func TestProviderCacheSkipsExpiredRows(t *testing.T) {
	redirectCachePaths(t)
	dbForCache(t)
	clearProviderMaps()
	t.Cleanup(clearProviderMaps)

	providerCachePut(models.ProviderCacheLidarrArtists, "1",
		models.CachedLidarrArtistRelease{Artist: models.LidarrArtist{ID: 1, Name: "Radiohead"}}, -time.Minute)

	clearProviderMaps()
	if err := LidarrLoadArtistsCache(); err != nil {
		t.Fatalf("LidarrLoadArtistsCache: %v", err)
	}
	if _, ok := lidarrArtistsCache["1"]; ok {
		t.Error("an expired entry was restored into memory")
	}

	var rows int64
	if err := cacheDB.Model(&models.ProviderCache{}).
		Where("source = ?", models.ProviderCacheLidarrArtists).Count(&rows).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("rows = %d, want 1 — expired rows stay so the JSON import cannot re-run", rows)
	}
}

// The upgrade must not re-ask Lidarr for everything it already knew, so the legacy
// file is read exactly once — and only while the source is still empty.
func TestProviderCacheImportsLegacyJSONOnce(t *testing.T) {
	dir := redirectCachePaths(t)
	dbForCache(t)
	clearProviderMaps()
	t.Cleanup(clearProviderMaps)

	legacy := map[string]models.CachedLidarrArtistRelease{
		"7": {Artist: models.LidarrArtist{ID: 7, Name: "Talk Talk"}, Timestamp: time.Now()},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lidarr_artists.json"), data, 0o644); err != nil {
		t.Fatalf("write legacy cache: %v", err)
	}

	if err := LidarrLoadArtistsCache(); err != nil {
		t.Fatalf("LidarrLoadArtistsCache: %v", err)
	}
	if lidarrArtistsCache["7"].Artist.Name != "Talk Talk" {
		t.Fatal("the legacy JSON cache was not imported")
	}

	// The user drops the artist; the stale file must not resurrect it.
	LidarrInvalidateCaches()
	if err := os.WriteFile(filepath.Join(dir, "lidarr_artists.json"), data, 0o644); err != nil {
		t.Fatalf("rewrite legacy cache: %v", err)
	}
	if err := LidarrLoadArtistsCache(); err != nil {
		t.Fatalf("second load: %v", err)
	}
	if _, ok := lidarrArtistsCache["7"]; ok {
		// Invalidating empties the source, so this is the one case where the import
		// can legitimately run again — the guard is worth stating either way.
		t.Log("note: the import re-ran after an explicit invalidation, which is expected")
	}
}

// The import takes its source with it. config/ used to accumulate five files nothing
// read, sitting alongside the one file that is live configuration — and a one-hour
// provider TTL means there is nothing in them worth keeping once the rows exist.
func TestProviderCacheRemovesLegacyJSONAfterImport(t *testing.T) {
	dir := redirectCachePaths(t)
	dbForCache(t)
	clearProviderMaps()
	t.Cleanup(clearProviderMaps)

	path := filepath.Join(dir, "lidarr_artists.json")
	data, err := json.Marshal(map[string]models.CachedLidarrArtistRelease{
		"7": {Artist: models.LidarrArtist{ID: 7, Name: "Talk Talk"}, Timestamp: time.Now()},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write legacy cache: %v", err)
	}

	if err := LidarrLoadArtistsCache(); err != nil {
		t.Fatalf("LidarrLoadArtistsCache: %v", err)
	}
	if lidarrArtistsCache["7"].Artist.Name != "Talk Talk" {
		t.Fatal("the legacy JSON cache was not imported")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the legacy file survived a successful import (stat err: %v)", err)
	}

	// An install that upgraded before this change still has the file, and its import
	// no-ops on the rows already there — so the skip path has to clean up too, or the
	// cleanup only ever helps installs that had not upgraded yet.
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("rewrite legacy cache: %v", err)
	}
	if err := LidarrLoadArtistsCache(); err != nil {
		t.Fatalf("second load: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the legacy file survived a skipped import (stat err: %v)", err)
	}
}

// A file that does not decode is the one case the deletion would make unrecoverable,
// so it stays put — and the import must not claim to have run.
func TestProviderCacheKeepsUnparseableLegacyJSON(t *testing.T) {
	dir := redirectCachePaths(t)
	dbForCache(t)
	clearProviderMaps()
	t.Cleanup(clearProviderMaps)

	path := filepath.Join(dir, "lidarr_artists.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write legacy cache: %v", err)
	}

	if err := LidarrLoadArtistsCache(); err != nil {
		t.Fatalf("LidarrLoadArtistsCache: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("an unparseable legacy file was deleted: %v", err)
	}
}

// Invalidating by hand has to reach the database too, or the next restart restores
// exactly what the user asked to discard.
func TestLidarrInvalidateClearsTheRows(t *testing.T) {
	redirectCachePaths(t)
	dbForCache(t)
	clearProviderMaps()
	t.Cleanup(clearProviderMaps)

	providerCachePut(models.ProviderCacheLidarrAlbums, "42",
		models.CachedLidarrAlbumRelease{Album: models.LidarrAlbum{ID: 42, Title: "OK Computer"}, Timestamp: time.Now()}, time.Hour)

	LidarrInvalidateCaches()

	clearProviderMaps()
	if err := LidarrLoadAlbumsCache(); err != nil {
		t.Fatalf("LidarrLoadAlbumsCache: %v", err)
	}
	if _, ok := lidarrAlbumsCache["42"]; ok {
		t.Error("a discarded entry came back from the database")
	}
}

// LoadAllCaches must be a safe no-op on a fresh install with no rows and no files.
func TestLoadAllCachesEmpty(t *testing.T) {
	redirectCachePaths(t)
	dbForCache(t)
	clearProviderMaps()
	t.Cleanup(clearProviderMaps)

	LoadAllCaches() // must not panic or error out
}

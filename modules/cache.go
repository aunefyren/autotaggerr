package modules

import (
	"sync"

	"github.com/aunefyren/autotaggerr/logger"
	"gorm.io/gorm"
)

// cacheDB is the database handle used for DB-backed caches (the MusicBrainz
// release cache today). It is nil until SetDB runs at startup; while nil, caches
// fall back to their legacy JSON files, which keeps DB-less callers (and tests)
// working unchanged.
var cacheDB *gorm.DB

// SetDB wires the database handle for DB-backed caches. Call once at startup,
// before LoadAllCaches.
func SetDB(db *gorm.DB) {
	cacheDB = db
}

// Cache writes used to be flushed to disk on every single cache miss, which on a
// full-library scan meant rewriting each (growing) JSON file thousands of times.
// Instead, the Save*Cache paths now mark a cache dirty (cheap, in-memory) and the
// actual disk write is batched via FlushCaches, which is called periodically
// during a scan and once when it finishes.
//
// NOTE: this batching is currently only safe because a scan is single-threaded —
// dirty marks and flushes all happen on the scan goroutine. When per-file
// concurrency is added, the cache maps themselves must be guarded too.

const (
	cacheNameMusicbrainz      = "musicbrainz_releases"
	cacheNameLidarrArtists    = "lidarr_artists"
	cacheNameLidarrAlbums     = "lidarr_albums"
	cacheNameLidarrTracks     = "lidarr_tracks"
	cacheNameLidarrTrackFiles = "lidarr_trackfiles"
	cacheNamePlexAlbumKeys    = "plex_album_keys"
)

type cacheWriter func() error

var (
	cacheMu      sync.Mutex
	cacheDirty   = map[string]bool{}
	cacheWriters = map[string]cacheWriter{}
)

// registerCache wires a cache name to the function that persists it to disk.
func registerCache(name string, write cacheWriter) {
	cacheMu.Lock()
	cacheWriters[name] = write
	cacheMu.Unlock()
}

// markCacheDirty flags a cache as having unsaved in-memory changes.
func markCacheDirty(name string) {
	cacheMu.Lock()
	cacheDirty[name] = true
	cacheMu.Unlock()
}

// LoadAllCaches loads every on-disk cache into memory once. It is called at
// startup so the hot per-file path never touches disk (and never races a
// concurrent reader by reloading a cache mid-scan). Individual load failures are
// logged and skipped — a missing or corrupt cache just starts empty.
func LoadAllCaches() {
	loaders := map[string]func() error{
		cacheNameMusicbrainz:      MusicbrainzLoadCache,
		cacheNameLidarrArtists:    LidarrLoadArtistsCache,
		cacheNameLidarrAlbums:     LidarrLoadAlbumsCache,
		cacheNameLidarrTracks:     LidarrLoadTracksCache,
		cacheNameLidarrTrackFiles: LidarrLoadTrackFilesCache,
		cacheNamePlexAlbumKeys:    PlexLoadAlbumKeyCache,
	}

	for name, load := range loaders {
		if err := load(); err != nil {
			logger.Log.Errorf("failed to load %s cache: %s", name, err.Error())
		}
	}
}

// FlushCaches persists every cache that has unsaved changes. It is a no-op for
// caches that are already clean, so it is cheap to call at scan boundaries.
func FlushCaches() {
	cacheMu.Lock()
	pending := make([]string, 0, len(cacheDirty))
	for name, dirty := range cacheDirty {
		if dirty {
			pending = append(pending, name)
		}
	}
	cacheMu.Unlock()

	for _, name := range pending {
		cacheMu.Lock()
		write := cacheWriters[name]
		cacheMu.Unlock()
		if write == nil {
			continue
		}

		if err := write(); err != nil {
			logger.Log.Errorf("failed to flush %s cache: %s", name, err.Error())
			continue
		}

		cacheMu.Lock()
		cacheDirty[name] = false
		cacheMu.Unlock()
	}
}

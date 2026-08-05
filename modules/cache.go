package modules

import (
	"github.com/aunefyren/autotaggerr/logger"
	"gorm.io/gorm"
)

// cacheDB is the database handle behind every persistent cache. It is nil until
// SetDB runs at startup; while nil, the caches degrade to process-local memory,
// which is what the tests and the one-shot `--file` invocation want.
var cacheDB *gorm.DB

// SetDB wires the database handle for the caches. Call once at startup, before
// LoadAllCaches.
func SetDB(db *gorm.DB) {
	cacheDB = db
}

// Every cache here is write-through: an entry is persisted as it is fetched, in the
// same call.
//
// It used to be batched. Writes marked a cache dirty and a FlushCaches call rewrote
// the whole JSON file later — which meant the write only landed if a flush happened
// to follow, and flushes ran during a scan, at the end of a refresh pass, or in
// one-shot mode. Nothing flushed on shutdown, so a Lidarr sync triggered from the
// Collection page routinely never reached disk at all, and a restart looked like a
// cache that had silently forgotten an hour of work.
//
// Write-through is affordable because these are single rows written seconds apart,
// not the thousands-per-scan churn that made a growing JSON blob expensive to
// rewrite. The batching also carried a warning that it was only safe because scans
// were single-threaded, which stopped being true when the worker pool arrived.

// LoadAllCaches warms every persistent cache into memory once, at startup, so the
// hot per-file path never touches the database (and never races a concurrent reader
// by reloading mid-scan). Individual failures are logged and skipped — a cache that
// cannot be read just starts empty, which costs refetches and nothing else.
func LoadAllCaches() {
	loaders := map[string]func() error{
		"musicbrainz_releases": MusicbrainzLoadCache,
		"musicbrainz_entities": musicbrainzLoadEntityCache,
		"artwork":              artworkLoadCache,
		"lidarr_artists":       LidarrLoadArtistsCache,
		"lidarr_albums":        LidarrLoadAlbumsCache,
		"lidarr_tracks":        LidarrLoadTracksCache,
		"lidarr_trackfiles":    LidarrLoadTrackFilesCache,
		"plex_album_keys":      PlexLoadAlbumKeyCache,
	}

	for name, load := range loaders {
		if err := load(); err != nil {
			logger.Log.Errorf("failed to load %s cache: %s", name, err.Error())
		}
	}
}

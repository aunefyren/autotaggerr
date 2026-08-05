// Persistent cache for the services a library is managed by: Lidarr's artists,
// albums, tracks and track files, and Plex's album keys.
//
// All five used to be JSON files under config/, written by a batched flusher. The
// format was the smaller problem — the flusher ran only during a scan, at the end of
// a refresh pass, or in one-shot mode, and nothing flushed on shutdown, so a restart
// between a lookup and the next flush simply lost the writes. Here every write goes
// through as it happens, which is what let the whole dirty/flush mechanism be deleted.
//
// Each cache keeps its in-memory map as before: that is the hot path, and a scan asks
// for the same artist's track files once per track. This file is only how those maps
// survive a restart. Reads come from the map; the database is read once, at startup.
package modules

import (
	"encoding/json"
	"os"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm/clause"
)

// providerCacheItem is one entry to store: the service's key and the value to encode.
type providerCacheItem struct {
	key   string
	value any
}

// providerCachePut stores one entry, write-through.
func providerCachePut(source, key string, value any, ttl time.Duration) {
	providerCachePutMany(source, ttl, providerCacheItem{key: key, value: value})
}

// providerCachePutMany stores a batch in one statement.
//
// Batched because the call sites populate whole lists at once — every artist Lidarr
// knows, every album on an artist — and a row-at-a-time upsert would be one SQLite
// transaction each for work that is logically a single answer.
//
// A failure to encode or persist is logged and swallowed: the caller already has the
// value it asked for, and failing its request because the cache could not be updated
// would turn a performance problem into a correctness one.
func providerCachePutMany(source string, ttl time.Duration, items ...providerCacheItem) {
	if cacheDB == nil || len(items) == 0 {
		return
	}

	now := time.Now()
	rows := make([]models.ProviderCache, 0, len(items))
	for _, item := range items {
		payload, err := json.Marshal(item.value)
		if err != nil {
			logger.Log.Warnf("failed to encode %s cache entry %s: %s", source, item.key, err.Error())
			continue
		}
		rows = append(rows, models.ProviderCache{
			Source:    source,
			Key:       item.key,
			Payload:   string(payload),
			FetchedAt: now,
			ExpiresAt: now.Add(ttl),
		})
	}
	if len(rows) == 0 {
		return
	}

	// Upsert on the composite key: these are re-fetched on their TTL, so a second
	// answer for the same id must replace the first rather than collide with it.
	if err := cacheDB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "source"}, {Name: "key"}},
		UpdateAll: true,
	}).Create(&rows).Error; err != nil {
		logger.Log.Warnf("failed to persist %d %s cache row(s): %s", len(rows), source, err.Error())
	}
}

// providerCacheRows returns one source's unexpired entries, for warming its map at
// startup.
//
// Expired rows are left in place rather than deleted. They are not restored, they are
// overwritten by key on the next fetch, and the key space is bounded by what the
// service holds — while deleting them would empty the source and re-trigger the
// one-time JSON import below.
func providerCacheRows(source string) ([]models.ProviderCache, error) {
	if cacheDB == nil {
		return nil, nil
	}
	var rows []models.ProviderCache
	if err := cacheDB.Where("source = ? AND expires_at > ?", source, time.Now()).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// providerCacheDropSource forgets everything cached for one service endpoint. It is
// what invalidating a cache by hand means now that the maps are backed by rows: the
// emptied map has to be emptied in the database too, or the next restart restores
// exactly what the user just asked to discard.
func providerCacheDropSource(source string) {
	if cacheDB == nil {
		return
	}
	if err := cacheDB.Where("source = ?", source).Delete(&models.ProviderCache{}).Error; err != nil {
		logger.Log.Warnf("failed to clear the %s cache: %s", source, err.Error())
	}
}

// providerCacheImportJSON imports a legacy config/*.json cache once, when the source
// has no rows yet. It is a no-op afterwards.
//
// Worth doing for a one-hour TTL because the alternative is asking Lidarr for every
// artist, album and track file again on the first boot after the upgrade — the same
// cold start the MusicBrainz cache avoided when it moved (musicbrainzMigrateJSONIfNeeded).
//
// decode receives the file's bytes and returns the entries to store. The legacy file
// is left on disk: it is harmless, and a failed import that had deleted its own source
// would be unrecoverable.
func providerCacheImportJSON(source, path string, ttl time.Duration, decode func([]byte) ([]providerCacheItem, error)) {
	if cacheDB == nil {
		return
	}

	var count int64
	if err := cacheDB.Model(&models.ProviderCache{}).Where("source = ?", source).Count(&count).Error; err != nil {
		logger.Log.Warnf("could not check the %s cache: %s", source, err.Error())
		return
	}
	if count > 0 {
		return
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Log.Warnf("could not read the legacy %s cache: %s", source, err.Error())
		}
		return
	}

	items, err := decode(data)
	if err != nil {
		logger.Log.Warnf("could not parse the legacy %s cache: %s", source, err.Error())
		return
	}
	if len(items) == 0 {
		return
	}

	providerCachePutMany(source, ttl, items...)
	logger.Log.Infof("imported %d %s cache entr(ies) from %s", len(items), source, path)
}

// providerCacheDecodeMap adapts a legacy `map[string]T` cache file into import items.
func providerCacheDecodeMap[T any](data []byte) ([]providerCacheItem, error) {
	var entries map[string]T
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	items := make([]providerCacheItem, 0, len(entries))
	for key, value := range entries {
		items = append(items, providerCacheItem{key: key, value: value})
	}
	return items, nil
}

// providerCacheRestore decodes one source's rows into a caller-owned map. The caller
// holds its own lock; this only removes the five identical decode loops.
func providerCacheRestore[T any](source string, into map[string]T) error {
	rows, err := providerCacheRows(source)
	if err != nil {
		return err
	}
	for _, row := range rows {
		var value T
		if err := json.Unmarshal([]byte(row.Payload), &value); err != nil {
			// A row that no longer decodes is one refetch, not a startup failure.
			logger.Log.Debugf("skipping unreadable %s cache row %s: %s", source, row.Key, err.Error())
			continue
		}
		into[row.Key] = value
	}
	return nil
}

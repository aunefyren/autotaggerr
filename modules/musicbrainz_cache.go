// Persistent cache for MusicBrainz lookups other than the full release payload:
// artist entities, artist discographies, and a release-group's editions.
//
// These three used to be process-local maps with short TTLs, which made every
// restart a cold start — the first visit to an artist page re-paid up to five
// rate-limited requests for a discography that had not changed in a decade. This
// file gives them the same treatment the release cache already had: an in-memory
// front for the hot path, write-through to the database so the work survives a
// restart, and a jittered TTL so entries warmed together do not all expire at once.
//
// Everything is keyed by (entity, MBID) and stored as raw JSON, so caching another
// endpoint is a new constant rather than a schema change. With no database
// configured (the one-shot `--file` invocation, and most tests) the cache degrades
// to the old in-memory-only behaviour rather than failing.
package modules

import (
	"encoding/json"
	"math/rand"
	"sync"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
)

// Base TTLs per entity kind. All three are reference data that changes on the
// order of years, so these are short by the nature of the data and long only by
// the standard of a request cache — the mirror (package `mirror`) refreshes them
// on a schedule, and these values only bound how stale an *unmirrored* entry gets.
var (
	mbArtistTTL      = 7 * 24 * time.Hour
	mbDiscographyTTL = 7 * 24 * time.Hour
	mbEditionsTTL    = 7 * 24 * time.Hour
)

// mbEntityTTL returns the base TTL for an entity kind. An unknown kind gets the
// shortest of the three rather than a zero TTL, which would mean "already expired"
// and turn a typo into an uncached endpoint.
func mbEntityTTL(entity string) time.Duration {
	switch entity {
	case models.MBEntityArtist:
		return mbArtistTTL
	case models.MBEntityDiscography:
		return mbDiscographyTTL
	case models.MBEntityEditions:
		return mbEditionsTTL
	}
	return mbEditionsTTL
}

// mbCacheRecord is one cached lookup, held as encoded JSON. Storing the payload
// encoded rather than as a typed value is what lets one map serve three unrelated
// response shapes; the cost is a decode per read, which is nothing next to the
// rate-limited request it replaces.
type mbCacheRecord struct {
	payload   []byte
	fetchedAt time.Time
	expiresAt time.Time
}

var (
	mbEntityCache   = map[mbCacheKey]mbCacheRecord{}
	mbEntityCacheMu sync.RWMutex
)

// mbCacheKey is the composite (entity, MBID) key. A struct rather than a joined
// string so no MBID containing the separator could ever collide across kinds.
type mbCacheKey struct {
	entity string
	mbid   string
}

// mbCacheExpiry returns a jittered expiry (base + [0, base/2)), the same trick the
// release cache uses: entries warmed together by one mirror pass should not all
// come due in the same minute a week later.
func mbCacheExpiry(now time.Time, ttl time.Duration) time.Time {
	if ttl <= 0 {
		return now
	}
	return now.Add(ttl + time.Duration(rand.Int63n(int64(ttl/2))))
}

// mbCacheGet decodes a fresh cached entry into out. It reports false when the
// entry is absent, expired, or no longer decodes into the caller's type — a
// payload shape that changed between versions is treated as a miss and refetched
// rather than as an error the caller has to handle.
func mbCacheGet(entity, mbid string, out any) bool {
	rec, ok := mbCacheLookup(entity, mbid)
	if !ok || !time.Now().Before(rec.expiresAt) {
		return false
	}
	return json.Unmarshal(rec.payload, out) == nil
}

// mbCacheGetStale decodes a cached entry whatever its age. Callers use it as the
// fallback when MusicBrainz is unreachable: a discography from last month is a
// better answer for a browsing page than an empty one, and none of this data is
// live enough for staleness to mislead.
func mbCacheGetStale(entity, mbid string, out any) bool {
	rec, ok := mbCacheLookup(entity, mbid)
	if !ok {
		return false
	}
	return json.Unmarshal(rec.payload, out) == nil
}

func mbCacheLookup(entity, mbid string) (mbCacheRecord, bool) {
	mbEntityCacheMu.RLock()
	defer mbEntityCacheMu.RUnlock()
	rec, ok := mbEntityCache[mbCacheKey{entity: entity, mbid: mbid}]
	return rec, ok
}

// mbCachePut stores a freshly fetched value in memory and, when a database is
// configured, writes the row through immediately. Write-through rather than
// batched: these are single rows fetched seconds apart, not the thousands-per-scan
// churn that made the release JSON file expensive to rewrite.
//
// A failure to encode or persist is logged and swallowed. The caller already has
// the value it asked for, and failing its request because the cache could not be
// updated would turn a performance problem into a correctness one.
func mbCachePut(entity, mbid string, v any) {
	payload, err := json.Marshal(v)
	if err != nil {
		logger.Log.Warnf("failed to encode %s cache entry %s: %s", entity, mbid, err.Error())
		return
	}

	now := time.Now()
	rec := mbCacheRecord{
		payload:   payload,
		fetchedAt: now,
		expiresAt: mbCacheExpiry(now, mbEntityTTL(entity)),
	}

	mbEntityCacheMu.Lock()
	mbEntityCache[mbCacheKey{entity: entity, mbid: mbid}] = rec
	mbEntityCacheMu.Unlock()

	if cacheDB == nil {
		return
	}
	row := models.MusicbrainzEntityCache{
		Entity:    entity,
		MBID:      mbid,
		Payload:   string(payload),
		FetchedAt: rec.fetchedAt,
		ExpiresAt: rec.expiresAt,
	}
	if err := cacheDB.Save(&row).Error; err != nil {
		logger.Log.Warnf("failed to persist %s cache row %s: %s", entity, mbid, err.Error())
	}
}

// MusicbrainzEntityFresh reports whether a cached entry exists and is still within
// its TTL. The mirror uses it to decide what to refetch without decoding payloads
// it has no interest in.
func MusicbrainzEntityFresh(entity, mbid string) bool {
	rec, ok := mbCacheLookup(entity, mbid)
	return ok && time.Now().Before(rec.expiresAt)
}

// MusicbrainzEntityCounts returns how many cached entries exist per entity kind,
// so the mirror can report coverage without a query per entity.
func MusicbrainzEntityCounts() map[string]int {
	counts := map[string]int{}
	mbEntityCacheMu.RLock()
	defer mbEntityCacheMu.RUnlock()
	for key := range mbEntityCache {
		counts[key.entity]++
	}
	return counts
}

// musicbrainzLoadEntityCache warms the in-memory map from the database at startup.
// Rows whose payload is unreadable are skipped rather than fatal: a corrupt cache
// row costs one refetch, and refusing to start over it would be absurd.
func musicbrainzLoadEntityCache() error {
	if cacheDB == nil {
		return nil
	}

	var rows []models.MusicbrainzEntityCache
	if err := cacheDB.Find(&rows).Error; err != nil {
		return err
	}

	mbEntityCacheMu.Lock()
	defer mbEntityCacheMu.Unlock()
	for _, r := range rows {
		mbEntityCache[mbCacheKey{entity: r.Entity, mbid: r.MBID}] = mbCacheRecord{
			payload:   []byte(r.Payload),
			fetchedAt: r.FetchedAt,
			expiresAt: r.ExpiresAt,
		}
	}
	return nil
}

// MusicbrainzExpireEntity backdates one cached entry so the next read refetches it,
// without discarding the payload.
//
// This is what "check now" means for an entity lookup. Deleting the row would work
// too, but it also throws away the stale fallback — so if MusicBrainz happened to be
// down at that moment, a user who pressed Refresh to *improve* an artist page would
// be left with a blank one. Expiring forces the request and keeps the old copy as
// the safety net.
//
// MusicbrainzForgetEntity is the harsher variant, and identity verification wants
// it: there, a transport failure must surface rather than be answered by the very
// copy the call exists to re-verify.
func MusicbrainzExpireEntity(entity, mbid string) {
	key := mbCacheKey{entity: entity, mbid: mbid}

	mbEntityCacheMu.Lock()
	rec, ok := mbEntityCache[key]
	if ok {
		rec.expiresAt = time.Now().Add(-time.Minute)
		mbEntityCache[key] = rec
	}
	mbEntityCacheMu.Unlock()

	if !ok || cacheDB == nil {
		return
	}
	if err := cacheDB.Model(&models.MusicbrainzEntityCache{}).
		Where("entity = ? AND mb_id = ?", entity, mbid).
		Update("expires_at", rec.expiresAt).Error; err != nil {
		logger.Log.Warnf("failed to expire %s cache row %s: %s", entity, mbid, err.Error())
	}
}

// MusicbrainzForgetEntity drops one cached entry from memory and the database. It
// is called when an MBID is retired by a migration: the entry describes an entity
// that no longer answers under that ID, so keeping it would serve a name for
// something the rest of the app has stopped believing in.
func MusicbrainzForgetEntity(entity, mbid string) {
	mbEntityCacheMu.Lock()
	delete(mbEntityCache, mbCacheKey{entity: entity, mbid: mbid})
	mbEntityCacheMu.Unlock()

	if cacheDB == nil {
		return
	}
	if err := cacheDB.Where("entity = ? AND mb_id = ?", entity, mbid).
		Delete(&models.MusicbrainzEntityCache{}).Error; err != nil {
		logger.Log.Warnf("failed to drop %s cache row %s: %s", entity, mbid, err.Error())
	}
}

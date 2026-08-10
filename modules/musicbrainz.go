package modules

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
)

var (
	lastQueryTime time.Time
	queryMutex    sync.Mutex
	rateLimit     = time.Second
	// musicbrainzBaseURL is the API root; a package var so tests can point it at a
	// local httptest server instead of the live service.
	musicbrainzBaseURL              = "https://musicbrainz.org/ws/2"
	musicbrainzReleaseCachePath     = "config/mb_releases.json"
	musicbrainzReleaseCacheDuration = 7 * 24 * time.Hour // 1 week (base TTL)
	musicbrainzReleaseCacheJitter   = 7 * 24 * time.Hour // up to +1 week of jitter (7-14 days total)
	musicbrainzReleaseCache         = map[string]models.CachedMusicBrainzRelease{}
	musicbrainzReleaseCacheMu       sync.RWMutex

	// inflightFetches coalesces concurrent fetches of the *same* release. The cache
	// is only populated once a fetch completes, so without this every worker that
	// starts on a track of the same album during a cold scan misses the cache and
	// issues its own identical request — each one serialized behind the global 1 req/s
	// limiter. An album's tracks are adjacent in walk order and therefore land on
	// different workers at the same time, which made this the common case rather than
	// a rare one.
	inflightFetches   = map[string]*inflightFetch{}
	inflightFetchesMu sync.Mutex

	// Fetch accounting, for measuring what a scan actually cost upstream.
	statCacheHits atomic.Int64
	statCoalesced atomic.Int64
	statFetches   atomic.Int64
)

// inflightFetch is one in-progress release fetch. The leader fills release/err and
// closes done; waiters read them only after done is closed, so the close provides
// the happens-before edge.
type inflightFetch struct {
	done    chan struct{}
	release models.MusicBrainzReleaseResponse
	err     error
}

// MusicbrainzFetchStats counts what release lookups did during a run: served from
// cache, coalesced onto another goroutine's in-flight request, or actually fetched
// over the network. Fetches are the only ones that pay the rate limiter, so this is
// what makes a cold-vs-warm scan comparison meaningful.
type MusicbrainzFetchStats struct {
	CacheHits int64 `json:"cache_hits"`
	Coalesced int64 `json:"coalesced"`
	Fetches   int64 `json:"fetches"`
}

// MusicbrainzStatsSnapshot returns the counters accumulated so far.
func MusicbrainzStatsSnapshot() MusicbrainzFetchStats {
	return MusicbrainzFetchStats{
		CacheHits: statCacheHits.Load(),
		Coalesced: statCoalesced.Load(),
		Fetches:   statFetches.Load(),
	}
}

// MusicbrainzResetStats zeroes the counters. Callers that report per-run figures
// (a scan, a drift sync) reset at the start of the run.
func MusicbrainzResetStats() {
	statCacheHits.Store(0)
	statCoalesced.Store(0)
	statFetches.Store(0)
}

// SetMusicBrainzRateLimit sets the minimum interval between MusicBrainz requests
// from a requests-per-second figure (the `rate_limit` on the MusicBrainz data
// source row). Non-positive values leave the current interval alone, so a blank or
// zeroed config cannot accidentally remove the throttle. Raising it above the ~1
// req/s MusicBrainz permits publicly is only appropriate against a local mirror.
func SetMusicBrainzRateLimit(perSecond float64) {
	if perSecond <= 0 {
		return
	}
	interval := time.Duration(float64(time.Second) / perSecond)
	if interval <= 0 {
		return
	}

	queryMutex.Lock()
	defer queryMutex.Unlock()
	rateLimit = interval
}

// musicbrainzCacheExpiry returns a jittered expiry time (base + [0, jitter))
// so entries fetched together during one scan don't all expire at once.
func musicbrainzCacheExpiry(now time.Time) time.Time {
	return now.Add(musicbrainzReleaseCacheDuration + time.Duration(rand.Int63n(int64(musicbrainzReleaseCacheJitter))))
}

// RateLimit wraps any API function and ensures at least 1s between executions
func RateLimit() error {
	queryMutex.Lock()
	defer queryMutex.Unlock()

	now := time.Now()
	elapsed := now.Sub(lastQueryTime)
	if elapsed < rateLimit {
		time.Sleep(rateLimit - elapsed)
	}

	lastQueryTime = time.Now()
	return nil
}

// GetMusicBrainzRelease returns a release from the cache, or fetches it. Concurrent
// callers asking for the same release share a single fetch (see inflightFetches).
func GetMusicBrainzRelease(mbID string) (models.MusicBrainzReleaseResponse, error) {
	if release, ok := cachedFreshRelease(mbID); ok {
		statCacheHits.Add(1)
		logger.Log.Debug("returning cached release for ID: " + mbID)
		return release, nil
	}

	inflightFetchesMu.Lock()
	if leader, ok := inflightFetches[mbID]; ok {
		inflightFetchesMu.Unlock()
		statCoalesced.Add(1)
		logger.Log.Debug("joining in-flight release fetch for ID: " + mbID)
		<-leader.done
		return leader.release, leader.err
	}
	// Re-check under the in-flight lock: a fetch may have completed and populated the
	// cache between the lookup above and here.
	if release, ok := cachedFreshRelease(mbID); ok {
		inflightFetchesMu.Unlock()
		statCacheHits.Add(1)
		return release, nil
	}
	fetch := &inflightFetch{done: make(chan struct{})}
	inflightFetches[mbID] = fetch
	inflightFetchesMu.Unlock()

	statFetches.Add(1)
	// propagate the real cause (HTTP status / transport / parse) instead of a
	// generic message, so the file-level log can tell them apart.
	//
	// The retry belongs here rather than inside QueryMusicBrainzReleaseData for the
	// same reason the stale fallback does: this is the point where one album's worth
	// of waiting workers are represented by a single request, so one leader retrying
	// is one extra request — not one per track.
	release, err := retryTransient("release "+mbID, func() (models.MusicBrainzReleaseResponse, error) {
		return QueryMusicBrainzReleaseData(mbID, files.ConfigFile.AutotaggerrVersion)
	})
	if err != nil {
		// Standing in for the failure *here* rather than at the call site is what makes
		// it worth anything: the fallback lands before the result is handed to the
		// coalesced waiters, so an album whose tracks all missed the cache together
		// survives an outage together.
		if stale, ok := staleCachedRelease(mbID, err); ok {
			release, err = stale, nil
		}
	}
	fetch.release, fetch.err = release, err

	inflightFetchesMu.Lock()
	delete(inflightFetches, mbID)
	inflightFetchesMu.Unlock()
	close(fetch.done) // releases every waiter with the leader's result

	return fetch.release, fetch.err
}

// cachedFreshRelease returns a cached release if it is present and unexpired. A zero
// ExpiresAt (pre-jitter cache entry) counts as expired so it gets refreshed once and
// upgraded to the jittered format.
func cachedFreshRelease(mbID string) (models.MusicBrainzReleaseResponse, bool) {
	musicbrainzReleaseCacheMu.RLock()
	cached, ok := musicbrainzReleaseCache[mbID]
	musicbrainzReleaseCacheMu.RUnlock()
	if !ok || !time.Now().Before(cached.ExpiresAt) {
		return models.MusicBrainzReleaseResponse{}, false
	}
	return cached.Release, true
}

// staleCachedRelease returns an *expired* cached release to stand in for a fetch
// that failed, or false if there is nothing usable to stand in with. It is the
// release cache's mbCacheGetStale — same job, but releases have their own map and
// table rather than living in the entity cache, so they need their own reader.
//
// An expiry says when the data is worth re-checking, not when it stops describing
// the release. Between those two readings sits the whole bug this exists for: a
// release held for months, its TTL lapsed by an hour, thrown away over one 503 —
// and with it the correlation for every file on that album, which then left the disk
// view and made the album read as mismatched against the manager. Week-old track
// titles are a better answer than no answer, by a wide margin.
//
// A gone entity is the case that must NOT be served from stale: MusicBrainz saying
// the release no longer exists is an *answer*, and the migration recorded from it is
// how a dead ID gets dealt with once instead of re-failing every run. Papering over
// it with the copy we happen to still hold would keep the file pointed at an ID
// nothing upstream will ever resolve again.
//
// This is a deliberate divergence from GetMusicBrainzArtist, which *does* serve a
// stale artist through a 404 once the deletion is recorded. The difference is what
// the answer is used for: an artist lookup renders a page, while a release drives
// the tags written to disk. Re-tagging a whole album against a release MusicBrainz
// has deleted is a write we would have to undo. Refusing here costs nothing the
// album cares about — the deletion is already recorded by the caller, so the pending
// migration is what keeps the album visible and gives the user a way to repoint it.
//
// Everything else falls back, and note that this asks "is it gone?" rather than the
// narrower "is it ErrTransient?" — deliberately, now that both sentinels exist. A
// 400, an unparseable body, an unread response: none of them are MusicBrainz saying
// anything about this release, and answering with week-old truth beats dropping an
// album out of the disk view over a failure mode nobody anticipated. ErrTransient
// marks what is *known* to be worth retrying; it is not a list of the only things
// that may fail. The empty-ID guard is for a cache entry that somehow holds no release:
// returning it would send the caller into "track not in release", which reads as a
// correlation problem and is a far worse failure than the one being handled.
func staleCachedRelease(mbID string, fetchErr error) (models.MusicBrainzReleaseResponse, bool) {
	if errors.Is(fetchErr, ErrEntityGone) {
		return models.MusicBrainzReleaseResponse{}, false
	}

	musicbrainzReleaseCacheMu.RLock()
	cached, ok := musicbrainzReleaseCache[mbID]
	musicbrainzReleaseCacheMu.RUnlock()
	if !ok || cached.Release.ID == "" {
		return models.MusicBrainzReleaseResponse{}, false
	}

	logger.Log.Warnf("serving stale cached release %s (cached %s, expired %s) because the fetch failed: %s",
		mbID, cached.Timestamp.Format(time.RFC3339), cached.ExpiresAt.Format(time.RFC3339), fetchErr.Error())
	return cached.Release, true
}

// MusicbrainzExtendExpiry pushes a cached release's expiry out to a longer TTL.
// The mirror uses it to tier freshness by how much the collection cares: an
// edition nobody owns is reference data that can go stale for weeks, while a
// release with files on disk drives the tags on those files and should not.
//
// Extending rather than setting: the jitter already spread the base expiry, and
// clamping every long-TTL entry to the same instant would undo that.
func MusicbrainzExtendExpiry(mbID string, ttl time.Duration) {
	musicbrainzReleaseCacheMu.Lock()
	cached, ok := musicbrainzReleaseCache[mbID]
	if ok {
		if extended := cached.Timestamp.Add(ttl); extended.After(cached.ExpiresAt) {
			cached.ExpiresAt = extended
			musicbrainzReleaseCache[mbID] = cached
		} else {
			ok = false
		}
	}
	entry := musicbrainzReleaseCache[mbID]
	musicbrainzReleaseCacheMu.Unlock()

	if !ok || cacheDB == nil {
		return
	}
	if err := musicbrainzStoreDB(mbID, entry); err != nil {
		logger.Log.Warnf("failed to extend MusicBrainz cache expiry for %s: %s", mbID, err.Error())
	}
}

// MusicbrainzReleaseFresh reports whether a release is cached and unexpired. The
// mirror uses it to decide what a refresh pass still owes without paying the
// decode — and, more importantly, without calling GetMusicBrainzRelease, which
// would fetch the very entry it is trying to ask about.
func MusicbrainzReleaseFresh(mbID string) bool {
	_, ok := cachedFreshRelease(mbID)
	return ok
}

// MusicbrainzReleaseCacheSize returns how many releases are cached, for coverage
// reporting.
func MusicbrainzReleaseCacheSize() int {
	musicbrainzReleaseCacheMu.RLock()
	defer musicbrainzReleaseCacheMu.RUnlock()
	return len(musicbrainzReleaseCache)
}

// readBodySnippet reads a small, bounded portion of an error response body so it
// can be included in an error message without risking a huge/streaming read.
func readBodySnippet(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 512))
	return strings.TrimSpace(string(b))
}

func QueryMusicBrainzReleaseData(mbID string, autotaggerrVersion string) (models.MusicBrainzReleaseResponse, error) {
	var apiResponse models.MusicBrainzReleaseResponse

	// rate limit the request to comply
	err := RateLimit()
	if err != nil {
		logger.Log.Error("failed to rate limit. error: " + err.Error())
		return apiResponse, errors.New("failed to rate limit")
	}

	// do API request
	url := fmt.Sprintf("%s/release/%s?inc=recordings+labels+artists+genres+tags+release-groups+isrcs&fmt=json", musicbrainzBaseURL, mbID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		logger.Log.Error("failed to create new request. error: " + err.Error())
		return apiResponse, errors.New("failed to create new request")
	}

	// set User-Agent to comply with MB guidelines
	req.Header.Set("User-Agent", "Autotaggerr/"+autotaggerrVersion+" (https://github.com/aunefyren/autotaggerr)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// transport failure (DNS, timeout, connection reset) — usually transient
		return apiResponse, newTransientError(err, "MusicBrainz request failed for release %q (transport error)", mbID)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet := readBodySnippet(resp.Body)
		switch {
		case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
			// The release genuinely does not exist upstream: deleted, or an ID that
			// was never valid. A *merged* ID does not land here — MusicBrainz still
			// resolves those, which is handled below. Recorded as a migration so the
			// dead ID is dealt with once instead of re-failing on every run, and
			// returned as ErrEntityGone so callers can tell it from an outage.
			RecordDeletion(models.MigrationEntityRelease, mbID)
			return apiResponse, newGoneError(models.MigrationEntityRelease, mbID, resp.StatusCode, snippet)
		case transientStatus(resp.StatusCode):
			return apiResponse, newTransientError(nil, "MusicBrainz throttled/unavailable for release %q (HTTP %d, retry later): %s", mbID, resp.StatusCode, snippet)
		default:
			return apiResponse, fmt.Errorf("MusicBrainz returned HTTP %d for release %q: %s", resp.StatusCode, mbID, snippet)
		}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return apiResponse, fmt.Errorf("failed to read MusicBrainz response body for release %q: %w", mbID, err)
	}

	err = json.Unmarshal(body, &apiResponse)
	if err != nil {
		return apiResponse, fmt.Errorf("failed to parse MusicBrainz response for release %q: %w", mbID, err)
	}

	// A merge is only visible here: MusicBrainz resolved the old ID and answered with
	// the surviving release, whose payload id is the new one. Cache under the *asked
	// for* key as well as recording the move — the caller is mid-scan holding the old
	// ID, and failing its lookup to make a point about identity would break tagging
	// for a file whose metadata we are holding in our hands. The remap that retires
	// the old key happens when the migration is applied.
	canonicalID := mbID
	if apiResponse.ID != "" && apiResponse.ID != mbID {
		RecordRedirect(models.MigrationEntityRelease, mbID, apiResponse.ID, apiResponse.Title)
		canonicalID = apiResponse.ID
	}

	now := time.Now()
	entry := models.CachedMusicBrainzRelease{
		Release:   apiResponse,
		Timestamp: now,
		ExpiresAt: musicbrainzCacheExpiry(now),
	}
	musicbrainzReleaseCacheMu.Lock()
	musicbrainzReleaseCache[mbID] = entry
	if canonicalID != mbID {
		musicbrainzReleaseCache[canonicalID] = entry
	}
	musicbrainzReleaseCacheMu.Unlock()

	// Persist the entry. With a database configured we write through this single
	// row immediately (cheap, unlike rewriting a growing JSON blob); otherwise we
	// mark the JSON cache dirty for the batched flush (see cache.go).
	if cacheDB != nil {
		if err := musicbrainzStoreDB(mbID, entry); err != nil {
			logger.Log.Warnf("failed to persist MusicBrainz cache row %s: %s", mbID, err.Error())
		}
		if canonicalID != mbID {
			if err := musicbrainzStoreDB(canonicalID, entry); err != nil {
				logger.Log.Warnf("failed to persist MusicBrainz cache row %s: %s", canonicalID, err.Error())
			}
		}
	}

	logger.Log.Trace(fmt.Sprintf("api response: %+v", apiResponse))

	return apiResponse, nil
}

func MusicBrainzArtistsArrayToString(artists []models.ArtistCredit, tagger models.TaggerSettings) string {
	artistString := ""
	for index, feature := range artists {
		logger.Log.Trace("processing featuring artist: " + feature.Artist.Name)

		// choose join phrase based on settings
		joinPhrase := tagger.CustomArtistDelimiter
		if !tagger.UseCustomArtistDelimiter {
			joinPhrase = feature.Joinphrase
		} else if index+1 == len(artists) {
			joinPhrase = ""
		} else if len(artists) > 2 && index+1 < len(artists)-1 && tagger.CustomArtistDelimiterCommas {
			joinPhrase = ", "
		}

		logger.Log.Trace("feature join phrase to use: " + joinPhrase)

		// either use original release artist name or current name
		if tagger.UseCurrentArtistName {
			artistString += feature.Artist.Name + joinPhrase
		} else {
			artistString += feature.Name + joinPhrase
		}
	}

	return artistString
}

func MusicBrainzDateStringToDateTime(dateStr string) (time.Time, error) {
	// Go's time layout uses this reference date: "2006-01-02 15:04:05"
	layout := "2006-01-02"
	var parsedTime time.Time

	parsedTime, err := time.Parse(layout, dateStr)
	if err != nil {
		return parsedTime, err
	}

	return parsedTime, nil
}

// MusicbrainzLoadCache warms the in-memory map at startup, importing the legacy
// JSON cache once if the table is still empty.
//
// There is no JSON *write* path any more. Without a database the map is simply
// process-local, which is the correct behaviour for a cache and is what the
// `--file` one-shot and the tests actually want; the alternative was a whole-map
// rewrite driven by a flusher that only ran during a scan.
func MusicbrainzLoadCache() error {
	if cacheDB == nil {
		return nil
	}
	if err := musicbrainzMigrateJSONIfNeeded(); err != nil {
		logger.Log.Warnf("MusicBrainz cache JSON migration failed: %s", err.Error())
	}
	return musicbrainzLoadFromDB()
}

// musicbrainzStoreDB upserts one release into the DB-backed cache. The MB ID is
// the primary key, so Save inserts or updates in one call.
func musicbrainzStoreDB(mbID string, entry models.CachedMusicBrainzRelease) error {
	payload, err := json.Marshal(entry.Release)
	if err != nil {
		return err
	}
	row := models.MusicbrainzReleaseCache{
		MBID:      mbID,
		Payload:   string(payload),
		FetchedAt: entry.Timestamp,
		ExpiresAt: entry.ExpiresAt,
	}
	return cacheDB.Save(&row).Error
}

// musicbrainzLoadFromDB loads every cached release row into the in-memory map.
func musicbrainzLoadFromDB() error {
	var rows []models.MusicbrainzReleaseCache
	if err := cacheDB.Find(&rows).Error; err != nil {
		return err
	}

	musicbrainzReleaseCacheMu.Lock()
	defer musicbrainzReleaseCacheMu.Unlock()
	for _, r := range rows {
		var release models.MusicBrainzReleaseResponse
		if err := json.Unmarshal([]byte(r.Payload), &release); err != nil {
			logger.Log.Warnf("skipping corrupt MusicBrainz cache row %s: %s", r.MBID, err.Error())
			continue
		}
		musicbrainzReleaseCache[r.MBID] = models.CachedMusicBrainzRelease{
			Release:   release,
			Timestamp: r.FetchedAt,
			ExpiresAt: r.ExpiresAt,
		}
	}
	return nil
}

// musicbrainzMigrateJSONIfNeeded imports a legacy config/mb_releases.json into the
// database once, when the DB cache is still empty. It is a no-op afterwards.
//
// The file is removed once its contents are in the database — see
// removeLegacyCacheFile for why the import no longer leaves its own source behind.
func musicbrainzMigrateJSONIfNeeded() error {
	var count int64
	if err := cacheDB.Model(&models.MusicbrainzReleaseCache{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		removeLegacyCacheFile("musicbrainz release", musicbrainzReleaseCachePath)
		return nil
	}

	data, err := os.ReadFile(musicbrainzReleaseCachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	legacy := map[string]models.CachedMusicBrainzRelease{}
	if err := json.Unmarshal(data, &legacy); err != nil {
		// Unparseable: keep the file, since this is the one failure the deletion
		// would make unrecoverable.
		return err
	}
	for id, entry := range legacy {
		if err := musicbrainzStoreDB(id, entry); err != nil {
			return err
		}
	}
	if len(legacy) > 0 {
		logger.Log.Infof("migrated %d MusicBrainz cache entries from JSON into the database", len(legacy))
	}
	removeLegacyCacheFile("musicbrainz release", musicbrainzReleaseCachePath)
	return nil
}

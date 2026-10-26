package modules

import (
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// Test seams. These are exported production symbols that exist only for tests in
// *other* packages, which is a trade worth naming.
//
// The MusicBrainz endpoint and the cache maps are unexported, so until now only tests
// inside `modules` could stand MusicBrainz up against an httptest server. Anything
// outside it — the scan runner, the refresh verb, the API handlers — could only be
// covered on paths that return before the external call, which is documented in
// docs/development.md as a real limitation. The alternative to this file is either
// tests that hit musicbrainz.org for real (flaky, rate limited, and failing for
// reasons unrelated to the change that broke them) or whole packages left untested.
//
// They take *testing.T and register their own cleanup, so they cannot be called
// from production code by accident: there is no *testing.T to hand them.

// SetMusicBrainzBaseURLForTest points MusicBrainz lookups at a stub server and
// clears every cache and the rate limiter, restoring all of it on cleanup. Without
// the reset one test's stub response answers the next test's call.
func SetMusicBrainzBaseURLForTest(t *testing.T, baseURL string) {
	t.Helper()

	useMusicBrainzBaseURL(t, baseURL)
	ResetMusicBrainzCachesForTest(t)
}

// useMusicBrainzBaseURL swaps the endpoint for the test's duration, keeping its
// contact, and puts the previous one back — nil included — on cleanup. The URL is
// stored as given, so a test can hand it one SetMusicBrainzEndpoint would tidy.
func useMusicBrainzBaseURL(t *testing.T, baseURL string) {
	t.Helper()
	previous := musicbrainzEndpoint.Load()
	next := mbEndpoint{baseURL: baseURL}
	if previous != nil {
		next.contact = previous.contact
	}
	musicbrainzEndpoint.Store(&next)
	t.Cleanup(func() { musicbrainzEndpoint.Store(previous) })
}

// ResetMusicBrainzCachesForTest empties the release and entity caches and clears
// the rate limiter so the next request does not sleep.
func ResetMusicBrainzCachesForTest(t *testing.T) {
	t.Helper()

	reset := func() {
		musicbrainzReleaseCacheMu.Lock()
		musicbrainzReleaseCache = map[string]models.CachedMusicBrainzRelease{}
		musicbrainzReleaseCacheMu.Unlock()

		mbEntityCacheMu.Lock()
		mbEntityCache = map[mbCacheKey]mbCacheRecord{}
		mbEntityCacheMu.Unlock()

		queryMutex.Lock()
		lastQueryTime = time.Time{}
		queryMutex.Unlock()
	}

	reset()
	t.Cleanup(reset)
}

// SeedMusicBrainzEntityForTest puts a value straight into the entity cache, so a
// caller can exercise the cache-hit path without a stub server.
func SeedMusicBrainzEntityForTest(t *testing.T, entity, mbid string, value any) {
	t.Helper()
	mbCachePut(entity, mbid, value)
}

// SetMetaflacAvailableForTest makes MetaflacAvailable answer ok, whatever is on PATH,
// restoring the real lookup on cleanup. CI installs flac, so the missing case cannot
// otherwise be reached.
func SetMetaflacAvailableForTest(t *testing.T, ok bool) {
	t.Helper()
	metaflacMu.Lock()
	checked, found := metaflacChecked, metaflacFound
	metaflacChecked, metaflacFound = true, ok
	metaflacMu.Unlock()
	t.Cleanup(func() {
		metaflacMu.Lock()
		metaflacChecked, metaflacFound = checked, found
		metaflacMu.Unlock()
	})
}

// SeedMusicBrainzReleaseForTest puts a release straight into the release cache, for a
// caller that reads it cache-only (CachedRelease) and has no stub server to fetch from.
// The entry is removed on cleanup.
func SeedMusicBrainzReleaseForTest(t *testing.T, release models.MusicBrainzReleaseResponse) {
	t.Helper()
	now := time.Now()
	musicbrainzReleaseCacheMu.Lock()
	musicbrainzReleaseCache[release.ID] = models.CachedMusicBrainzRelease{Release: release, Timestamp: now, ExpiresAt: now.Add(time.Hour)}
	musicbrainzReleaseCacheMu.Unlock()
	t.Cleanup(func() {
		musicbrainzReleaseCacheMu.Lock()
		delete(musicbrainzReleaseCache, release.ID)
		musicbrainzReleaseCacheMu.Unlock()
	})
}

// ResetPlexAlbumCacheForTest empties the Plex folder cache, before and after the test.
func ResetPlexAlbumCacheForTest(t *testing.T) {
	t.Helper()
	reset := func() {
		plexAlbumKeyCacheMu.Lock()
		plexAlbumKeyCache = map[string]models.PlexAlbumKeyCache{}
		plexAlbumKeyCacheMu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

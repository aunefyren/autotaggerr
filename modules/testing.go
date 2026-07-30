package modules

import (
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// Test seams. These are exported production symbols that exist only for tests in
// *other* packages, which is a trade worth naming.
//
// `musicbrainzBaseURL` and the cache maps are unexported, so until now only tests
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

	original := musicbrainzBaseURL
	musicbrainzBaseURL = baseURL
	t.Cleanup(func() { musicbrainzBaseURL = original })

	ResetMusicBrainzCachesForTest(t)
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

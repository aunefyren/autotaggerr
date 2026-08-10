package modules

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/metadata"
	"github.com/aunefyren/autotaggerr/models"
)

// withMockMB points the MusicBrainz client at a local test server and resets the
// cache + rate limiter so tests are isolated and don't incur the 1s throttle.
func withMockMB(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	orig := musicbrainzBaseURL
	musicbrainzBaseURL = srv.URL
	t.Cleanup(func() { musicbrainzBaseURL = orig })

	musicbrainzReleaseCacheMu.Lock()
	musicbrainzReleaseCache = map[string]models.CachedMusicBrainzRelease{}
	musicbrainzReleaseCacheMu.Unlock()

	// The entity cache answers artist/discography/edition lookups, so it has to be
	// cleared here too or one test's stub response leaks into the next.
	mbEntityCacheMu.Lock()
	mbEntityCache = map[mbCacheKey]mbCacheRecord{}
	mbEntityCacheMu.Unlock()

	queryMutex.Lock()
	origRate := rateLimit
	lastQueryTime = time.Time{} // ensure the next RateLimit() call doesn't sleep
	// A retried request is spaced by the limiter (see retryTransient), so against the
	// live 1s interval every transient-failure test would pay a real second per retry.
	// The interval's *value* is covered by its own test; here it only has to exist.
	rateLimit = time.Millisecond
	queryMutex.Unlock()
	t.Cleanup(func() {
		queryMutex.Lock()
		rateLimit = origRate
		queryMutex.Unlock()
	})

	return srv
}

func TestQueryMusicBrainzReleaseData(t *testing.T) {
	want := models.MusicBrainzReleaseResponse{
		ID:    "rel-1",
		Title: "OK Computer",
		Media: []models.MusicBrainzMedia{
			{Tracks: []models.Track{{ID: "trk-1", Title: "Airbag"}}},
		},
	}

	var gotUA, gotPath string
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(want)
	})

	got, err := QueryMusicBrainzReleaseData("rel-1", "9.9.9")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "rel-1" || got.Title != "OK Computer" {
		t.Errorf("parsed release = %+v", got)
	}
	if len(got.Media) != 1 || len(got.Media[0].Tracks) != 1 || got.Media[0].Tracks[0].ID != "trk-1" {
		t.Errorf("parsed media/tracks = %+v", got.Media)
	}
	if !strings.HasPrefix(gotUA, "Autotaggerr/9.9.9") {
		t.Errorf("User-Agent = %q, want prefix Autotaggerr/9.9.9", gotUA)
	}
	if !strings.Contains(gotPath, "/release/rel-1") {
		t.Errorf("request path = %q, want to contain /release/rel-1", gotPath)
	}
}

// TestGetMusicBrainzReleaseCaches confirms the release is fetched once and then
// served from the in-memory cache, and that the entry gets a future (jittered)
// expiry.
func TestGetMusicBrainzReleaseCaches(t *testing.T) {
	var hits int32
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_ = json.NewEncoder(w).Encode(models.MusicBrainzReleaseResponse{ID: "rel-2", Title: "Kid A"})
	})

	for i := 0; i < 3; i++ {
		got, err := GetMusicBrainzRelease("rel-2")
		if err != nil {
			t.Fatalf("call %d unexpected error: %v", i, err)
		}
		if got.Title != "Kid A" {
			t.Fatalf("call %d title = %q, want Kid A", i, got.Title)
		}
	}

	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("server hit %d times, want 1 (subsequent calls should be cached)", n)
	}

	musicbrainzReleaseCacheMu.RLock()
	entry, ok := musicbrainzReleaseCache["rel-2"]
	musicbrainzReleaseCacheMu.RUnlock()
	if !ok {
		t.Fatal("release not stored in cache")
	}
	if !entry.ExpiresAt.After(time.Now()) {
		t.Errorf("cache entry ExpiresAt %v is not in the future", entry.ExpiresAt)
	}
	// jitter keeps expiry within [now+7d, now+14d]
	if entry.ExpiresAt.After(time.Now().Add(15 * 24 * time.Hour)) {
		t.Errorf("cache entry ExpiresAt %v exceeds the jitter window", entry.ExpiresAt)
	}
}

// TestGetMusicBrainzReleaseCoalescesConcurrent is the cold-scan case: several
// workers land on tracks of the same album at once, all miss the cache (nothing has
// been written to it yet), and would each issue the same request — every one of them
// serialized behind the 1 req/s limiter. They must collapse into a single fetch.
//
// The handler holds the fetch open until every caller has arrived, so the test
// cannot pass by luck because the requests happened to be fast enough to look
// coalesced. The hold is bounded so that an implementation *without* coalescing
// fails on the assertions in a few seconds rather than wedging until the package
// timeout.
func TestGetMusicBrainzReleaseCoalescesConcurrent(t *testing.T) {
	const callers = 8

	var hits int32
	release := make(chan struct{})
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		select {
		case <-release:
		case <-time.After(10 * time.Second):
		}
		_ = json.NewEncoder(w).Encode(models.MusicBrainzReleaseResponse{ID: "rel-c", Title: "Amnesiac"})
	})
	MusicbrainzResetStats()

	var wg sync.WaitGroup
	errs := make([]error, callers)
	titles := make([]string, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := GetMusicBrainzRelease("rel-c")
			errs[i], titles[i] = err, got.Title
		}(i)
	}

	// Wait until all but the leader are parked on the in-flight entry, then let the
	// single request complete.
	waitFor(t, func() bool { return MusicbrainzStatsSnapshot().Coalesced == callers-1 })
	close(release)
	wg.Wait()

	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("server hit %d times, want 1 — concurrent callers should share one fetch", n)
	}
	for i := 0; i < callers; i++ {
		if errs[i] != nil {
			t.Errorf("caller %d: unexpected error: %v", i, errs[i])
		}
		if titles[i] != "Amnesiac" {
			t.Errorf("caller %d: title = %q, want Amnesiac", i, titles[i])
		}
	}

	stats := MusicbrainzStatsSnapshot()
	if stats.Fetches != 1 || stats.Coalesced != callers-1 {
		t.Errorf("stats = %+v, want 1 fetch and %d coalesced", stats, callers-1)
	}

	// The in-flight entry must not leak once the fetch is done.
	inflightFetchesMu.Lock()
	leftover := len(inflightFetches)
	inflightFetchesMu.Unlock()
	if leftover != 0 {
		t.Errorf("%d in-flight entries left behind", leftover)
	}
}

// TestGetMusicBrainzReleaseCoalescesError checks waiters receive the leader's error
// rather than a zero value, and that the failure is not cached — the next call must
// be free to retry.
//
// It also pins where the transient retry sits relative to the coalescing. The mock
// answers 503, so each fetch costs `mbTransientRetries + 1` requests — and *one*
// fetch is all two concurrent callers produce between them. If the retry sat outside
// the in-flight map instead, every waiter would come back from the leader's failure
// and retry on its own, turning one album's cold miss during a blip into a burst.
func TestGetMusicBrainzReleaseCoalescesError(t *testing.T) {
	attempts := int32(mbTransientRetries + 1)

	var hits int32
	release := make(chan struct{})
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			select {
			case <-release:
			case <-time.After(10 * time.Second):
			}
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	MusicbrainzResetStats()

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = GetMusicBrainzRelease("rel-e")
		}(i)
	}
	waitFor(t, func() bool { return MusicbrainzStatsSnapshot().Coalesced == 1 })
	close(release)
	wg.Wait()

	for i, err := range errs {
		if err == nil {
			t.Errorf("caller %d: expected the leader's error, got nil", i)
		}
	}
	if n := atomic.LoadInt32(&hits); n != attempts {
		t.Errorf("server hit %d times, want %d — two callers must share one fetch, retries and all", n, attempts)
	}

	// A failed fetch leaves nothing cached, so a later call goes back to the server.
	if _, err := GetMusicBrainzRelease("rel-e"); err == nil {
		t.Error("expected the second call to hit the server and fail too")
	}
	if n := atomic.LoadInt32(&hits); n != 2*attempts {
		t.Errorf("server hit %d times after the second call, want %d — a failure must not be cached", n, 2*attempts)
	}
}

// seedExpiredRelease puts an already-expired entry in the release cache: the state
// a library reaches on its own after a week of not being scanned.
func seedExpiredRelease(mbID, title string) {
	musicbrainzReleaseCacheMu.Lock()
	musicbrainzReleaseCache[mbID] = models.CachedMusicBrainzRelease{
		Release:   models.MusicBrainzReleaseResponse{ID: mbID, Title: title},
		Timestamp: time.Now().Add(-8 * 24 * time.Hour),
		ExpiresAt: time.Now().Add(-time.Hour),
	}
	musicbrainzReleaseCacheMu.Unlock()
}

// TestGetMusicBrainzReleaseServesStaleOnOutage is the regression for albums going
// mismatched during a MusicBrainz outage. An expired entry is not a reason to have
// no answer: failing here discards the correlation for every file on the album,
// which then drops out of the disk view.
func TestGetMusicBrainzReleaseServesStaleOnOutage(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusTooManyRequests, http.StatusInternalServerError} {
		withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		})
		seedExpiredRelease("rel-stale", "Still Known")

		got, err := GetMusicBrainzRelease("rel-stale")
		if err != nil {
			t.Fatalf("HTTP %d: expected the stale entry to stand in, got error: %v", status, err)
		}
		if got.Title != "Still Known" {
			t.Errorf("HTTP %d: served %q, want the cached title", status, got.Title)
		}
	}
}

// A transport failure is the same case as a 5xx — the service is unreachable, which
// is not an answer about the release.
func TestGetMusicBrainzReleaseServesStaleOnTransportFailure(t *testing.T) {
	srv := withMockMB(t, func(w http.ResponseWriter, r *http.Request) {})
	srv.Close() // nothing is listening: the request fails before any status exists
	seedExpiredRelease("rel-dead-socket", "Unreachable")

	got, err := GetMusicBrainzRelease("rel-dead-socket")
	if err != nil {
		t.Fatalf("expected the stale entry to stand in, got error: %v", err)
	}
	if got.Title != "Unreachable" {
		t.Errorf("served %q, want the cached title", got.Title)
	}
}

// A gone entity is an *answer*, and the one case the fallback must not swallow:
// serving the copy we still hold would keep files pointed at an ID that will never
// resolve again, and would hide the migration that deals with it.
func TestGetMusicBrainzReleaseDoesNotServeStaleWhenGone(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusGone} {
		withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		})
		seedExpiredRelease("rel-deleted", "Deleted Upstream")

		if _, err := GetMusicBrainzRelease("rel-deleted"); !errors.Is(err, ErrEntityGone) {
			t.Errorf("HTTP %d: expected ErrEntityGone, got %v", status, err)
		}
	}
}

// With nothing cached there is nothing to stand in with, so the failure must still
// reach the caller rather than becoming an empty release — an empty one would read
// downstream as "track not in release", which looks like a correlation problem.
func TestGetMusicBrainzReleaseFailsWithoutStaleEntry(t *testing.T) {
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	if _, err := GetMusicBrainzRelease("rel-never-seen"); err == nil {
		t.Fatal("expected an error when there is no cached entry to fall back to")
	}
}

// The fallback has to land before the coalesced waiters are released, or it only
// rescues whichever goroutine happened to lead — and an album's tracks miss the
// cache together, which is the case that matters.
func TestGetMusicBrainzReleaseStaleFallbackReachesWaiters(t *testing.T) {
	var hits int32
	release := make(chan struct{})
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			select {
			case <-release:
			case <-time.After(10 * time.Second):
			}
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	seedExpiredRelease("rel-album", "Shared Album")
	MusicbrainzResetStats()

	var wg sync.WaitGroup
	results := make([]models.MusicBrainzReleaseResponse, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = GetMusicBrainzRelease("rel-album")
		}(i)
	}
	waitFor(t, func() bool { return MusicbrainzStatsSnapshot().Coalesced == 1 })
	close(release)
	wg.Wait()

	for i := range results {
		if errs[i] != nil {
			t.Errorf("caller %d: got error %v, want the stale release", i, errs[i])
		}
		if results[i].Title != "Shared Album" {
			t.Errorf("caller %d: served %q, want the cached title", i, results[i].Title)
		}
	}
}

// A stale serve must not be mistaken for a refresh: the entry stays expired so the
// next run re-checks it, and only a real response may extend its life.
func TestGetMusicBrainzReleaseStaleServeDoesNotRefreshEntry(t *testing.T) {
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	seedExpiredRelease("rel-still-stale", "Unchanged")

	if _, err := GetMusicBrainzRelease("rel-still-stale"); err != nil {
		t.Fatalf("expected the stale entry to stand in, got error: %v", err)
	}
	if _, fresh := cachedFreshRelease("rel-still-stale"); fresh {
		t.Error("the stale entry was marked fresh by being served; the next run would skip re-checking it")
	}
}

// TestTransientClassification pins which failures are worth retrying. The pair of
// sentinels is the whole point: a caller deciding what to persist about a file has to
// tell "MusicBrainz was down" from "this release is gone" from "this request is
// wrong", and only the first should be shrugged off and re-attempted.
func TestTransientClassification(t *testing.T) {
	cases := []struct {
		status    int
		transient bool
		gone      bool
	}{
		{http.StatusServiceUnavailable, true, false},
		{http.StatusTooManyRequests, true, false},
		{http.StatusInternalServerError, true, false},
		{http.StatusBadGateway, true, false},
		{http.StatusGatewayTimeout, true, false},
		{http.StatusNotFound, false, true},
		{http.StatusGone, false, true},
		// A 400 or a 401 is a request this client keeps getting wrong. Retrying it
		// forever would hide a misconfiguration, so it is neither.
		{http.StatusBadRequest, false, false},
		{http.StatusUnauthorized, false, false},
	}

	for _, c := range cases {
		withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
		})
		// No cached entry: the stale fallback must not stand in and mask the error
		// being classified here.
		_, err := GetMusicBrainzRelease("rel-classify")
		if err == nil {
			t.Fatalf("HTTP %d: expected an error", c.status)
		}
		if got := errors.Is(err, ErrTransient); got != c.transient {
			t.Errorf("HTTP %d: ErrTransient = %v, want %v (%v)", c.status, got, c.transient, err)
		}
		if got := errors.Is(err, ErrEntityGone); got != c.gone {
			t.Errorf("HTTP %d: ErrEntityGone = %v, want %v (%v)", c.status, got, c.gone, err)
		}
	}
}

// A transport failure never produced a status to classify, so it has to be marked
// transient on its own — and the cause must stay reachable underneath, or a handler
// loses the ability to ask what actually broke.
func TestTransientErrorKeepsItsCause(t *testing.T) {
	srv := withMockMB(t, func(w http.ResponseWriter, r *http.Request) {})
	srv.Close()

	_, err := GetMusicBrainzRelease("rel-no-socket")
	if err == nil {
		t.Fatal("expected an error with nothing listening")
	}
	if !errors.Is(err, ErrTransient) {
		t.Errorf("a transport failure is transient, got %v", err)
	}
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		t.Errorf("the underlying cause was dropped from the chain: %v", err)
	}
}

// The other MusicBrainz fetch paths classify the same way — they share the outage,
// so they must share the verdict.
func TestTransientClassificationAcrossFetchPaths(t *testing.T) {
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	if _, err := GetMusicBrainzArtist("artist-down"); !errors.Is(err, ErrTransient) {
		t.Errorf("artist lookup: want ErrTransient, got %v", err)
	}
	if _, _, err := GetMusicBrainzArtistReleaseGroups("artist-down"); !errors.Is(err, ErrTransient) {
		t.Errorf("artist release-groups: want ErrTransient, got %v", err)
	}
	if _, err := SearchMusicBrainzReleases(metadata.ReleaseSearchQuery{Text: "anything"}); !errors.Is(err, ErrTransient) {
		t.Errorf("release search: want ErrTransient, got %v", err)
	}
}

// TestSetMusicBrainzRateLimit covers the conversion from the data source's
// requests-per-second figure to the limiter's interval, and the refusal to accept a
// non-positive rate — a blank or zeroed config must not silently remove the throttle.
func TestSetMusicBrainzRateLimit(t *testing.T) {
	queryMutex.Lock()
	orig := rateLimit
	queryMutex.Unlock()
	t.Cleanup(func() {
		queryMutex.Lock()
		rateLimit = orig
		queryMutex.Unlock()
	})

	cases := []struct {
		perSecond float64
		want      time.Duration
	}{
		{1, time.Second},
		{4, 250 * time.Millisecond},
		{0.5, 2 * time.Second},
		{0, 2 * time.Second},  // ignored, previous value kept
		{-3, 2 * time.Second}, // ignored, previous value kept
	}
	for _, c := range cases {
		SetMusicBrainzRateLimit(c.perSecond)
		queryMutex.Lock()
		got := rateLimit
		queryMutex.Unlock()
		if got != c.want {
			t.Errorf("SetMusicBrainzRateLimit(%v): interval = %v, want %v", c.perSecond, got, c.want)
		}
	}
}

// waitFor polls cond until it holds, failing the test on timeout.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for condition")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestGetMusicBrainzReleaseHTTPError(t *testing.T) {
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	_, err := GetMusicBrainzRelease("rel-3")
	if err == nil {
		t.Fatal("expected error on HTTP 500, got nil")
	}
	// the real status must survive to the caller, not be replaced by a generic message
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error %q should mention the HTTP status", err.Error())
	}
}

// TestGetMusicBrainzReleaseNotFound covers the dead-MB-ID case: a 404 must be
// distinguishable from a transient failure by the *type* of the error, not by
// matching words in its message, so callers can act on it. See ErrEntityGone.
func TestGetMusicBrainzReleaseNotFound(t *testing.T) {
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Not Found"}`))
	})

	_, err := GetMusicBrainzRelease("gone-id")
	if err == nil {
		t.Fatal("expected error on HTTP 404, got nil")
	}
	if !errors.Is(err, ErrEntityGone) {
		t.Errorf("404 error %v should unwrap to ErrEntityGone", err)
	}
	if !strings.Contains(err.Error(), "gone-id") {
		t.Errorf("404 error %q should name the release ID", err.Error())
	}
}

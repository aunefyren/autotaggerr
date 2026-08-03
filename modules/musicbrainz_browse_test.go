package modules

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/aunefyren/autotaggerr/metadata"
	"github.com/aunefyren/autotaggerr/models"
)

// The browse and search calls against MusicBrainz, exercised through the local stub
// (withMockMB). Two things are worth asserting beyond "it parses": the *request*
// (MusicBrainz browse omits formats and track counts without inc=media, which is
// exactly what tells two editions apart) and the caching, which is what makes these
// affordable behind a 1 req/s limiter.

// resetBrowseCaches clears the process-global caches these functions share, so one
// test's stub response cannot answer another test's call.
func resetBrowseCaches(t *testing.T) {
	t.Helper()
	resetEntityCache(t)
}

func TestSearchMusicBrainzArtists(t *testing.T) {
	resetBrowseCaches(t)
	var gotQuery string
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"count":1,"artists":[
			{"id":"a1","score":100,"name":"Kate Bush","sort-name":"Bush, Kate","type":"Person","country":"GB"}
		]}`)
	})

	artists, err := SearchMusicBrainzArtists("  Kate Bush  ")
	if err != nil {
		t.Fatalf("SearchMusicBrainzArtists: %v", err)
	}
	if len(artists) != 1 || artists[0].Name != "Kate Bush" {
		t.Fatalf("artists = %+v", artists)
	}
	// Trimmed before it becomes a query, so trailing whitespace does not change the
	// result set.
	if gotQuery != "Kate Bush" {
		t.Errorf("query = %q, want %q", gotQuery, "Kate Bush")
	}
}

func TestSearchMusicBrainzArtistsBlankQuery(t *testing.T) {
	resetBrowseCaches(t)
	withMockMB(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a blank query reached MusicBrainz")
	})
	artists, err := SearchMusicBrainzArtists("   ")
	if err != nil || artists != nil {
		t.Errorf("blank query = (%v, %v), want (nil, nil)", artists, err)
	}
}

func TestSearchMusicBrainzArtistsPropagatesFailure(t *testing.T) {
	resetBrowseCaches(t)
	withMockMB(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	})
	if _, err := SearchMusicBrainzArtists("Kate Bush"); err == nil {
		t.Error("an HTTP 503 was reported as success")
	}
}

func TestGetReleaseGroupReleasesRequestsMedia(t *testing.T) {
	resetBrowseCaches(t)
	var gotURL *url.URL
	calls := 0
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		gotURL = r.URL
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"release-count":2,"releases":[
			{"id":"rel-1","title":"Hounds of Love","date":"1985-09-16","country":"GB",
			 "media":[{"format":"12\" Vinyl","track-count":12}]},
			{"id":"rel-2","title":"Hounds of Love","date":"1997-01-01","country":"GB",
			 "media":[{"format":"CD","track-count":24}]}
		]}`)
	})

	releases, err := GetMusicBrainzReleaseGroupReleases("rg-1")
	if err != nil {
		t.Fatalf("GetMusicBrainzReleaseGroupReleases: %v", err)
	}
	if len(releases) != 2 {
		t.Fatalf("releases = %d, want 2", len(releases))
	}
	// Without inc=media the browse endpoint returns no format and no track count,
	// and an edition list where every row reads the same is useless.
	if inc := gotURL.Query().Get("inc"); !strings.Contains(inc, "media") {
		t.Errorf("inc = %q, want it to include media", inc)
	}
	if releases[1].Media[0].TrackCount != 24 {
		t.Errorf("second edition track count = %d, want 24", releases[1].Media[0].TrackCount)
	}

	// Cached: the release-group page reads this on every open, and each call costs a
	// rate-limited second.
	if _, err := GetMusicBrainzReleaseGroupReleases("rg-1"); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if calls != 1 {
		t.Errorf("upstream calls = %d, want 1 — the edition cache did not hold", calls)
	}
}

func TestGetReleaseGroupReleasesServesStaleWhenMBIsDown(t *testing.T) {
	resetBrowseCaches(t)
	fail := false
	withMockMB(t, func(w http.ResponseWriter, _ *http.Request) {
		if fail {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"release-count":1,"releases":[{"id":"rel-1","title":"Spiderland"}]}`)
	})

	if _, err := GetMusicBrainzReleaseGroupReleases("rg-1"); err != nil {
		t.Fatalf("warm the cache: %v", err)
	}
	// Expire the entry so the next call must go upstream, and make upstream fail.
	expireEntityCache(t, models.MBEntityEditions, "rg-1")
	fail = true

	releases, err := GetMusicBrainzReleaseGroupReleases("rg-1")
	if err != nil {
		t.Fatalf("a stale list beats an error: %v", err)
	}
	if len(releases) != 1 || releases[0].ID != "rel-1" {
		t.Errorf("releases = %+v, want the stale entry", releases)
	}
}

func TestGetReleaseGroupReleasesBlankID(t *testing.T) {
	resetBrowseCaches(t)
	withMockMB(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a blank release-group id reached MusicBrainz")
	})
	if releases, err := GetMusicBrainzReleaseGroupReleases("  "); err != nil || releases != nil {
		t.Errorf("blank id = (%v, %v), want (nil, nil)", releases, err)
	}
}

func TestGetMusicBrainzArtist(t *testing.T) {
	resetBrowseCaches(t)
	var gotPath, gotInc string
	calls := 0
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		gotPath, gotInc = r.URL.Path, r.URL.Query().Get("inc")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"a1","name":"Kate Bush","sort-name":"Bush, Kate","type":"Person",
			"country":"GB","area":{"name":"United Kingdom"},"begin-area":{"name":"Bexleyheath"},
			"life-span":{"begin":"1958-07-30","ended":false},
			"genres":[{"name":"pop","count":3},{"name":"art pop","count":9}]}`)
	})

	artist, err := GetMusicBrainzArtist("a1")
	if err != nil {
		t.Fatalf("GetMusicBrainzArtist: %v", err)
	}
	if artist.Name != "Kate Bush" || artist.Type != "Person" || artist.Country != "GB" {
		t.Errorf("artist = %+v", artist)
	}
	if artist.BeginArea.Name != "Bexleyheath" {
		t.Errorf("begin area = %q, want Bexleyheath", artist.BeginArea.Name)
	}
	if artist.LifeSpan.Begin != "1958-07-30" || artist.LifeSpan.Ended {
		t.Errorf("life span = %+v", artist.LifeSpan)
	}
	if gotPath != "/artist/a1" {
		t.Errorf("path = %q, want /artist/a1", gotPath)
	}
	// Genres are the point of the lookup for the header; without inc they are absent.
	if !strings.Contains(gotInc, "genres") {
		t.Errorf("inc = %q, want it to include genres", gotInc)
	}
	if len(artist.Genres) != 2 {
		t.Errorf("genres = %+v, want 2", artist.Genres)
	}

	// Cached for a day: this is a fact about a person, read on every page open.
	if _, err := GetMusicBrainzArtist("a1"); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if calls != 1 {
		t.Errorf("upstream calls = %d, want 1 — the artist cache did not hold", calls)
	}
}

func TestGetMusicBrainzArtistServesStaleWhenMBIsDown(t *testing.T) {
	resetBrowseCaches(t)
	fail := false
	withMockMB(t, func(w http.ResponseWriter, _ *http.Request) {
		if fail {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"a1","name":"Talk Talk","type":"Group"}`)
	})

	if _, err := GetMusicBrainzArtist("a1"); err != nil {
		t.Fatalf("warm the cache: %v", err)
	}
	expireEntityCache(t, models.MBEntityArtist, "a1")
	fail = true

	artist, err := GetMusicBrainzArtist("a1")
	if err != nil {
		t.Fatalf("a stale artist beats an error: %v", err)
	}
	if artist.Name != "Talk Talk" {
		t.Errorf("artist = %+v, want the stale entry", artist)
	}
}

func TestGetMusicBrainzArtistFailsWithoutACachedCopy(t *testing.T) {
	resetBrowseCaches(t)
	withMockMB(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	// Nothing cached, so there is nothing to fall back to: the caller must learn the
	// header data is unavailable rather than get a zero-valued artist.
	if _, err := GetMusicBrainzArtist("a1"); err == nil {
		t.Error("a 404 with a cold cache was reported as success")
	}
}

func TestGetArtistDiscographyCachesAndPages(t *testing.T) {
	resetBrowseCaches(t)
	calls := 0
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		offset := r.URL.Query().Get("offset")
		w.Header().Set("Content-Type", "application/json")
		// Two pages: the first is full (100), so the client must ask for the second.
		if offset == "0" {
			fmt.Fprintf(w, `{"release-group-count":%d,"release-group-offset":0,"release-groups":[%s]}`,
				artistReleaseGroupPageSize+1, releaseGroupJSON(artistReleaseGroupPageSize, 0))
			return
		}
		fmt.Fprintf(w, `{"release-group-count":%d,"release-group-offset":%d,"release-groups":[%s]}`,
			artistReleaseGroupPageSize+1, artistReleaseGroupPageSize, releaseGroupJSON(1, artistReleaseGroupPageSize))
	})

	groups, err := GetArtistDiscography("a1")
	if err != nil {
		t.Fatalf("GetArtistDiscography: %v", err)
	}
	if len(groups) != artistReleaseGroupPageSize+1 {
		t.Fatalf("groups = %d, want %d — paging stopped early", len(groups), artistReleaseGroupPageSize+1)
	}
	if calls != 2 {
		t.Errorf("upstream calls = %d, want 2 (one page each)", calls)
	}

	// Browsing an artist pages through up to five rate-limited requests, so
	// re-opening the same artist must not repeat them.
	if _, err := GetArtistDiscography("a1"); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if calls != 2 {
		t.Errorf("upstream calls after a cached read = %d, want 2", calls)
	}
}

func TestGetArtistDiscographyServesStaleWhenMBIsDown(t *testing.T) {
	resetBrowseCaches(t)
	fail := false
	withMockMB(t, func(w http.ResponseWriter, _ *http.Request) {
		if fail {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"release-group-count":1,"release-groups":[{"id":"rg-1","title":"Spirit of Eden","primary-type":"Album"}]}`)
	})

	if _, err := GetArtistDiscography("a1"); err != nil {
		t.Fatalf("warm the cache: %v", err)
	}
	expireEntityCache(t, models.MBEntityDiscography, "a1")
	fail = true

	groups, err := GetArtistDiscography("a1")
	if err != nil {
		t.Fatalf("a stale discography beats an empty page: %v", err)
	}
	if len(groups) != 1 || groups[0].Title != "Spirit of Eden" {
		t.Errorf("groups = %+v, want the stale entry", groups)
	}

	// With nothing cached at all, the failure has to surface.
	resetBrowseCaches(t)
	if _, err := GetArtistDiscography("a2"); err == nil {
		t.Error("a cold-cache failure was reported as success")
	}
}

// TestGetArtistDiscographyIsConcurrencySafe: the artist page and a follow sync can
// ask at the same time. Run with -race, this is the test that would catch an
// unguarded map.
func TestGetArtistDiscographyIsConcurrencySafe(t *testing.T) {
	resetBrowseCaches(t)
	withMockMB(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"release-group-count":1,"release-groups":[{"id":"rg-1","title":"Laughing Stock"}]}`)
	})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := GetArtistDiscography("a1"); err != nil {
				t.Errorf("concurrent read: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestSearchMusicBrainzReleases(t *testing.T) {
	resetBrowseCaches(t)
	var gotQuery string
	var gotLimit string
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("query")
		gotLimit = r.URL.Query().Get("limit")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"count":1,"releases":[{"id":"rel-1","title":"Spiderland","score":100}]}`)
	})

	page, err := SearchMusicBrainzReleases(metadata.ReleaseSearchQuery{Artist: "Slint", Release: "Spiderland", Limit: 5})
	if err != nil {
		t.Fatalf("SearchMusicBrainzReleases: %v", err)
	}
	if page.Count != 1 || len(page.Releases) != 1 {
		t.Fatalf("page = %+v", page)
	}
	// The fielded search is one Lucene query, not a sequence of requests.
	if !strings.Contains(gotQuery, "Slint") || !strings.Contains(gotQuery, "Spiderland") {
		t.Errorf("query = %q, want both fields in it", gotQuery)
	}
	if gotLimit != "5" {
		t.Errorf("limit = %q, want 5", gotLimit)
	}
}

func TestSearchMusicBrainzReleasesClampsPaging(t *testing.T) {
	resetBrowseCaches(t)
	var gotLimit, gotOffset string
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		gotLimit, gotOffset = r.URL.Query().Get("limit"), r.URL.Query().Get("offset")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"count":0,"releases":[]}`)
	})

	// An unbounded limit is a request for MusicBrainz to send everything; a negative
	// offset is not a page at all.
	if _, err := SearchMusicBrainzReleases(metadata.ReleaseSearchQuery{Artist: "Slint", Limit: 9999, Offset: -10}); err != nil {
		t.Fatalf("SearchMusicBrainzReleases: %v", err)
	}
	if gotLimit != fmt.Sprint(maxReleaseSearchLimit) {
		t.Errorf("limit = %q, want it clamped to %d", gotLimit, maxReleaseSearchLimit)
	}
	if gotOffset != "0" {
		t.Errorf("offset = %q, want 0", gotOffset)
	}
}

func TestSearchMusicBrainzReleasesEmptyQuery(t *testing.T) {
	resetBrowseCaches(t)
	withMockMB(t, func(http.ResponseWriter, *http.Request) {
		t.Error("an empty query reached MusicBrainz")
	})
	page, err := SearchMusicBrainzReleases(metadata.ReleaseSearchQuery{})
	if err != nil || page.Count != 0 || page.Releases != nil {
		t.Errorf("empty query = (%+v, %v), want a zero page", page, err)
	}
}

func TestSearchMusicBrainzReleasesReportsHTTPError(t *testing.T) {
	resetBrowseCaches(t)
	withMockMB(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusServiceUnavailable)
	})
	if _, err := SearchMusicBrainzReleases(metadata.ReleaseSearchQuery{Artist: "Slint"}); err == nil {
		t.Error("an HTTP 503 was reported as success")
	}
}

// TestSearchResultFromRelease: a release reached by pasting its MBID must render in
// the same list as a searched one, so the projection has to fill the fields the
// search hit carries.
func TestSearchResultFromRelease(t *testing.T) {
	hit := SearchResultFromRelease(models.MusicBrainzReleaseResponse{
		ID: "rel-1", Title: "Hounds of Love", Status: "Official",
		Date: "1985-09-16", Country: "GB", Disambiguation: "original",
		Media: []models.MusicBrainzMedia{
			{Format: `12" Vinyl`, TrackCount: 2, Tracks: []models.Track{{ID: "t1"}, {ID: "t2"}}},
		},
	})

	if hit.ID != "rel-1" || hit.Title != "Hounds of Love" || hit.Country != "GB" {
		t.Errorf("hit = %+v", hit)
	}
	if len(hit.Media) != 1 {
		t.Fatalf("media = %+v, want one entry", hit.Media)
	}
	// The track count is what a person uses to tell a standard pressing from a
	// deluxe one, and the full release states it as a list rather than a number.
	if hit.Media[0].TrackCount != 2 || hit.Media[0].Format != `12" Vinyl` {
		t.Errorf("media[0] = %+v, want the format and a count of 2", hit.Media[0])
	}
}

// releaseGroupJSON builds n release-group objects, numbered from start.
func releaseGroupJSON(n, start int) string {
	parts := make([]string, 0, n)
	for i := 0; i < n; i++ {
		parts = append(parts, fmt.Sprintf(`{"id":"rg-%d","title":"Album %d","primary-type":"Album"}`, start+i, start+i))
	}
	return strings.Join(parts, ",")
}

package modules

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// jpegBytes is the smallest thing that sniffs as a JPEG. The artwork path only
// inspects the magic number, never decodes.
var jpegBytes = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F'}

const testMBID = "019fa765-1234-7890-abcd-c389e527ed21"

// artworkTestServer stands in for the Cover Art Archive, counting requests so the
// caching claims can be checked rather than assumed.
func artworkTestServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *int64) {
	t.Helper()
	var calls int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func TestGetArtworkRejectsNonMBID(t *testing.T) {
	// The id becomes a cache file name, so a path traversal must be refused before
	// it is ever joined onto a path.
	for _, id := range []string{"", "../../etc/passwd", "not-a-uuid"} {
		_, err := GetArtwork(ArtworkProviders{CoverArtEnabled: true}, ArtworkEntityReleaseGroup, id, ArtworkKindFront, 250)
		// Specifically a bad *request*: no provider was contacted, so it must not
		// surface as a provider failure.
		if !errors.Is(err, ErrBadArtworkRequest) {
			t.Errorf("GetArtwork(%q) err = %v; want ErrBadArtworkRequest", id, err)
		}
	}
}

func TestGetArtworkRejectsImpossibleKinds(t *testing.T) {
	// MusicBrainz has no artist cover art and the Cover Art Archive has no artist
	// entity, so this combination is a coding mistake, not a missing image.
	if _, err := GetArtwork(ArtworkProviders{CoverArtEnabled: true}, ArtworkEntityArtist, testMBID, ArtworkKindFront, 250); !errors.Is(err, ErrBadArtworkRequest) {
		t.Errorf("artist front cover err = %v; want ErrBadArtworkRequest", err)
	}
	if _, err := GetArtwork(ArtworkProviders{CoverArtEnabled: true}, ArtworkEntityRelease, testMBID, ArtworkKindThumb, 250); !errors.Is(err, ErrBadArtworkRequest) {
		t.Errorf("release thumb err = %v; want ErrBadArtworkRequest", err)
	}
}

func TestGetArtworkUnconfiguredProviderIsNotAnError(t *testing.T) {
	t.Chdir(t.TempDir())
	ResetArtworkNegativeCache()

	// No enabled provider means "no image", which the UI renders as a monogram —
	// it must not read as a broken provider.
	if _, err := GetArtwork(ArtworkProviders{}, ArtworkEntityReleaseGroup, testMBID, ArtworkKindFront, 250); err != ErrNoArtwork {
		t.Errorf("err = %v; want ErrNoArtwork", err)
	}
	// Likewise fanart.tv enabled but keyless: opt-in, not misconfigured.
	if _, err := GetArtwork(ArtworkProviders{FanartEnabled: true}, ArtworkEntityArtist, testMBID, ArtworkKindThumb, 250); err != ErrNoArtwork {
		t.Errorf("keyless fanart err = %v; want ErrNoArtwork", err)
	}
}

func TestGetArtworkCachesToDisk(t *testing.T) {
	t.Chdir(t.TempDir())
	ResetArtworkNegativeCache()

	server, calls := artworkTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		want := fmt.Sprintf("/release-group/%s/front-250", testMBID)
		if r.URL.Path != want {
			t.Errorf("requested %q; want %q", r.URL.Path, want)
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpegBytes)
	})

	providers := ArtworkProviders{CoverArtEnabled: true, CoverArtBaseURL: server.URL}

	first, err := GetArtwork(providers, ArtworkEntityReleaseGroup, testMBID, ArtworkKindFront, 250)
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if first.FromCache {
		t.Error("the first fetch reported a cache hit")
	}
	if first.ContentType != "image/jpeg" {
		t.Errorf("content type = %q; want image/jpeg", first.ContentType)
	}

	second, err := GetArtwork(providers, ArtworkEntityReleaseGroup, testMBID, ArtworkKindFront, 250)
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if !second.FromCache {
		t.Error("the second fetch missed the disk cache")
	}
	if got := atomic.LoadInt64(calls); got != 1 {
		t.Errorf("upstream calls = %d; want 1 — the disk cache did not hold", got)
	}
}

func TestGetArtworkRemembersMissingArt(t *testing.T) {
	t.Chdir(t.TempDir())
	ResetArtworkNegativeCache()

	server, calls := artworkTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		// The Cover Art Archive's documented answer for "no art", and the common
		// case for obscure releases.
		w.WriteHeader(http.StatusNotFound)
	})

	providers := ArtworkProviders{CoverArtEnabled: true, CoverArtBaseURL: server.URL}
	for i := 0; i < 3; i++ {
		if _, err := GetArtwork(providers, ArtworkEntityReleaseGroup, testMBID, ArtworkKindFront, 250); err != ErrNoArtwork {
			t.Fatalf("fetch %d: err = %v; want ErrNoArtwork", i, err)
		}
	}
	// Without the negative cache this is one request per row per page paint, which
	// is the whole reason it exists.
	if got := atomic.LoadInt64(calls); got != 1 {
		t.Errorf("upstream calls = %d; want 1 — a missing cover was re-fetched", got)
	}
}

func TestGetArtworkSingleFlights(t *testing.T) {
	t.Chdir(t.TempDir())
	ResetArtworkNegativeCache()

	release := make(chan struct{})
	server, calls := artworkTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		<-release // hold the first request open so the others pile up behind it
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpegBytes)
	})

	providers := ArtworkProviders{CoverArtEnabled: true, CoverArtBaseURL: server.URL}

	const racers = 8
	var wg sync.WaitGroup
	errs := make([]error, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = GetArtwork(providers, ArtworkEntityReleaseGroup, testMBID, ArtworkKindFront, 500)
		}(i)
	}
	close(release)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("racer %d: %v", i, err)
		}
	}
	// A table renders every row at once; without single-flight a cold cache turns
	// one cover into one upstream request per row.
	if got := atomic.LoadInt64(calls); got != 1 {
		t.Errorf("upstream calls = %d; want 1 — the requests were not single-flighted", got)
	}
}

func TestGetArtworkFanartResolvesThumb(t *testing.T) {
	t.Chdir(t.TempDir())
	ResetArtworkNegativeCache()

	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpegBytes)
	}))
	t.Cleanup(imageServer.Close)

	indexServer, _ := artworkTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("api_key"); got != "test-key" {
			t.Errorf("api_key = %q; want test-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"name":"Kate Bush","artistthumb":[{"id":"1","url":%q,"likes":"9"}]}`, imageServer.URL+"/thumb.jpg")
	})

	providers := ArtworkProviders{FanartEnabled: true, FanartBaseURL: indexServer.URL, FanartAPIKey: "test-key"}
	art, err := GetArtwork(providers, ArtworkEntityArtist, testMBID, ArtworkKindThumb, 500)
	if err != nil {
		t.Fatalf("fanart thumb: %v", err)
	}
	if art.ContentType != "image/jpeg" {
		t.Errorf("content type = %q; want image/jpeg", art.ContentType)
	}

	// The same artist has no backdrop in that response, which is a missing image
	// rather than a failure — and must not be confused with the thumb it does have.
	if _, err := GetArtwork(providers, ArtworkEntityArtist, testMBID, ArtworkKindBackground, 500); err != ErrNoArtwork {
		t.Errorf("background err = %v; want ErrNoArtwork", err)
	}
}

func TestGetArtworkRejectsNonImageBody(t *testing.T) {
	t.Chdir(t.TempDir())
	ResetArtworkNegativeCache()

	server, _ := artworkTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		// A captive portal or an error page dressed as a 200. Caching this would
		// serve HTML as a cover forever.
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>nope</html>"))
	})

	providers := ArtworkProviders{CoverArtEnabled: true, CoverArtBaseURL: server.URL}
	if _, err := GetArtwork(providers, ArtworkEntityReleaseGroup, testMBID, ArtworkKindFront, 250); err != ErrNoArtwork {
		t.Errorf("err = %v; want ErrNoArtwork", err)
	}
}

func TestNearestCoverArtSize(t *testing.T) {
	// The archive serves 250/500/1200 only; anything else must round up to one it
	// actually has rather than 404.
	cases := map[int]int{1: 250, 250: 250, 251: 500, 500: 500, 900: 1200, 4000: 1200}
	for in, want := range cases {
		if got := nearestCoverArtSize(in); got != want {
			t.Errorf("nearestCoverArtSize(%d) = %d; want %d", in, got, want)
		}
	}
}

func TestArtworkCacheKeySeparatesSizes(t *testing.T) {
	// A thumbnail and a hero image of the same album must not evict each other.
	thumb := artworkCacheKey(ArtworkEntityReleaseGroup, testMBID, ArtworkKindFront, 250)
	hero := artworkCacheKey(ArtworkEntityReleaseGroup, testMBID, ArtworkKindFront, 500)
	if thumb == hero {
		t.Errorf("both sizes share the cache key %q", thumb)
	}
}

// TestSniffImageType: fanart.tv serves a mix of formats behind extensionless URLs
// and CDNs mislabel Content-Type, so the bytes are the only reliable answer. The
// negative cases matter most — an error page cached as a cover would be served
// forever.
func TestSniffImageType(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"jpeg", jpegBytes, "image/jpeg"},
		{"png", []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00}, "image/png"},
		{"webp", []byte("RIFF____WEBPVP8 "), "image/webp"},
		{"gif", []byte("GIF89a__________"), "image/gif"},
		{"html error page", []byte("<html>nope</html>"), ""},
		{"empty", nil, ""},
		{"too short to identify", []byte{0xFF}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sniffImageType(tc.data); got != tc.want {
				t.Errorf("sniffImageType = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestOrDefault: an empty base URL on a data source row means "use the provider's
// real endpoint", not "request nothing".
func TestOrDefault(t *testing.T) {
	if got := orDefault("", "fallback"); got != "fallback" {
		t.Errorf("orDefault(empty) = %q, want fallback", got)
	}
	if got := orDefault("   ", "fallback"); got != "fallback" {
		t.Errorf("orDefault(blank) = %q, want fallback", got)
	}
	if got := orDefault("https://configured", "fallback"); got != "https://configured" {
		t.Errorf("orDefault = %q, want the configured value", got)
	}
}

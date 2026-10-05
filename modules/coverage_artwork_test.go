package modules

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// Provider failure modes and cache bookkeeping the happy-path artwork tests do not
// reach. Every provider is an httptest server; nothing here leaves the machine.

// resetArtworkThrottle forgets the per-host gate so tests do not pay the 500ms
// spacing between each other's requests.
func resetArtworkThrottle(t *testing.T) {
	t.Helper()
	reset := func() {
		artworkThrottleMu.Lock()
		artworkLastCall = map[string]time.Time{}
		artworkThrottleMu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

// closedCacheDB installs a database and then closes it, so every write-through
// fails the way a dropped connection would.
func closedCacheDB(t *testing.T) {
	t.Helper()
	db := withMigrationDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	_ = sqlDB.Close()
}

func TestArtworkExpireMakesAnEntryStale(t *testing.T) {
	t.Chdir(t.TempDir())
	resetMap()
	t.Cleanup(resetMap)

	// Unknown key: nothing to expire, and nothing gets created.
	ArtworkExpire(ArtworkEntityRelease, testMBID, ArtworkKindFront, 250)
	if _, known := artworkMetaFor(artworkCacheKey(ArtworkEntityRelease, testMBID, ArtworkKindFront, 250)); known {
		t.Fatal("expiring an unknown key created an entry")
	}

	key := artworkCacheKey(ArtworkEntityRelease, testMBID, ArtworkKindFront, 250)
	writeArtworkCache(key, Artwork{Data: jpegBytes, ContentType: "image/jpeg"})
	if !ArtworkFresh(ArtworkEntityRelease, testMBID, ArtworkKindFront, 250) {
		t.Fatal("a just-written entry is not fresh")
	}

	ArtworkExpire(ArtworkEntityRelease, testMBID, ArtworkKindFront, 240) // rounds to the same key
	if ArtworkFresh(ArtworkEntityRelease, testMBID, ArtworkKindFront, 250) {
		t.Error("an expired entry still reads as fresh")
	}
	// Expired, not deleted: the bytes stay as the outage fallback.
	if _, ok := readExpiredArtwork(key); !ok {
		t.Error("expiring removed the file behind the entry")
	}
}

func TestArtworkCacheCountsSeparatesImagesFromMisses(t *testing.T) {
	resetMap()
	t.Cleanup(resetMap)

	artworkIndexMu.Lock()
	artworkIndex["a"] = artworkMeta{contentType: "image/jpeg"}
	artworkIndex["b"] = artworkMeta{contentType: "image/png"}
	artworkIndex["c"] = artworkMeta{missing: true}
	artworkIndexMu.Unlock()

	images, missing := ArtworkCacheCounts()
	if images != 2 || missing != 1 {
		t.Errorf("counts = (%d images, %d missing), want (2, 1)", images, missing)
	}
}

func TestFetchArtworkUnknownEntity(t *testing.T) {
	if _, err := fetchArtwork(ArtworkProviders{CoverArtEnabled: true, FanartEnabled: true, FanartAPIKey: "k"},
		"label", testMBID, ArtworkKindFront, 250); !errors.Is(err, ErrNoArtwork) {
		t.Errorf("err = %v, want ErrNoArtwork for an entity no provider holds", err)
	}
}

func TestFanartProviderFailures(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		want    string
	}{
		{"rejected key", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}, "rejected the API key"},
		{"server error", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "upstream on fire", http.StatusBadGateway)
		}, "HTTP 502: upstream on fire"},
		{"bad json", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("<html>"))
		}, "could not parse the fanart.tv response"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetArtworkThrottle(t)
			server, _ := artworkTestServer(t, tc.handler)
			_, err := fetchFanartImage(ArtworkProviders{FanartBaseURL: server.URL, FanartAPIKey: " k "}, testMBID, ArtworkKindThumb)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}

	t.Run("transport", func(t *testing.T) {
		resetArtworkThrottle(t)
		dead := httptest.NewServer(http.NotFoundHandler())
		dead.Close()
		_, err := fetchFanartImage(ArtworkProviders{FanartBaseURL: dead.URL, FanartAPIKey: "k"}, testMBID, ArtworkKindThumb)
		if err == nil || !strings.Contains(err.Error(), "fanart.tv request failed") {
			t.Errorf("err = %v", err)
		}
	})
}

// The backdrop comes from its own list; a thumb-only artist has no backdrop.
func TestFanartBackgroundUsesItsOwnList(t *testing.T) {
	resetArtworkThrottle(t)
	var server *httptest.Server
	server, _ = artworkTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/music/"):
			if r.URL.Query().Get("api_key") != "k" {
				t.Errorf("api_key = %q, want it trimmed", r.URL.Query().Get("api_key"))
			}
			fmt.Fprintf(w, `{"artistthumb":[{"url":"%s/thumb.jpg"}],"artistbackground":[{"url":"%s/bg.jpg"}]}`, server.URL, server.URL)
		case r.URL.Path == "/bg.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(jpegBytes)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	})

	art, err := fetchFanartImage(ArtworkProviders{FanartBaseURL: server.URL, FanartAPIKey: " k"}, testMBID, ArtworkKindBackground)
	if err != nil {
		t.Fatalf("fetchFanartImage: %v", err)
	}
	if art.ContentType != "image/jpeg" || len(art.Data) != len(jpegBytes) {
		t.Errorf("art = %+v", art)
	}
}

func TestDownloadImageResponses(t *testing.T) {
	pngBytes := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0, 0}
	cases := []struct {
		name     string
		handler  http.HandlerFunc
		wantErr  string // substring; "" means success
		wantNone bool   // ErrNoArtwork
		wantType string
	}{
		{"server error", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}, "HTTP 500", false, ""},
		{"gone", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusGone)
		}, "", true, ""},
		{"empty body", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/jpeg")
		}, "", true, ""},
		// A CDN that mislabels the type: the bytes decide.
		{"mislabelled png", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(pngBytes)
		}, "", false, "image/png"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetArtworkThrottle(t)
			var gotReferer, gotUA string
			server, _ := artworkTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				gotReferer, gotUA = r.Header.Get("Referer"), r.Header.Get("User-Agent")
				tc.handler(w, r)
			})
			art, err := downloadImage("test", server.URL+"/img", "https://ref.example/")
			switch {
			case tc.wantNone:
				if !errors.Is(err, ErrNoArtwork) {
					t.Errorf("err = %v, want ErrNoArtwork", err)
				}
			case tc.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("err = %v, want %q", err, tc.wantErr)
				}
			default:
				if err != nil || art.ContentType != tc.wantType {
					t.Errorf("art = %+v, err = %v; want type %s", art, err, tc.wantType)
				}
			}
			if gotReferer != "https://ref.example/" || !strings.HasPrefix(gotUA, "Autotaggerr/") {
				t.Errorf("headers: referer %q, user agent %q", gotReferer, gotUA)
			}
		})
	}

	t.Run("bad url", func(t *testing.T) {
		resetArtworkThrottle(t)
		if _, err := downloadImage("test", "http://bad host/x", ""); err == nil {
			t.Error("an unparseable URL downloaded")
		}
	})
	t.Run("transport", func(t *testing.T) {
		resetArtworkThrottle(t)
		dead := httptest.NewServer(http.NotFoundHandler())
		dead.Close()
		if _, err := downloadImage("test", dead.URL, ""); err == nil || !strings.Contains(err.Error(), "artwork request failed") {
			t.Errorf("err = %v", err)
		}
	})
}

// A cache file that is not an image is a miss, both for the fresh read and for the
// outage fallback — serving it would put garbage in an <img>.
func TestArtworkCacheIgnoresNonImageFiles(t *testing.T) {
	t.Chdir(t.TempDir())
	resetMap()
	t.Cleanup(resetMap)

	key := artworkCacheKey(ArtworkEntityRelease, testMBID, ArtworkKindFront, 250)
	if err := os.MkdirAll(artworkCacheDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artworkCachePath(key), []byte("this is not an image"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, ok := readArtworkCache(key); ok {
		t.Error("readArtworkCache served a non-image file")
	}
	if _, ok := readExpiredArtwork(key); ok {
		t.Error("readExpiredArtwork served a non-image file")
	}
	if _, ok := readExpiredArtwork(artworkCacheKey(ArtworkEntityRelease, testMBID, ArtworkKindFront, 500)); ok {
		t.Error("readExpiredArtwork served a key with no file")
	}
}

func TestWriteArtworkCacheFailuresLeaveNoEntry(t *testing.T) {
	art := Artwork{Data: jpegBytes, ContentType: "image/jpeg"}

	t.Run("directory blocked", func(t *testing.T) {
		t.Chdir(t.TempDir())
		resetMap()
		t.Cleanup(resetMap)
		// "config" as a file means config/artwork cannot be created.
		if err := os.WriteFile("config", nil, 0o644); err != nil {
			t.Fatal(err)
		}
		key := artworkCacheKey(ArtworkEntityRelease, testMBID, ArtworkKindFront, 250)
		writeArtworkCache(key, art)
		if _, known := artworkMetaFor(key); known {
			t.Error("a failed write still recorded an index entry")
		}
	})

	t.Run("temp file blocked", func(t *testing.T) {
		t.Chdir(t.TempDir())
		resetMap()
		t.Cleanup(resetMap)
		key := artworkCacheKey(ArtworkEntityRelease, testMBID, ArtworkKindFront, 250)
		// A directory where the temp file would go makes WriteFile fail.
		if err := os.MkdirAll(artworkCachePath(key)+".tmp", 0o755); err != nil {
			t.Fatal(err)
		}
		writeArtworkCache(key, art)
		if _, known := artworkMetaFor(key); known {
			t.Error("a failed temp write still recorded an index entry")
		}
	})

	t.Run("rename blocked", func(t *testing.T) {
		t.Chdir(t.TempDir())
		resetMap()
		t.Cleanup(resetMap)
		key := artworkCacheKey(ArtworkEntityRelease, testMBID, ArtworkKindFront, 250)
		// A non-empty directory at the final name makes the rename fail.
		if err := os.MkdirAll(filepath.Join(artworkCachePath(key), "occupied"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeArtworkCache(key, art)
		if _, known := artworkMetaFor(key); known {
			t.Error("a failed rename still recorded an index entry")
		}
		if _, err := os.Stat(artworkCachePath(key) + ".tmp"); !os.IsNotExist(err) {
			t.Errorf("the temp file was left behind: %v", err)
		}
	})
}

// The negative half is capped, and hitting the cap drops it wholesale — in memory
// and in the database — while leaving every positive entry alone.
func TestPruneArtworkNegativesAtTheCap(t *testing.T) {
	t.Chdir(t.TempDir())
	dbForCache(t)

	positive := artworkCacheKey(ArtworkEntityRelease, testMBID, ArtworkKindFront, 250)
	writeArtworkCache(positive, Artwork{Data: jpegBytes, ContentType: "image/jpeg"})
	if err := cacheDB.Create(&models.ArtworkCacheEntry{Key: "persisted-negative", Missing: true, ExpiresAt: time.Now().Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}

	artworkIndexMu.Lock()
	for i := 0; i < artworkNegativeMax; i++ {
		artworkIndex[fmt.Sprintf("neg-%d", i)] = artworkMeta{missing: true, expiresAt: time.Now().Add(time.Hour)}
	}
	artworkIndexMu.Unlock()

	fresh := artworkCacheKey(ArtworkEntityRelease, testMBID, ArtworkKindFront, 500)
	rememberNoArtwork(fresh)

	images, missing := ArtworkCacheCounts()
	if images != 1 || missing != 1 {
		t.Errorf("after prune: %d images, %d missing; want 1 and only the new negative", images, missing)
	}
	var persisted int64
	cacheDB.Model(&models.ArtworkCacheEntry{}).Where("key = ?", "persisted-negative").Count(&persisted)
	if persisted != 0 {
		t.Error("the prune did not reach the database")
	}
}

// With the database gone, every write-through only warns: the in-memory cache
// keeps working, which is what a request in flight needs.
func TestArtworkCacheSurvivesADeadDatabase(t *testing.T) {
	t.Chdir(t.TempDir())
	resetMap()
	t.Cleanup(resetMap)
	closedCacheDB(t)

	key := artworkCacheKey(ArtworkEntityRelease, testMBID, ArtworkKindFront, 250)
	rememberNoArtwork(key)
	if !negativeCached(key) {
		t.Error("the in-memory negative was lost with the database")
	}

	ResetArtworkNegativeCache()
	if negativeCached(key) {
		t.Error("ResetArtworkNegativeCache did not clear memory when the database failed")
	}

	artworkIndexMu.Lock()
	for i := 0; i < artworkNegativeMax; i++ {
		artworkIndex[fmt.Sprintf("neg-%d", i)] = artworkMeta{missing: true}
	}
	artworkIndexMu.Unlock()
	pruneArtworkNegatives()
	if _, missing := ArtworkCacheCounts(); missing != 0 {
		t.Errorf("prune left %d negatives in memory", missing)
	}

	if err := artworkLoadCache(); err == nil {
		t.Error("artworkLoadCache read a closed database without error")
	}
	if err := artworkMigrateArtistKeys(); err == nil {
		t.Error("artworkMigrateArtistKeys read a closed database without error")
	}
}

func TestArtworkLoadCacheWithoutDatabase(t *testing.T) {
	orig := cacheDB
	cacheDB = nil
	t.Cleanup(func() { cacheDB = orig })
	if err := artworkLoadCache(); err != nil {
		t.Errorf("artworkLoadCache without a database = %v, want nil", err)
	}
}

// A legacy artist entry whose file cannot be moved stays where it is: it will miss
// and refetch normally, which is better than a row pointing at nothing.
func TestArtworkMigrationLeavesUnmovableEntries(t *testing.T) {
	t.Chdir(t.TempDir())
	dbForCache(t)

	legacy := artworkCacheKey(ArtworkEntityArtist, testMBID, ArtworkKindThumb, 250)
	writeArtworkCache(legacy, Artwork{Data: jpegBytes, ContentType: "image/jpeg"})
	// A non-empty directory at the canonical name blocks the rename.
	target := artworkCacheKey(ArtworkEntityArtist, testMBID, ArtworkKindThumb, 0)
	if err := os.MkdirAll(filepath.Join(artworkCachePath(target), "x"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := artworkMigrateArtistKeys(); err != nil {
		t.Fatalf("artworkMigrateArtistKeys: %v", err)
	}
	var rows int64
	cacheDB.Model(&models.ArtworkCacheEntry{}).Where("key = ?", legacy).Count(&rows)
	if rows != 1 {
		t.Errorf("legacy rows = %d, want the unmovable entry left in place", rows)
	}
}

package modules

import (
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// Surviving a restart is the entire point of the entity cache: a process-local
// discography meant every restart re-paid up to five rate-limited requests the
// first time anyone opened an artist page.
func TestEntityCacheSurvivesRestart(t *testing.T) {
	dbForCache(t)

	mbCachePut(models.MBEntityDiscography, "a1", []models.MusicBrainzArtistReleaseGroup{
		{ID: "rg-1", Title: "Spirit of Eden"},
	})

	// Wipe memory but not the database — what a restart looks like from here.
	resetMap()
	if MusicbrainzEntityFresh(models.MBEntityDiscography, "a1") {
		t.Fatal("memory was not actually cleared")
	}

	if err := musicbrainzLoadEntityCache(); err != nil {
		t.Fatalf("musicbrainzLoadEntityCache: %v", err)
	}

	var groups []models.MusicBrainzArtistReleaseGroup
	if !mbCacheGet(models.MBEntityDiscography, "a1", &groups) {
		t.Fatal("expected the discography to be restored from the database")
	}
	if len(groups) != 1 || groups[0].Title != "Spirit of Eden" {
		t.Fatalf("restored payload = %+v", groups)
	}
}

func TestEntityCacheForgetRemovesTheRow(t *testing.T) {
	dbForCache(t)

	mbCachePut(models.MBEntityArtist, "a1", models.MusicBrainzArtistLookup{ID: "a1", Name: "Talk Talk"})
	MusicbrainzForgetEntity(models.MBEntityArtist, "a1")

	resetMap()
	if err := musicbrainzLoadEntityCache(); err != nil {
		t.Fatalf("musicbrainzLoadEntityCache: %v", err)
	}

	var artist models.MusicBrainzArtistLookup
	if mbCacheGetStale(models.MBEntityArtist, "a1", &artist) {
		t.Fatal("a forgotten entry came back from the database")
	}
}

// Without a database the cache degrades to memory-only rather than failing, which
// is what the one-shot --file invocation and most tests run as.
func TestEntityCacheWithoutDB(t *testing.T) {
	resetEntityCache(t)

	mbCachePut(models.MBEntityArtist, "a1", models.MusicBrainzArtistLookup{ID: "a1", Name: "Talk Talk"})

	var artist models.MusicBrainzArtistLookup
	if !mbCacheGet(models.MBEntityArtist, "a1", &artist) {
		t.Fatal("expected an in-memory hit with no database configured")
	}
	if err := musicbrainzLoadEntityCache(); err != nil {
		t.Fatalf("loading with no database should be a no-op, got %v", err)
	}
}

// "No cover for this release" is the common answer for obscure releases. Held only
// in memory, it meant every restart re-asked the Cover Art Archive for thousands
// of covers it had already declined.
func TestArtworkNegativeCacheSurvivesRestart(t *testing.T) {
	dbForCache(t)

	key := artworkCacheKey(ArtworkEntityReleaseGroup, testMBID, ArtworkKindFront, 250)
	rememberNoArtwork(key)

	resetMap()
	if negativeCached(key) {
		t.Fatal("memory was not actually cleared")
	}

	if err := artworkLoadCache(); err != nil {
		t.Fatalf("artworkLoadCache: %v", err)
	}
	if !negativeCached(key) {
		t.Fatal("expected the negative result to be restored from the database")
	}
}

func TestArtworkPositiveEntryExpires(t *testing.T) {
	t.Chdir(t.TempDir())
	dbForCache(t)

	key := artworkCacheKey(ArtworkEntityReleaseGroup, testMBID, ArtworkKindFront, 250)
	writeArtworkCache(key, Artwork{Data: jpegBytes, ContentType: "image/jpeg"})

	if _, ok := readArtworkCache(key); !ok {
		t.Fatal("expected a fresh cache hit")
	}

	// Backdate the index entry. The bytes stay on disk — it is the index that
	// decides whether they are still trusted, which is what gives images an expiry
	// at all.
	storeArtworkMeta(key, artworkMeta{
		contentType: "image/jpeg",
		expiresAt:   time.Now().Add(-time.Minute),
	})
	if _, ok := readArtworkCache(key); ok {
		t.Fatal("an expired image must not be served from the cache")
	}
}

// Installs that cached artwork before the index existed have a full
// config/artwork/ directory; treating those files as misses would re-download
// every cover the install already has.
func TestArtworkAdoptsUnindexedFiles(t *testing.T) {
	t.Chdir(t.TempDir())
	dbForCache(t)

	key := artworkCacheKey(ArtworkEntityReleaseGroup, testMBID, ArtworkKindFront, 250)
	writeArtworkCache(key, Artwork{Data: jpegBytes, ContentType: "image/jpeg"})

	// Drop the index but keep the file, which is exactly the pre-index state.
	artworkIndexMu.Lock()
	artworkIndex = map[string]artworkMeta{}
	artworkIndexMu.Unlock()

	art, ok := readArtworkCache(key)
	if !ok {
		t.Fatal("expected the orphaned file to be adopted rather than ignored")
	}
	if !art.FromCache || art.ContentType != "image/jpeg" {
		t.Fatalf("adopted artwork = %+v", art)
	}

	// Adoption gives it a normal expiry, so it refreshes on the ordinary schedule.
	meta, known := artworkMetaFor(key)
	if !known || !meta.fresh() {
		t.Fatalf("expected an adopted entry with a live expiry, got %+v", meta)
	}
}

// Only negatives are capped: a positive entry required a real image to come back,
// while the artwork endpoint answers for any MBID anyone asks about.
func TestResetArtworkNegativeCacheKeepsPositives(t *testing.T) {
	t.Chdir(t.TempDir())
	dbForCache(t)

	positive := artworkCacheKey(ArtworkEntityReleaseGroup, testMBID, ArtworkKindFront, 250)
	negative := artworkCacheKey(ArtworkEntityReleaseGroup, testMBID, ArtworkKindFront, 500)

	writeArtworkCache(positive, Artwork{Data: jpegBytes, ContentType: "image/jpeg"})
	rememberNoArtwork(negative)

	ResetArtworkNegativeCache()

	if negativeCached(negative) {
		t.Error("the negative entry should have been cleared")
	}
	if _, ok := readArtworkCache(positive); !ok {
		t.Error("the positive entry should have survived")
	}
}

// Giving images an expiry is about picking up better scans over time, not about
// discarding a good one because a CDN is down.
func TestArtworkServesStaleWhenRefreshFails(t *testing.T) {
	t.Chdir(t.TempDir())
	dbForCache(t)

	server, calls := artworkTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	providers := ArtworkProviders{CoverArtEnabled: true, CoverArtBaseURL: server.URL}

	key := artworkCacheKey(ArtworkEntityReleaseGroup, testMBID, ArtworkKindFront, 250)
	writeArtworkCache(key, Artwork{Data: jpegBytes, ContentType: "image/jpeg"})
	storeArtworkMeta(key, artworkMeta{
		contentType: "image/jpeg",
		expiresAt:   time.Now().Add(-time.Minute),
	})

	art, err := GetArtwork(providers, ArtworkEntityReleaseGroup, testMBID, ArtworkKindFront, 250)
	if err != nil {
		t.Fatalf("a stale cover beats a blank tile: %v", err)
	}
	if !art.FromCache || len(art.Data) == 0 {
		t.Fatalf("expected the on-disk copy, got %+v", art)
	}
	if got := atomic.LoadInt64(calls); got != 1 {
		t.Errorf("upstream calls = %d, want 1 — the refresh should still have been attempted", got)
	}
}

// A negative entry has no file behind it, so an outage cannot conjure one.
func TestArtworkStaleFallbackSkipsNegatives(t *testing.T) {
	t.Chdir(t.TempDir())
	dbForCache(t)

	key := artworkCacheKey(ArtworkEntityReleaseGroup, testMBID, ArtworkKindFront, 250)
	rememberNoArtwork(key)

	if _, ok := readExpiredArtwork(key); ok {
		t.Fatal("a missing-marked entry must not resolve to an image")
	}
}

// Changing the artist cache key must not read as an empty cache. Most artists have
// no fanart entry, so a cold start after the upgrade would re-ask the provider about
// every one of them at ~2 req/s to learn what the database already recorded.
func TestArtworkMigrationFoldsLegacyArtistKeys(t *testing.T) {
	t.Chdir(t.TempDir())
	dbForCache(t)

	// What the old keying left behind: a portrait fetched at 250 for a collection
	// row, and the same artist's "no backdrop" answer recorded at 1200.
	legacyThumb := artworkCacheKey(ArtworkEntityArtist, testMBID, ArtworkKindThumb, 250)
	legacyBackground := artworkCacheKey(ArtworkEntityArtist, testMBID, ArtworkKindBackground, 1200)
	writeArtworkCache(legacyThumb, Artwork{Data: jpegBytes, ContentType: "image/jpeg"})
	rememberNoArtwork(legacyBackground)

	// A restart: memory cleared, database kept.
	artworkIndexMu.Lock()
	artworkIndex = map[string]artworkMeta{}
	artworkIndexMu.Unlock()

	if err := artworkLoadCache(); err != nil {
		t.Fatalf("artworkLoadCache: %v", err)
	}

	// The image is served under the key the running code now asks for, without
	// touching a provider.
	thumb := artworkCacheKey(ArtworkEntityArtist, testMBID, ArtworkKindThumb, 0)
	art, ok := readArtworkCache(thumb)
	if !ok {
		t.Fatal("the cached portrait did not survive the key change")
	}
	if len(art.Data) == 0 || art.ContentType != "image/jpeg" {
		t.Errorf("restored artwork = %+v", art)
	}

	// And the negative too, which is the bulk of the rows in a real install.
	if !negativeCached(artworkCacheKey(ArtworkEntityArtist, testMBID, ArtworkKindBackground, 0)) {
		t.Error("the recorded 'no backdrop' answer did not survive the key change")
	}

	// The legacy rows are gone rather than duplicated.
	var remaining int64
	if err := cacheDB.Model(&models.ArtworkCacheEntry{}).
		Where("key IN ?", []string{legacyThumb, legacyBackground}).Count(&remaining).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 0 {
		t.Errorf("%d legacy row(s) left behind", remaining)
	}

	// Running again changes nothing: it has to be safe on every boot, not just the
	// one after the upgrade.
	if err := artworkMigrateArtistKeys(); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if _, ok := readArtworkCache(thumb); !ok {
		t.Error("a second migration pass lost the entry it had already folded")
	}
}

// Two legacy entries for one artist are the same image by definition, so they
// collapse onto one rather than fighting over the canonical key.
func TestArtworkMigrationCollapsesDuplicates(t *testing.T) {
	t.Chdir(t.TempDir())
	dbForCache(t)

	for _, size := range []int{250, 500} {
		writeArtworkCache(artworkCacheKey(ArtworkEntityArtist, testMBID, ArtworkKindThumb, size),
			Artwork{Data: jpegBytes, ContentType: "image/jpeg"})
	}

	if err := artworkMigrateArtistKeys(); err != nil {
		t.Fatalf("artworkMigrateArtistKeys: %v", err)
	}

	var rows int64
	if err := cacheDB.Model(&models.ArtworkCacheEntry{}).Count(&rows).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("rows = %d, want 1 — duplicates should collapse", rows)
	}
}

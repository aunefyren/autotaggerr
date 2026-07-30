package modules

import (
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// resetEntityCache empties the process-global entity cache so one test's stub
// response cannot answer another test's call.
func resetEntityCache(t *testing.T) {
	t.Helper()
	mbEntityCacheMu.Lock()
	mbEntityCache = map[mbCacheKey]mbCacheRecord{}
	mbEntityCacheMu.Unlock()
}

// expireEntityCache backdates one entry's expiry while keeping its payload, which
// is how the stale-fallback paths are exercised without waiting out a TTL.
func expireEntityCache(t *testing.T, entity, mbid string) {
	t.Helper()
	key := mbCacheKey{entity: entity, mbid: mbid}

	mbEntityCacheMu.Lock()
	defer mbEntityCacheMu.Unlock()
	rec, ok := mbEntityCache[key]
	if !ok {
		t.Fatalf("no cached %s entry for %q to expire", entity, mbid)
	}
	rec.expiresAt = time.Now().Add(-time.Minute)
	mbEntityCache[key] = rec
}

func TestMBCacheRoundTrip(t *testing.T) {
	resetEntityCache(t)

	want := models.MusicBrainzArtistLookup{ID: "a1", Name: "Talk Talk"}
	mbCachePut(models.MBEntityArtist, "a1", want)

	var got models.MusicBrainzArtistLookup
	if !mbCacheGet(models.MBEntityArtist, "a1", &got) {
		t.Fatal("expected a fresh cache hit")
	}
	if got.Name != want.Name {
		t.Fatalf("got %q, want %q", got.Name, want.Name)
	}
}

// The (entity, MBID) key is composite for a reason: the same artist ID is both an
// artist lookup and a discography browse, with unrelated payload shapes.
func TestMBCacheSeparatesEntitiesSharingAnID(t *testing.T) {
	resetEntityCache(t)

	mbCachePut(models.MBEntityArtist, "a1", models.MusicBrainzArtistLookup{ID: "a1", Name: "Talk Talk"})
	mbCachePut(models.MBEntityDiscography, "a1", []models.MusicBrainzArtistReleaseGroup{{ID: "rg-1", Title: "Laughing Stock"}})

	var artist models.MusicBrainzArtistLookup
	if !mbCacheGet(models.MBEntityArtist, "a1", &artist) || artist.Name != "Talk Talk" {
		t.Fatalf("artist entry was clobbered: %+v", artist)
	}

	var groups []models.MusicBrainzArtistReleaseGroup
	if !mbCacheGet(models.MBEntityDiscography, "a1", &groups) {
		t.Fatal("expected a discography hit")
	}
	if len(groups) != 1 || groups[0].Title != "Laughing Stock" {
		t.Fatalf("discography entry was clobbered: %+v", groups)
	}
}

func TestMBCacheExpiryHidesEntryFromGetButNotStale(t *testing.T) {
	resetEntityCache(t)

	mbCachePut(models.MBEntityArtist, "a1", models.MusicBrainzArtistLookup{ID: "a1", Name: "Talk Talk"})
	expireEntityCache(t, models.MBEntityArtist, "a1")

	var fresh models.MusicBrainzArtistLookup
	if mbCacheGet(models.MBEntityArtist, "a1", &fresh) {
		t.Fatal("an expired entry must not read as fresh")
	}
	if MusicbrainzEntityFresh(models.MBEntityArtist, "a1") {
		t.Fatal("MusicbrainzEntityFresh must agree with mbCacheGet")
	}

	// The stale copy is what every caller falls back to when MusicBrainz is down,
	// so expiry must not discard it.
	var stale models.MusicBrainzArtistLookup
	if !mbCacheGetStale(models.MBEntityArtist, "a1", &stale) {
		t.Fatal("expected the expired payload to still be readable")
	}
	if stale.Name != "Talk Talk" {
		t.Fatalf("stale payload lost: %+v", stale)
	}
}

// A payload that no longer decodes into the caller's type reads as a miss rather
// than as an error, so a shape change between versions self-heals on refetch.
func TestMBCacheTypeMismatchReadsAsMiss(t *testing.T) {
	resetEntityCache(t)

	mbCachePut(models.MBEntityDiscography, "a1", []models.MusicBrainzArtistReleaseGroup{{ID: "rg-1"}})

	var wrongShape models.MusicBrainzArtistLookup
	if mbCacheGet(models.MBEntityDiscography, "a1", &wrongShape) {
		t.Fatal("a list payload must not decode into a struct and report a hit")
	}
}

func TestMBCacheForgetEntity(t *testing.T) {
	resetEntityCache(t)

	mbCachePut(models.MBEntityArtist, "a1", models.MusicBrainzArtistLookup{ID: "a1"})
	MusicbrainzForgetEntity(models.MBEntityArtist, "a1")

	// Forgotten means gone, not merely expired: VerifyArtistIdentity relies on the
	// stale fallback disappearing so a transport failure surfaces as an error.
	var stale models.MusicBrainzArtistLookup
	if mbCacheGetStale(models.MBEntityArtist, "a1", &stale) {
		t.Fatal("expected the entry to be dropped entirely")
	}
}

func TestMusicbrainzEntityCounts(t *testing.T) {
	resetEntityCache(t)

	mbCachePut(models.MBEntityArtist, "a1", models.MusicBrainzArtistLookup{ID: "a1"})
	mbCachePut(models.MBEntityArtist, "a2", models.MusicBrainzArtistLookup{ID: "a2"})
	mbCachePut(models.MBEntityEditions, "rg-1", []models.MusicBrainzReleaseSearchResult{})

	counts := MusicbrainzEntityCounts()
	if counts[models.MBEntityArtist] != 2 {
		t.Fatalf("artist count = %d, want 2", counts[models.MBEntityArtist])
	}
	if counts[models.MBEntityEditions] != 1 {
		t.Fatalf("editions count = %d, want 1", counts[models.MBEntityEditions])
	}
	if counts[models.MBEntityDiscography] != 0 {
		t.Fatalf("discography count = %d, want 0", counts[models.MBEntityDiscography])
	}
}

// The jitter exists so entries warmed together by one mirror pass do not all come
// due in the same minute a week later.
func TestMBCacheExpiryIsJittered(t *testing.T) {
	now := time.Now()
	base := time.Hour

	seen := map[time.Time]bool{}
	for i := 0; i < 50; i++ {
		expiry := mbCacheExpiry(now, base)
		if expiry.Before(now.Add(base)) {
			t.Fatalf("expiry %v is earlier than the base TTL", expiry.Sub(now))
		}
		if !expiry.Before(now.Add(base + base/2)) {
			t.Fatalf("expiry %v exceeds base + jitter", expiry.Sub(now))
		}
		seen[expiry] = true
	}
	if len(seen) < 2 {
		t.Fatal("expected jittered expiries to differ")
	}
}

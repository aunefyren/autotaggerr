package modules

import (
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// The exported test seams in testing.go are used by other packages' tests, so their
// contract — point MusicBrainz at a stub, start from empty caches, put everything
// back afterwards — is checked from here, where the unexported state is visible.

func TestSetMusicBrainzBaseURLForTestRestoresEverything(t *testing.T) {
	original := musicbrainzBaseURL

	t.Run("inner", func(t *testing.T) {
		// Dirty every piece of state the seam promises to reset.
		musicbrainzReleaseCacheMu.Lock()
		musicbrainzReleaseCache["stale"] = models.CachedMusicBrainzRelease{}
		musicbrainzReleaseCacheMu.Unlock()
		mbCachePut(models.MBEntityArtist, "stale-artist", models.MusicBrainzArtistLookup{ID: "stale-artist"})
		queryMutex.Lock()
		lastQueryTime = time.Now()
		queryMutex.Unlock()

		SetMusicBrainzBaseURLForTest(t, "http://stub.invalid")

		if musicbrainzBaseURL != "http://stub.invalid" {
			t.Errorf("base URL = %q, want the stub", musicbrainzBaseURL)
		}
		musicbrainzReleaseCacheMu.Lock()
		n := len(musicbrainzReleaseCache)
		musicbrainzReleaseCacheMu.Unlock()
		if n != 0 {
			t.Errorf("release cache holds %d entries, want it cleared", n)
		}
		var a models.MusicBrainzArtistLookup
		if mbCacheGetStale(models.MBEntityArtist, "stale-artist", &a) {
			t.Error("entity cache still answers a pre-seam entry")
		}
		queryMutex.Lock()
		last := lastQueryTime
		queryMutex.Unlock()
		if !last.IsZero() {
			t.Error("the rate limiter was not cleared, so the next request would sleep")
		}

		// Something this test leaves behind must not survive its cleanup.
		SeedMusicBrainzEntityForTest(t, models.MBEntityArtist, "seeded", models.MusicBrainzArtistLookup{ID: "seeded", Name: "Seeded"})
		var seeded models.MusicBrainzArtistLookup
		if !mbCacheGet(models.MBEntityArtist, "seeded", &seeded) || seeded.Name != "Seeded" {
			t.Errorf("seeded entity not served from cache: %+v", seeded)
		}
	})

	if musicbrainzBaseURL != original {
		t.Errorf("base URL after cleanup = %q, want %q restored", musicbrainzBaseURL, original)
	}
	var seeded models.MusicBrainzArtistLookup
	if mbCacheGetStale(models.MBEntityArtist, "seeded", &seeded) {
		t.Error("an entity seeded inside the test outlived its cleanup")
	}
}

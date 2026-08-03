package modules

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// seedReleaseCache replaces the process-global release cache with a single fresh
// entry and returns a cleanup that empties it again.
func seedReleaseCache(t *testing.T, mbID string, rel models.MusicBrainzReleaseResponse, expiresAt time.Time) {
	t.Helper()
	musicbrainzReleaseCacheMu.Lock()
	musicbrainzReleaseCache = map[string]models.CachedMusicBrainzRelease{
		mbID: {Release: rel, Timestamp: time.Now(), ExpiresAt: expiresAt},
	}
	musicbrainzReleaseCacheMu.Unlock()
	t.Cleanup(func() {
		musicbrainzReleaseCacheMu.Lock()
		musicbrainzReleaseCache = map[string]models.CachedMusicBrainzRelease{}
		musicbrainzReleaseCacheMu.Unlock()
	})
}

func TestMusicbrainzReleaseCacheSize(t *testing.T) {
	seedReleaseCache(t, "rel-1", models.MusicBrainzReleaseResponse{ID: "rel-1"}, time.Now().Add(time.Hour))
	if n := MusicbrainzReleaseCacheSize(); n != 1 {
		t.Errorf("cache size = %d, want 1", n)
	}
}

func TestMusicbrainzReleaseFresh(t *testing.T) {
	seedReleaseCache(t, "rel-1", models.MusicBrainzReleaseResponse{ID: "rel-1"}, time.Now().Add(time.Hour))
	if !MusicbrainzReleaseFresh("rel-1") {
		t.Error("a cached, unexpired release should read fresh")
	}
	if MusicbrainzReleaseFresh("rel-missing") {
		t.Error("an uncached release must not read fresh")
	}
}

func TestMusicbrainzReleaseFreshExpired(t *testing.T) {
	seedReleaseCache(t, "rel-1", models.MusicBrainzReleaseResponse{ID: "rel-1"}, time.Now().Add(-time.Minute))
	if MusicbrainzReleaseFresh("rel-1") {
		t.Error("an expired release must not read fresh")
	}
}

// MusicbrainzExtendExpiry pushes a cached release's expiry out, but only forward —
// a shorter TTL than what is already set leaves it untouched.
func TestMusicbrainzExtendExpiry(t *testing.T) {
	base := time.Now()
	seedReleaseCache(t, "rel-1", models.MusicBrainzReleaseResponse{ID: "rel-1"}, base.Add(10*time.Minute))

	MusicbrainzExtendExpiry("rel-1", 2*time.Hour)
	musicbrainzReleaseCacheMu.RLock()
	extended := musicbrainzReleaseCache["rel-1"].ExpiresAt
	musicbrainzReleaseCacheMu.RUnlock()
	if !extended.After(base.Add(time.Hour)) {
		t.Errorf("expiry was not extended: %v", extended)
	}

	// A shorter extension than the current expiry is a no-op.
	before := extended
	MusicbrainzExtendExpiry("rel-1", time.Minute)
	musicbrainzReleaseCacheMu.RLock()
	after := musicbrainzReleaseCache["rel-1"].ExpiresAt
	musicbrainzReleaseCacheMu.RUnlock()
	if !after.Equal(before) {
		t.Errorf("a shorter TTL changed the expiry: %v -> %v", before, after)
	}

	// Extending an uncached release is a harmless no-op.
	MusicbrainzExtendExpiry("rel-missing", time.Hour)
}

func TestCachedReleaseGroupID(t *testing.T) {
	rel := models.MusicBrainzReleaseResponse{ID: "rel-1"}
	rel.ReleaseGroup.ID = "rg-1"
	seedReleaseCache(t, "rel-1", rel, time.Now().Add(time.Hour))

	if id, ok := CachedReleaseGroupID("rel-1"); !ok || id != "rg-1" {
		t.Errorf("CachedReleaseGroupID = %q, %v; want rg-1, true", id, ok)
	}
	if _, ok := CachedReleaseGroupID("rel-missing"); ok {
		t.Error("an uncached release should report no group")
	}
}

func TestAudioFilesInFolder(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"01.flac", "02.mp3", "03.ogg", "cover.jpg", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The path handed in is a file beside the others; the count is of its siblings.
	if got := AudioFilesInFolder(filepath.Join(dir, "01.flac")); got != 3 {
		t.Errorf("AudioFilesInFolder = %d, want 3 (flac+mp3+ogg)", got)
	}
}

package modules

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// TestAlbumRefreshSetConcurrent verifies the shared refresh collector is safe to
// Add to from many goroutines and returns a consistent snapshot.
func TestAlbumRefreshSetConcurrent(t *testing.T) {
	set := NewAlbumRefreshSet(map[string]string{"seed": "seed-key"})

	const workers = 16
	const perWorker = 200

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				name := fmt.Sprintf("album-%d-%d", w, i)
				set.Add(name, fmt.Sprintf("key-%d-%d", w, i))
			}
		}(w)
	}
	wg.Wait()

	snap := set.Snapshot()
	want := workers*perWorker + 1 // + the seed entry
	if len(snap) != want {
		t.Fatalf("snapshot has %d entries, want %d", len(snap), want)
	}
	if snap["seed"] != "seed-key" {
		t.Errorf("seed entry missing or wrong: %q", snap["seed"])
	}
}

// TestMusicbrainzCacheConcurrentAccess hammers the release cache with concurrent
// inserts (as QueryMusicBrainzReleaseData does) while flushing it to disk (marshal
// under the read lock). Run with -race, this catches unsynchronized map access.
func TestMusicbrainzCacheConcurrentAccess(t *testing.T) {
	// redirect the on-disk path to a temp file and start from an empty cache
	origPath := musicbrainzReleaseCachePath
	musicbrainzReleaseCachePath = filepath.Join(t.TempDir(), "mb_releases.json")
	musicbrainzReleaseCacheMu.Lock()
	musicbrainzReleaseCache = map[string]models.CachedMusicBrainzRelease{}
	musicbrainzReleaseCacheMu.Unlock()
	t.Cleanup(func() { musicbrainzReleaseCachePath = origPath })

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// flusher: repeatedly marshal + write the cache
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if err := MusicbrainzSaveCache(); err != nil {
					t.Errorf("save failed: %v", err)
					return
				}
			}
		}
	}()

	// writers: concurrently insert entries
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				now := time.Now()
				musicbrainzReleaseCacheMu.Lock()
				musicbrainzReleaseCache[fmt.Sprintf("%d-%d", w, i)] = models.CachedMusicBrainzRelease{
					Timestamp: now,
					ExpiresAt: now.Add(time.Hour),
				}
				musicbrainzReleaseCacheMu.Unlock()
			}
		}(w)
	}

	// let writers finish, then stop the flusher
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

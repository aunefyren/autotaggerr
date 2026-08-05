package modules

import (
	"fmt"
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
// inserts (as QueryMusicBrainzReleaseData does) while a reader walks it under the
// read lock. Run with -race, this catches unsynchronized map access.
func TestMusicbrainzCacheConcurrentAccess(t *testing.T) {
	musicbrainzReleaseCacheMu.Lock()
	musicbrainzReleaseCache = map[string]models.CachedMusicBrainzRelease{}
	musicbrainzReleaseCacheMu.Unlock()

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// reader: repeatedly walk the whole map, as the due-for-refresh sweep does
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				MusicbrainzDueForRefresh()
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

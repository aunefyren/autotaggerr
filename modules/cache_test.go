package modules

import (
	"errors"
	"testing"
)

// resetCacheState clears the package-level batching state so each test starts
// clean regardless of registrations made by init() or other tests.
func resetCacheState() {
	cacheMu.Lock()
	cacheDirty = map[string]bool{}
	cacheWriters = map[string]cacheWriter{}
	cacheMu.Unlock()
}

func TestFlushCachesOnlyWritesDirty(t *testing.T) {
	resetCacheState()

	writes := 0
	registerCache("test_cache", func() error {
		writes++
		return nil
	})

	// Clean cache: flushing writes nothing.
	FlushCaches()
	if writes != 0 {
		t.Fatalf("expected 0 writes for a clean cache, got %d", writes)
	}

	// Many marks collapse into a single write on the next flush.
	for i := 0; i < 100; i++ {
		markCacheDirty("test_cache")
	}
	FlushCaches()
	if writes != 1 {
		t.Fatalf("expected 1 write after 100 marks + flush, got %d", writes)
	}

	// A second flush with no new marks is a no-op.
	FlushCaches()
	if writes != 1 {
		t.Fatalf("expected no additional write on a clean flush, got %d", writes)
	}

	// New changes flush again.
	markCacheDirty("test_cache")
	FlushCaches()
	if writes != 2 {
		t.Fatalf("expected 2 writes after re-dirtying, got %d", writes)
	}
}

func TestFlushCachesKeepsDirtyOnWriteError(t *testing.T) {
	resetCacheState()

	fail := true
	writes := 0
	registerCache("flaky_cache", func() error {
		writes++
		if fail {
			return errors.New("disk full")
		}
		return nil
	})

	markCacheDirty("flaky_cache")

	// First flush fails; the cache must remain dirty so it is retried.
	FlushCaches()
	if writes != 1 {
		t.Fatalf("expected 1 write attempt, got %d", writes)
	}

	// Recover and flush again: the still-dirty cache is retried and clears.
	fail = false
	FlushCaches()
	if writes != 2 {
		t.Fatalf("expected retry write after failure, got %d", writes)
	}

	// Now clean: no further writes.
	FlushCaches()
	if writes != 2 {
		t.Fatalf("expected no write after successful flush, got %d", writes)
	}
}

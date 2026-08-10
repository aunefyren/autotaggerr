package collection

import (
	"errors"
	"sync"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
)

// TestRetryBusyOnlyRetriesLockErrors is the classification guard. A rebuild that fails
// for a real reason must fail once and say so — re-running it would turn a broken
// database into four broken databases and a longer wait for the same error.
func TestRetryBusyOnlyRetriesLockErrors(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		calls int
	}{
		{"lock contention", errors.New("database is locked (517)"), rebuildBusyRetries + 1},
		{"table lock", errors.New("database table is locked"), rebuildBusyRetries + 1},
		{"a real failure", errors.New("no such column: owned"), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			err := retryBusy(func() error {
				calls++
				return tc.err
			})
			if !errors.Is(err, tc.err) {
				t.Errorf("error = %v, want the cause to survive the retries", err)
			}
			if calls != tc.calls {
				t.Errorf("ran %d times, want %d", calls, tc.calls)
			}
		})
	}
}

// TestRetryBusyStopsOnSuccess: a transaction that succeeds on its second attempt is
// the case this exists for, and it must not keep going afterwards.
func TestRetryBusyStopsOnSuccess(t *testing.T) {
	calls := 0
	err := retryBusy(func() error {
		calls++
		if calls == 1 {
			return errors.New("database is locked (517)")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retryBusy: %v", err)
	}
	if calls != 2 {
		t.Errorf("ran %d times, want 2 (the loss and the retry)", calls)
	}
}

// TestRebuildSurvivesAConcurrentWriter is the end-to-end version: a rebuild racing a
// steady stream of writes has to land, not give up and leave the disk view stale.
//
// Rebuild reads before it writes, so under WAL it holds a read snapshot and upgrades
// when it clears the disk view — an upgrade SQLite refuses outright (SQLITE_BUSY_
// SNAPSHOT) if anyone committed in between, with no waiting and no help from
// busy_timeout. That is not hypothetical: attaching a file requests a rebuild and then
// carries on writing its tags and its Activity event, so the rebuild races the very
// request that asked for it.
func TestRebuildSurvivesAConcurrentWriter(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)

	seedRelease(t, db, "rel-1", "rg-1", "art-1", "Album", 2)
	if err := modules.MusicbrainzLoadCache(); err != nil {
		t.Fatalf("load cache: %v", err)
	}
	lib := models.Library{Name: "L", Path: "/music", Enabled: true}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	ownFile(t, db, "/music/a.flac", "rel-1", lib)

	// A writer committing throughout the rebuild, the way the rest of an attach
	// request does.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			ev := models.Event{Type: models.EventTypeTagFiles, Status: models.EventStatusOK, Title: "noise"}
			_ = db.Create(&ev).Error
		}
	}()

	var failures int
	for i := 0; i < 20; i++ {
		if _, err := Rebuild(db); err != nil {
			failures++
			t.Logf("rebuild %d failed: %s", i, err.Error())
		}
	}
	close(stop)
	wg.Wait()

	if failures > 0 {
		t.Errorf("%d of 20 rebuilds failed against a concurrent writer; they must be retried, not lost", failures)
	}

	// And the view they were deriving is actually there.
	var owned int64
	if err := db.Model(&models.CollectionReleaseGroup{}).Where("owned = ?", true).Count(&owned).Error; err != nil {
		t.Fatalf("count owned: %v", err)
	}
	if owned == 0 {
		t.Error("the disk view is empty after 20 rebuilds — the passes ran but wrote nothing")
	}
}

package collection

import (
	"errors"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// What a pass reports about itself, and the discography sync's failure handling.

// TestSyncReportingCarriesGhosts: an album the manager lists under a dead MusicBrainz
// ID is a finding of the sync, so every one of the three emitters must carry it.
func TestSyncReportingCarriesGhosts(t *testing.T) {
	stats := SyncStats{
		ArtistsSynced: 2, Groups: 5,
		Unknown:  []string{"art-x"},
		Ghosts:   []string{"rg-dead"},
		Failures: []string{"Lidarr: down"},
	}

	items := SyncEventItems(stats)
	if len(items) != 2 || items[1].Path != "rg-dead" || items[1].Status != models.EventItemStatusGone || items[1].Error == "" {
		t.Errorf("items = %+v, want the unknown artist then the ghost album with a reason", items)
	}

	details := SyncEventDetails(stats)
	for _, key := range []string{"unknown_artists", "ghost_albums", "failures"} {
		if _, ok := details[key]; !ok {
			t.Errorf("details missing %q: %+v", key, details)
		}
	}

	line := SyncSummaryLine(stats)
	for _, want := range []string{"1 not in Lidarr", "1 not in MusicBrainz", "1 lookup(s) failed"} {
		if !strings.Contains(line, want) {
			t.Errorf("summary %q missing %q", line, want)
		}
	}
}

// TestRecordScanStatesWhyItWasEmpty: a scan with nothing to read says why in its
// event, while still recording that it ran and finishing OK.
func TestRecordScanStatesWhyItWasEmpty(t *testing.T) {
	db := testDB(t)
	stats, err := RecordScan(db, "Collection scan", RebuildScope{}, nil)
	if err != nil {
		t.Fatalf("RecordScan: %v", err)
	}
	if stats.EmptyReason != ScanEmptyNoFiles {
		t.Fatalf("empty reason = %q, want %q", stats.EmptyReason, ScanEmptyNoFiles)
	}

	var ev models.Event
	if err := db.Where("type = ?", models.EventTypeCollectionScan).First(&ev).Error; err != nil {
		t.Fatalf("no collection_scan event: %v", err)
	}
	if ev.Status != models.EventStatusOK {
		t.Errorf("status = %q, want ok — nothing failed", ev.Status)
	}
	if !strings.Contains(ev.Summary, ScanEmptyNoFiles) || ev.Details["empty_reason"] != ScanEmptyNoFiles {
		t.Errorf("event = %q / %+v, want the empty reason in both", ev.Summary, ev.Details)
	}
}

// TestScanEmptyReasonCases: the reason is only for a pass with no input, and it
// distinguishes the artist with no files from the collection with none.
func TestScanEmptyReasonCases(t *testing.T) {
	db := testDB(t)
	artist := bounds{artist: "art-1"}
	all := bounds{all: true}

	if got := scanEmptyReason(db, all, 3, 3); got != "" {
		t.Errorf("pass with input = %q, want none", got)
	}
	if got := scanEmptyReason(db, all, 3, 0); got != "" {
		t.Errorf("unscoped pass with matched rows = %q, want none", got)
	}
	if got := scanEmptyReason(db, artist, 3, 0); got != ScanEmptyArtistNoFiles {
		t.Errorf("artist with no files among many = %q", got)
	}

	if err := db.Create(&models.LibraryItem{Path: "/m/x.flac", Status: models.LibraryItemStatusError}).Error; err != nil {
		t.Fatalf("item: %v", err)
	}
	if got := scanEmptyReason(db, artist, 0, 0); got != ScanEmptyArtistNoFiles {
		t.Errorf("artist pass over an unmatched index = %q", got)
	}
	if got := scanEmptyReason(db, all, 0, 0); got != ScanEmptyNothingMatched {
		t.Errorf("collection pass over an unmatched index = %q", got)
	}

	if got := scanEmptyReason(closedDB(t), all, 0, 0); got != "" {
		t.Errorf("unreadable index = %q, want no reason rather than a guess", got)
	}
}

// TestSyncArtistFailures: an unreachable discography is the sync's error; an
// unreadable artist record and a failed prune are not, because neither stops the
// discography being mirrored.
func TestSyncArtistFailures(t *testing.T) {
	seed := func(t *testing.T) *models.CollectionArtist {
		return &models.CollectionArtist{
			MBID: "art-1", Name: "Band", Monitored: true,
			ManagedBy: models.ManagedByAutotaggerr, FollowTypes: "Album",
		}
	}

	t.Run("discography unreachable", func(t *testing.T) {
		db := testDB(t)
		if err := db.Create(seed(t)).Error; err != nil {
			t.Fatalf("artist: %v", err)
		}
		meta := fakeMeta{rgs: func(string) ([]models.MusicBrainzArtistReleaseGroup, bool, error) {
			return nil, false, errors.New("503")
		}}
		if _, err := SyncArtist(db, meta, "art-1"); err == nil {
			t.Error("want the discography error")
		}
	})

	t.Run("artist lookup and prune fail", func(t *testing.T) {
		db := testDB(t)
		if err := db.Create(seed(t)).Error; err != nil {
			t.Fatalf("artist: %v", err)
		}
		storeGroup(t, db, "rg-merged", "art-1", nil)
		dropTable(t, db, &models.CollectionDesire{})

		meta := fakeMeta{
			artist: func(string) (models.MusicBrainzArtistLookup, error) {
				return models.MusicBrainzArtistLookup{}, errors.New("timeout")
			},
			rgs: func(string) ([]models.MusicBrainzArtistReleaseGroup, bool, error) {
				return []models.MusicBrainzArtistReleaseGroup{{ID: "rg-1", Title: "One", PrimaryType: "Album"}}, true, nil
			},
		}
		wanted, err := SyncArtist(db, meta, "art-1")
		if err != nil || wanted != 1 {
			t.Fatalf("SyncArtist = %d, %v; want 1 and no error", wanted, err)
		}
		if !groupExists(t, db, "rg-merged") {
			t.Error("a prune that could not check claims must not delete")
		}
	})

	t.Run("complete discography prunes", func(t *testing.T) {
		db := testDB(t)
		if err := db.Create(seed(t)).Error; err != nil {
			t.Fatalf("artist: %v", err)
		}
		storeGroup(t, db, "rg-merged", "art-1", nil)
		blockWrites(t, db, "collection_artists", "UPDATE")

		meta := fakeMeta{rgs: func(string) ([]models.MusicBrainzArtistReleaseGroup, bool, error) {
			return []models.MusicBrainzArtistReleaseGroup{{ID: "rg-1", Title: "One", PrimaryType: "Album"}}, true, nil
		}}
		if _, err := SyncArtist(db, meta, "art-1"); err != nil {
			t.Fatalf("SyncArtist: %v — a failed last_synced_at stamp is not the sync's error", err)
		}
		if groupExists(t, db, "rg-merged") {
			t.Error("a group MusicBrainz no longer lists and nothing claims should be pruned")
		}
	})
}

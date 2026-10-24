package collection

import (
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// TestRebuildReportsWhatItAdded: a Scan's totals are a state, and a nightly feed of
// identical states is unreadable. The deltas are the only part of the row that says
// which night was the interesting one.
func TestRebuildReportsWhatItAdded(t *testing.T) {
	db := twoArtistCollection(t)

	// The fixture has already rebuilt once, so a second pass over the same files must
	// report no movement at all — a delta that fires on every run is worse than none.
	stats, err := Rebuild(db)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if stats.ArtistsAdded != 0 || stats.AlbumsAdded != 0 || stats.ArtistsRemoved != 0 || stats.AlbumsRemoved != 0 {
		t.Fatalf("a no-op pass reported movement: %+v", stats)
	}

	var lib models.Library
	if err := db.First(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	seedOwnedAlbum(t, db, lib, "art-portishead", "Portishead", "rg-dummy", "rel-dummy", "/m/Portishead/Dummy/01.flac")

	stats, err = Rebuild(db)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if stats.ArtistsAdded != 1 || stats.AlbumsAdded != 1 {
		t.Errorf("added: %d artist(s), %d album(s); want 1 and 1 (%+v)", stats.ArtistsAdded, stats.AlbumsAdded, stats)
	}
	if stats.ArtistsRemoved != 0 || stats.AlbumsRemoved != 0 {
		t.Errorf("a pass that only added reported removals: %+v", stats)
	}
}

// TestRebuildReportsWhatWentAway is the half worth colouring: an album leaving the disk
// view means files moved or a correlation broke, and neither is something a Scan can
// fix on its own. Reported as a count, not inferred from a total that also moved for
// other reasons.
func TestRebuildReportsWhatWentAway(t *testing.T) {
	db := twoArtistCollection(t)

	if err := db.Where("mb_release_id = ?", "rel-blueprint").Delete(&models.LibraryItem{}).Error; err != nil {
		t.Fatalf("delete item: %v", err)
	}

	stats, err := Rebuild(db)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if stats.AlbumsRemoved != 1 {
		t.Errorf("albums removed = %d, want 1 (%+v)", stats.AlbumsRemoved, stats)
	}
	if stats.ArtistsRemoved != 1 {
		t.Errorf("artists removed = %d, want 1 — the artist is credited to nothing now (%+v)", stats.ArtistsRemoved, stats)
	}
	if stats.AlbumsAdded != 0 {
		t.Errorf("a pass that only lost rows reported additions: %+v", stats)
	}
}

// TestScanEventStatesWhatMoved: the counters and the summary line are what a reader
// actually sees, so the deltas have to reach both. A quiet pass says nothing extra —
// four zeroes appended to every nightly row is how the interesting ones get missed.
func TestScanEventStatesWhatMoved(t *testing.T) {
	db := twoArtistCollection(t)

	stats, err := RecordScan(db, "Collection scan", RebuildScope{}, nil)
	if err != nil {
		t.Fatalf("RecordScan: %v", err)
	}
	if got := changeClause(stats); got != "" {
		t.Errorf("a no-op pass wrote a change clause: %q", got)
	}

	var lib models.Library
	if err := db.First(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	seedOwnedAlbum(t, db, lib, "art-portishead", "Portishead", "rg-dummy", "rel-dummy", "/m/Portishead/Dummy/01.flac")

	if _, err := RecordScan(db, "Collection scan", RebuildScope{}, nil); err != nil {
		t.Fatalf("RecordScan: %v", err)
	}

	var ev models.Event
	if err := db.Where("type = ?", models.EventTypeCollectionScan).Order("started_at desc").First(&ev).Error; err != nil {
		t.Fatalf("no collection_scan event: %v", err)
	}
	if !strings.Contains(ev.Summary, "1 artist(s) added") || !strings.Contains(ev.Summary, "1 album(s) added") {
		t.Errorf("summary did not state what moved: %q", ev.Summary)
	}

	byLabel := map[string]int{}
	for _, s := range ev.Stats {
		byLabel[s.Label] = s.Value
	}
	if byLabel["Artists added"] != 1 || byLabel["Albums added"] != 1 {
		t.Errorf("counters did not carry the deltas: %+v", ev.Stats)
	}
}

// TestScanEventNamesWhatMoved: "1 album gone" names no album, and which one is the
// first thing anyone reading it wants. The movement counters each have rows behind
// them now, entity rows the feed resolves to names and links.
func TestScanEventNamesWhatMoved(t *testing.T) {
	db := twoArtistCollection(t)

	if err := db.Where("mb_release_id = ?", "rel-blueprint").Delete(&models.LibraryItem{}).Error; err != nil {
		t.Fatalf("delete item: %v", err)
	}
	var lib models.Library
	if err := db.First(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	seedOwnedAlbum(t, db, lib, "art-portishead", "Portishead", "rg-dummy", "rel-dummy", "/m/Portishead/Dummy/01.flac")

	if _, err := RecordScan(db, "Collection scan", RebuildScope{}, nil); err != nil {
		t.Fatalf("RecordScan: %v", err)
	}
	var ev models.Event
	if err := db.Where("type = ?", models.EventTypeCollectionScan).Order("started_at desc").First(&ev).Error; err != nil {
		t.Fatalf("no collection_scan event: %v", err)
	}
	var items []models.EventItem
	if err := db.Where("event_id = ?", ev.ID).Find(&items).Error; err != nil {
		t.Fatalf("items: %v", err)
	}
	got := map[string]string{}
	for _, it := range items {
		if it.Kind != models.EventItemKindEntity {
			t.Errorf("movement row %+v is not an entity row", it)
		}
		got[it.Phase+"/"+it.Status] += it.Path + " "
	}
	want := map[string]string{
		models.EventItemPhaseArtists + "/" + models.EventItemStatusAdded: "art-portishead ",
		models.EventItemPhaseAlbums + "/" + models.EventItemStatusAdded:  "rg-dummy ",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
	if got[models.EventItemPhaseAlbums+"/"+models.EventItemStatusLeft] == "" ||
		got[models.EventItemPhaseArtists+"/"+models.EventItemStatusLeft] == "" {
		t.Errorf("the album and artist that left are not named: %v", got)
	}
	for _, s := range ev.Stats {
		if s.Label == "Albums gone" && s.Filter != models.EventItemStatusLeft {
			t.Errorf("Albums gone is not selectable: %+v", s)
		}
	}
}

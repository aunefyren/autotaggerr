package process

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
)

// plexRefreshMock serves the one endpoint flushPlex reaches — the album refresh — and
// counts the calls. Seeding the refresh set with album keys skips Plex's section and
// album resolution entirely, which is what keeps this to a single route.
func plexRefreshMock(t *testing.T, hits *int32) *modules.PlexClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return modules.NewPlexClient(server.URL, "token")
}

// TestFlushPlexParentsItsEvent pins where a Plex refresh sits in the feed: under the run
// that caused it. It used to be recorded top-level whatever produced it, so a refresh
// appeared beside a run with nothing saying they were the same work — and the
// interactive re-tag, which passed no parent at all, was the last path still doing that.
func TestFlushPlexParentsItsEvent(t *testing.T) {
	db := newTestDB(t)
	var hits int32
	r := NewRunner(db, plexRefreshMock(t, &hits), models.ConfigStruct{AutotaggerrVersion: "test"})

	parent := events.Begin(db, models.EventTypeTagFiles, "Tag 2 attached files")
	set := modules.NewAlbumRefreshSet(map[string]string{
		"Album One": "/library/metadata/1",
		"Album Two": "/library/metadata/2",
	})

	r.flushPlex(set, parent)

	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("Plex refreshed %d album(s), want 2", got)
	}

	var ev models.Event
	if err := db.Where("type = ?", models.EventTypePlexRefresh).First(&ev).Error; err != nil {
		t.Fatalf("no plex_refresh event recorded: %v", err)
	}
	if ev.ParentID == nil {
		t.Fatal("Plex refresh recorded top-level; it belongs under the run that triggered it")
	}
	if *ev.ParentID != parent.ID {
		t.Errorf("Plex refresh parented to %s, want %s", *ev.ParentID, parent.ID)
	}
	if ev.Status != models.EventStatusOK {
		t.Errorf("status = %q, want %q (summary: %q)", ev.Status, models.EventStatusOK, ev.Summary)
	}
}

// TestFlushPlexSkipsAnEmptySet is the converse: nothing was touched, so nothing is
// reported. A run that changed no files must not leave a Plex refresh in the feed
// claiming it refreshed zero albums.
func TestFlushPlexSkipsAnEmptySet(t *testing.T) {
	db := newTestDB(t)
	var hits int32
	r := NewRunner(db, plexRefreshMock(t, &hits), models.ConfigStruct{AutotaggerrVersion: "test"})

	parent := events.Begin(db, models.EventTypeTagFiles, "Tag 1 attached file")
	r.flushPlex(modules.NewAlbumRefreshSet(nil), parent)

	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("Plex called %d time(s) for an empty set, want 0", got)
	}
	var count int64
	db.Model(&models.Event{}).Where("type = ?", models.EventTypePlexRefresh).Count(&count)
	if count != 0 {
		t.Errorf("recorded %d plex_refresh events for an empty set, want 0", count)
	}
}

// TestFlushPlexRecordsEachAlbum: a Plex refresh provokes exactly one question — which
// albums? — and this stage used to answer it with two numbers over nothing. The rows
// are what the counters filter, so a stage with counters and no rows has chips that
// select an empty list.
func TestFlushPlexRecordsEachAlbum(t *testing.T) {
	db := newTestDB(t)
	var hits int32
	r := NewRunner(db, plexRefreshMock(t, &hits), models.ConfigStruct{AutotaggerrVersion: "test"})

	parent := events.Begin(db, models.EventTypeProcess, "Processing music")
	r.flushPlex(modules.NewAlbumRefreshSet(map[string]string{
		"Spirit of Eden": "/library/metadata/1",
		"Laughing Stock": "/library/metadata/2",
	}), parent)

	var ev models.Event
	if err := db.Where("type = ?", models.EventTypePlexRefresh).First(&ev).Error; err != nil {
		t.Fatalf("no plex_refresh event: %v", err)
	}
	items, err := events.Items(db, ev.ID)
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("recorded %d album row(s), want 2", len(items))
	}
	// Sorted, so two runs over the same set read the same way rather than reshuffling
	// with the map's iteration order.
	if items[0].Path != "Laughing Stock" || items[1].Path != "Spirit of Eden" {
		t.Errorf("album rows out of order: %q, %q", items[0].Path, items[1].Path)
	}
	for _, it := range items {
		if it.Kind != models.EventItemKindAlbum {
			t.Errorf("%q recorded as kind %q, want %q — a file row would claim tags were written", it.Path, it.Kind, models.EventItemKindAlbum)
		}
		if it.Status != models.EventItemStatusRefreshed {
			t.Errorf("%q status = %q, want refreshed", it.Path, it.Status)
		}
	}

	// Both counters have to select something, or the chips are dead controls.
	for _, stat := range ev.Stats {
		if stat.Filter == "" {
			t.Errorf("counter %q selects nothing; the rows exist to be filtered", stat.Label)
		}
	}
}

// TestFlushPlexRecordsAFailure: an album Plex refused is the row worth having. It used
// to reach the feed as a name in a details blob nothing rendered, so "which one failed,
// and why" was unanswerable from the UI.
func TestFlushPlexRecordsAFailure(t *testing.T) {
	db := newTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	r := NewRunner(db, modules.NewPlexClient(server.URL, "token"), models.ConfigStruct{AutotaggerrVersion: "test"})

	parent := events.Begin(db, models.EventTypeProcess, "Processing music")
	r.flushPlex(modules.NewAlbumRefreshSet(map[string]string{"Spirit of Eden": "/library/metadata/1"}), parent)

	var ev models.Event
	if err := db.Where("type = ?", models.EventTypePlexRefresh).First(&ev).Error; err != nil {
		t.Fatalf("no plex_refresh event: %v", err)
	}
	if ev.Status != models.EventStatusError {
		t.Errorf("status = %q, want error", ev.Status)
	}
	items, _ := events.Items(db, ev.ID)
	if len(items) != 1 {
		t.Fatalf("recorded %d row(s), want 1", len(items))
	}
	if items[0].Status != models.EventItemStatusError || items[0].Error == "" {
		t.Errorf("failed album row = %+v, want an error status and a reason", items[0])
	}
}

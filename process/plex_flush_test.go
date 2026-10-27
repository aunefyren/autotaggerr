package process

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// plexRefreshMock is a media server row whose server serves the one endpoint
// flushPlex reaches — the album refresh — answering status and counting the calls.
// Seeding the refresh set with album keys skips Plex's section and album resolution
// entirely, which is what keeps this to a single route. Returns the row's ID.
func plexRefreshMock(t *testing.T, db *gorm.DB, name string, status int, hits *int32) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	row := models.MediaServer{Name: name, Type: models.MediaServerTypePlex, Enabled: true, BaseURL: server.URL, Token: "token"}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row.ID.String()
}

// refreshSetOf is a refresh set holding albums (name -> key) on one server.
func refreshSetOf(serverID string, albums map[string]string) *modules.AlbumRefreshSet {
	set := modules.NewAlbumRefreshSet()
	for name, key := range albums {
		set.Add(serverID, name, key)
	}
	return set
}

// TestFlushPlexParentsItsEvent pins where a Plex refresh sits in the feed: under the run
// that caused it. It used to be recorded top-level whatever produced it, so a refresh
// appeared beside a run with nothing saying they were the same work — and the
// interactive re-tag, which passed no parent at all, was the last path still doing that.
func TestFlushPlexParentsItsEvent(t *testing.T) {
	db := newTestDB(t)
	var hits int32
	server := plexRefreshMock(t, db, "Plex", http.StatusOK, &hits)
	r := NewRunner(db, models.ConfigStruct{AutotaggerrVersion: "test"})

	parent := events.Begin(db, models.EventTypeTagFiles, "Tag 2 attached files")
	set := refreshSetOf(server, map[string]string{
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
	plexRefreshMock(t, db, "Plex", http.StatusOK, &hits)
	r := NewRunner(db, models.ConfigStruct{AutotaggerrVersion: "test"})

	parent := events.Begin(db, models.EventTypeTagFiles, "Tag 1 attached file")
	r.flushPlex(modules.NewAlbumRefreshSet(), parent)

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
	server := plexRefreshMock(t, db, "Plex", http.StatusOK, &hits)
	r := NewRunner(db, models.ConfigStruct{AutotaggerrVersion: "test"})

	parent := events.Begin(db, models.EventTypeProcess, "Processing music")
	r.flushPlex(refreshSetOf(server, map[string]string{
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
	var hits int32
	server := plexRefreshMock(t, db, "Plex", http.StatusInternalServerError, &hits)
	r := NewRunner(db, models.ConfigStruct{AutotaggerrVersion: "test"})

	parent := events.Begin(db, models.EventTypeProcess, "Processing music")
	r.flushPlex(refreshSetOf(server, map[string]string{"Spirit of Eden": "/library/metadata/1"}), parent)

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

// TestFlushPlexSendsEachAlbumToItsServer: an album queued on two servers is two
// refreshes, each to the server it was found on, and each row then names its server.
// A server removed between the run and the flush fails its rows rather than sending
// them anywhere else.
func TestFlushPlexSendsEachAlbumToItsServer(t *testing.T) {
	db := newTestDB(t)
	var homeHits, cabinHits int32
	home := plexRefreshMock(t, db, "Home", http.StatusOK, &homeHits)
	cabin := plexRefreshMock(t, db, "Cabin", http.StatusOK, &cabinHits)
	r := NewRunner(db, models.ConfigStruct{AutotaggerrVersion: "test"})

	set := modules.NewAlbumRefreshSet()
	set.Add(home, "Spirit of Eden", "/library/metadata/1")
	set.Add(cabin, "Spirit of Eden", "/library/metadata/77")
	set.Add("gone", "Laughing Stock", "/library/metadata/2")
	r.flushPlex(set, events.Begin(db, models.EventTypeProcess, "Processing music"))

	if atomic.LoadInt32(&homeHits) != 1 || atomic.LoadInt32(&cabinHits) != 1 {
		t.Errorf("refreshes = home %d, cabin %d; want one each", homeHits, cabinHits)
	}
	var ev models.Event
	if err := db.Where("type = ?", models.EventTypePlexRefresh).First(&ev).Error; err != nil {
		t.Fatalf("no plex_refresh event: %v", err)
	}
	items, _ := events.Items(db, ev.ID)
	got := map[string]string{}
	for _, it := range items {
		got[it.Path] = it.Status
	}
	want := map[string]string{
		"Spirit of Eden · Home":  models.EventItemStatusRefreshed,
		"Spirit of Eden · Cabin": models.EventItemStatusRefreshed,
		"Laughing Stock":         models.EventItemStatusError,
	}
	for path, status := range want {
		if got[path] != status {
			t.Errorf("row %q = %q, want %q (rows: %v)", path, got[path], status, got)
		}
	}
}

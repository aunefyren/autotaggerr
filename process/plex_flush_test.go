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

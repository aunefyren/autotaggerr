package process

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
)

// historyLidarr serves /api/v1/history/since from a fixed list and remembers the
// date each request asked from, or fails every request when broken is set.
type historyLidarr struct {
	mu      sync.Mutex
	records []models.LidarrHistoryRecord
	asked   []string
	broken  bool
	server  *httptest.Server
}

func newHistoryLidarr(t *testing.T, records ...models.LidarrHistoryRecord) *historyLidarr {
	t.Helper()
	h := &historyLidarr{records: records}
	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if r.URL.Path != "/api/v1/history/since" {
			http.NotFound(w, r)
			return
		}
		if h.broken {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Query().Get("includeArtist") != "true" {
			t.Errorf("history requested without includeArtist: %s", r.URL.RawQuery)
		}
		h.asked = append(h.asked, r.URL.Query().Get("date"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(h.records)
	}))
	t.Cleanup(h.server.Close)
	return h
}

func (h *historyLidarr) requests() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.asked)
}

// newFollowRunner is a runner with a database and a queue but no worker, so a queued
// Process stays visible in r.queue instead of running.
func newFollowRunner(db *gorm.DB) *Runner {
	return &Runner{db: db, wake: make(chan struct{}, 1)}
}

func seedFollowedManager(t *testing.T, db *gorm.DB, baseURL string, cursor *time.Time) models.Manager {
	t.Helper()
	m := models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true,
		LidarrBaseURL: baseURL, LidarrAPIKey: "k", LidarrHistoryCursor: cursor}
	if err := db.Create(&m).Error; err != nil {
		t.Fatalf("create manager: %v", err)
	}
	return m
}

func followCursor(t *testing.T, db *gorm.DB, m models.Manager) *time.Time {
	t.Helper()
	var got models.Manager
	if err := db.First(&got, "id = ?", m.ID).Error; err != nil {
		t.Fatalf("reload manager: %v", err)
	}
	return got.LidarrHistoryCursor
}

func queuedKeys(r *Runner) []string {
	r.queueMu.Lock()
	defer r.queueMu.Unlock()
	keys := make([]string, 0, len(r.queue))
	for _, j := range r.queue {
		keys = append(keys, j.key)
	}
	return keys
}

func historyRecord(artistID int64, mbid, name string, eventType models.LidarrHistoryEventType, at time.Time) models.LidarrHistoryRecord {
	return models.LidarrHistoryRecord{
		ArtistID: artistID, AlbumID: artistID * 10, Date: at, EventType: eventType,
		Artist: &models.LidarrArtist{ID: artistID, ForeignArtistID: mbid, Name: name},
	}
}

// The first pass over a manager starts following it from now. Replaying its whole
// history would queue a run for every artist Lidarr ever touched.
func TestFollowLidarrStartsFromNow(t *testing.T) {
	db := newTestDB(t)
	h := newHistoryLidarr(t, historyRecord(1, "artist-1", "Artist", models.LidarrHistoryTrackFileImported, time.Now().Add(-time.Hour)))
	m := seedFollowedManager(t, db, h.server.URL, nil)
	r := newFollowRunner(db)

	r.FollowLidarr()

	if h.requests() != 0 {
		t.Errorf("history read %d times on first sight, want 0", h.requests())
	}
	if followCursor(t, db, m) == nil {
		t.Fatal("cursor not set on first sight")
	}
	if keys := queuedKeys(r); len(keys) != 0 {
		t.Errorf("queued %v on first sight, want nothing", keys)
	}
	if evs := eventsOfType(t, db, models.EventTypeLidarrFollow); len(evs) != 0 {
		t.Errorf("recorded %d events on first sight, want 0", len(evs))
	}
}

// The case the pass exists for: Lidarr upgraded an album of an artist the collection
// holds. A Process over that artist is queued, the window is recorded, and the cursor
// moves to the end of it.
func TestFollowLidarrQueuesAnArtistWhoseFilesChanged(t *testing.T) {
	db := newTestDB(t)
	seedArtistWithFile(t, db, t.TempDir())
	cursor := time.Now().Add(-time.Hour)
	settled := time.Now().Add(-10 * time.Minute)
	h := newHistoryLidarr(t,
		historyRecord(1, "artist-1", "Artist", models.LidarrHistoryTrackFileDeleted, settled),
		historyRecord(1, "artist-1", "Artist", models.LidarrHistoryTrackFileImported, settled),
	)
	m := seedFollowedManager(t, db, h.server.URL, &cursor)
	r := newFollowRunner(db)

	r.FollowLidarr()

	if keys := queuedKeys(r); len(keys) != 1 || keys[0] != "process_artist:artist-1" {
		t.Fatalf("queued %v, want one process_artist:artist-1", keys)
	}
	got := followCursor(t, db, m)
	if got == nil || !got.After(cursor) {
		t.Errorf("cursor = %v, want moved past %v", got, cursor)
	}
	if got != nil && time.Since(*got) < lidarrFollowSettle {
		t.Errorf("cursor = %v, want it held back by the settle window", got)
	}

	evs := eventsOfType(t, db, models.EventTypeLidarrFollow)
	if len(evs) != 1 {
		t.Fatalf("recorded %d events, want 1", len(evs))
	}
	var items []models.EventItem
	if err := db.Where("event_id = ?", evs[0].ID).Find(&items).Error; err != nil {
		t.Fatalf("load items: %v", err)
	}
	if len(items) != 1 || items[0].Path != "artist-1" || items[0].Status != models.EventItemStatusQueued {
		t.Errorf("items = %+v, want artist-1 queued", items)
	}
}

// What is not acted on: a record still inside the settle window (the upgrade may be
// half done — it is picked up next pass), a retag (Lidarr wrote tags; nothing moved),
// and a grab (nothing is on disk yet).
func TestFollowLidarrIgnoresUnsettledAndNonFileRecords(t *testing.T) {
	db := newTestDB(t)
	seedArtistWithFile(t, db, t.TempDir())
	cursor := time.Now().Add(-time.Hour)
	h := newHistoryLidarr(t,
		historyRecord(1, "artist-1", "Artist", models.LidarrHistoryTrackFileImported, time.Now()),
		historyRecord(1, "artist-1", "Artist", models.LidarrHistoryTrackFileRetagged, time.Now().Add(-30*time.Minute)),
		historyRecord(1, "artist-1", "Artist", models.LidarrHistoryGrabbed, time.Now().Add(-30*time.Minute)),
	)
	m := seedFollowedManager(t, db, h.server.URL, &cursor)
	r := newFollowRunner(db)

	r.FollowLidarr()

	if keys := queuedKeys(r); len(keys) != 0 {
		t.Errorf("queued %v, want nothing", keys)
	}
	if evs := eventsOfType(t, db, models.EventTypeLidarrFollow); len(evs) != 0 {
		t.Errorf("recorded %d events for a window with nothing to follow, want 0", len(evs))
	}
	got := followCursor(t, db, m)
	if got == nil || !got.After(cursor) || !got.Before(time.Now().Add(-lidarrFollowSettle+time.Second)) {
		t.Errorf("cursor = %v, want moved forward but short of the unsettled import", got)
	}
}

// An artist the collection does not hold, whose folder is in no library this manager
// governs, cannot have a run scoped to them. They are reported as skipped, with the
// reason, rather than dropped silently.
func TestFollowLidarrSkipsAnArtistItCannotFind(t *testing.T) {
	db := newTestDB(t)
	cursor := time.Now().Add(-time.Hour)
	h := newHistoryLidarr(t, historyRecord(7, "artist-new", "Newcomer", models.LidarrHistoryTrackFileImported, time.Now().Add(-10*time.Minute)))
	seedFollowedManager(t, db, h.server.URL, &cursor)
	r := newFollowRunner(db)

	r.FollowLidarr()

	if keys := queuedKeys(r); len(keys) != 0 {
		t.Errorf("queued %v, want nothing", keys)
	}
	evs := eventsOfType(t, db, models.EventTypeLidarrFollow)
	if len(evs) != 1 {
		t.Fatalf("recorded %d events, want 1", len(evs))
	}
	var items []models.EventItem
	if err := db.Where("event_id = ?", evs[0].ID).Find(&items).Error; err != nil {
		t.Fatalf("load items: %v", err)
	}
	if len(items) != 1 || items[0].Status != models.EventItemStatusSkipped || items[0].Error != followSkipNoFolder {
		t.Errorf("items = %+v, want one skipped for having no folder", items)
	}
}

// A Lidarr that cannot be read leaves the cursor where it was, so the window is read
// again next pass, and records nothing — the health check already says Lidarr is down.
func TestFollowLidarrKeepsTheCursorWhenLidarrIsDown(t *testing.T) {
	db := newTestDB(t)
	cursor := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	h := newHistoryLidarr(t)
	h.broken = true
	m := seedFollowedManager(t, db, h.server.URL, &cursor)
	r := newFollowRunner(db)

	r.FollowLidarr()

	if got := followCursor(t, db, m); got == nil || !got.Equal(cursor) {
		t.Errorf("cursor = %v, want unchanged %v", got, cursor)
	}
	if evs := eventsOfType(t, db, models.EventTypeLidarrFollow); len(evs) != 0 {
		t.Errorf("recorded %d events, want 0", len(evs))
	}
}

// A disabled manager, or one with no credentials, is not followed.
func TestFollowLidarrSkipsUnusableManagers(t *testing.T) {
	db := newTestDB(t)
	h := newHistoryLidarr(t)
	disabled := models.Manager{Name: "Off", Type: models.ManagerTypeLidarr, LidarrBaseURL: h.server.URL, LidarrAPIKey: "k"}
	noKey := models.Manager{Name: "No key", Type: models.ManagerTypeLidarr, Enabled: true, LidarrBaseURL: h.server.URL}
	for _, m := range []*models.Manager{&disabled, &noKey} {
		if err := db.Create(m).Error; err != nil {
			t.Fatalf("create manager: %v", err)
		}
	}
	// GORM writes Enabled's zero value on create only when no default overrides it;
	// make the disabled row's state explicit.
	if err := db.Model(&disabled).Update("enabled", false).Error; err != nil {
		t.Fatalf("disable manager: %v", err)
	}
	r := newFollowRunner(db)

	r.FollowLidarr()

	if followCursor(t, db, disabled) != nil || followCursor(t, db, noKey) != nil {
		t.Error("an unusable manager had its cursor set")
	}
}

// An artist's first import is the case the collection cannot answer for — nothing of
// theirs is indexed. They are found on disk by the folder name Lidarr files them
// under, in a library this manager governs, and a run is queued over that folder.
func TestFollowLidarrFindsANewArtistByFolder(t *testing.T) {
	db := newTestDB(t)
	cursor := time.Now().Add(-time.Hour)
	rec := historyRecord(7, "artist-new", "Newcomer", models.LidarrHistoryTrackFileImported, time.Now().Add(-10*time.Minute))
	rec.Artist.Path = "/data/music/Newcomer"
	h := newHistoryLidarr(t, rec)
	m := seedFollowedManager(t, db, h.server.URL, &cursor)

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Newcomer", "Debut (2026)"), 0o755); err != nil {
		t.Fatal(err)
	}
	governed := models.Library{Name: "Lidarr music", Path: root, Enabled: true, ManagerID: &m.ID}
	elsewhere := models.Library{Name: "Native", Path: t.TempDir(), Enabled: true}
	for _, l := range []*models.Library{&governed, &elsewhere} {
		if err := db.Create(l).Error; err != nil {
			t.Fatalf("create library: %v", err)
		}
	}
	r := newFollowRunner(db)

	r.FollowLidarr()

	if keys := queuedKeys(r); len(keys) != 1 || keys[0] != "process_artist:artist-new" {
		t.Fatalf("queued %v, want one process_artist:artist-new", keys)
	}
	r.queueMu.Lock()
	title := r.queue[0].title
	r.queueMu.Unlock()
	if title != "Processing Newcomer" {
		t.Errorf("job title = %q", title)
	}
}

package scan

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/database"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// newTestDB opens a fresh sqlite database in a temp dir. Each test gets its own so
// the serial queue worker never races another test's teardown.
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return db
}

// writeInvalidFlac drops a file that parses as audio by extension but fails
// correlation, so processing counts it as an error without any network access.
func writeInvalidFlac(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "01 track.flac")
	if err := os.WriteFile(p, []byte("not a real flac"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// seedArtistWithFile wires up the minimum a per-artist/-release-group scope needs to
// resolve to a folder on disk: a library, an artist, its release-group + release, and
// an indexed item pointing at an invalid FLAC.
func seedArtistWithFile(t *testing.T, db *gorm.DB, root string) (models.Library, string) {
	t.Helper()
	album := filepath.Join(root, "Artist", "Album (2020)")
	track := writeInvalidFlac(t, album)

	library := models.Library{Name: "L", Path: root, Enabled: true}
	if err := db.Create(&library).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	if err := db.Create(&models.CollectionArtist{MBID: "artist-1", Name: "Artist"}).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	if err := db.Create(&models.CollectionReleaseGroupArtist{ReleaseGroupMBID: "rg-1", ArtistMBID: "artist-1"}).Error; err != nil {
		t.Fatalf("link release-group: %v", err)
	}
	if err := db.Create(&models.CollectionReleaseGroup{MBID: "rg-1", ArtistMBID: "artist-1", Title: "Album"}).Error; err != nil {
		t.Fatalf("create release-group: %v", err)
	}
	if err := db.Create(&models.CollectionRelease{MBID: "rel-1", ReleaseGroupMBID: "rg-1", ArtistMBID: "artist-1"}).Error; err != nil {
		t.Fatalf("create release: %v", err)
	}
	if err := db.Create(&models.LibraryItem{
		LibraryID: library.ID, Path: track,
		MBReleaseID: "rel-1", Status: models.LibraryItemStatusOK,
	}).Error; err != nil {
		t.Fatalf("create item: %v", err)
	}
	return library, track
}

func TestRunLibraryScansOneLibrary(t *testing.T) {
	root := t.TempDir()
	writeInvalidFlac(t, filepath.Join(root, "Artist", "Album (2020)"))

	db := newTestDB(t)
	library := models.Library{Name: "L", Path: root, Enabled: true}
	if err := db.Create(&library).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}

	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrProcessConcurrency: 2, AutotaggerrVersion: "test"})
	if err := r.RunLibrary(library.ID); err != nil {
		t.Fatalf("RunLibrary: %v", err)
	}
	r.waitIdle(t)

	if s := r.Status(); s.Errors != 1 {
		t.Errorf("errors = %d, want 1 (the invalid flac)", s.Errors)
	}
	var ev models.Event
	if err := db.Where("type = ?", models.EventTypeProcess).First(&ev).Error; err != nil {
		t.Fatalf("scan event not recorded: %v", err)
	}
	if ev.Title != "Processing L" {
		t.Errorf("title = %q, want %q", ev.Title, "Processing L")
	}
}

func TestRunLibraryUnknownID(t *testing.T) {
	db := newTestDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})
	if err := r.RunLibrary(uuid.New()); err == nil {
		t.Error("RunLibrary with an unknown id should error before enqueueing")
	}
}

func TestForceRecorrelateArtist(t *testing.T) {
	db := newTestDB(t)
	seedArtistWithFile(t, db, t.TempDir())

	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrProcessConcurrency: 2, AutotaggerrVersion: "test"})
	if err := r.ForceRecorrelateArtist("artist-1"); err != nil {
		t.Fatalf("ForceRecorrelateArtist: %v", err)
	}
	r.waitIdle(t)

	if s := r.Status(); s.Errors != 1 {
		t.Errorf("errors = %d, want 1", s.Errors)
	}
	var ev models.Event
	if err := db.Where("type = ?", models.EventTypeProcess).First(&ev).Error; err != nil {
		t.Fatalf("scan event not recorded: %v", err)
	}
	if ev.Title != "Re-correlate Artist" {
		t.Errorf("title = %q, want %q", ev.Title, "Re-correlate Artist")
	}
}

func TestForceRecorrelateArtistUnknown(t *testing.T) {
	db := newTestDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})
	if err := r.ForceRecorrelateArtist("nope"); err == nil {
		t.Error("unknown artist should error")
	}
}

func TestForceRecorrelateReleaseGroup(t *testing.T) {
	db := newTestDB(t)
	seedArtistWithFile(t, db, t.TempDir())

	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrProcessConcurrency: 2, AutotaggerrVersion: "test"})
	if err := r.ForceRecorrelateReleaseGroup("rg-1"); err != nil {
		t.Fatalf("ForceRecorrelateReleaseGroup: %v", err)
	}
	r.waitIdle(t)

	var ev models.Event
	if err := db.Where("type = ?", models.EventTypeProcess).First(&ev).Error; err != nil {
		t.Fatalf("scan event not recorded: %v", err)
	}
	// forceTitle falls back to the kind when the release-group name is not in Detail.
	if ev.Title == "" {
		t.Error("re-correlate event has no title")
	}
}

func TestForceRecorrelateReleaseGroupUnknown(t *testing.T) {
	db := newTestDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})
	if err := r.ForceRecorrelateReleaseGroup("nope"); err == nil {
		t.Error("unknown release-group should error")
	}
}

func TestForceRecorrelateLibrary(t *testing.T) {
	root := t.TempDir()
	writeInvalidFlac(t, filepath.Join(root, "Artist", "Album (2020)"))

	db := newTestDB(t)
	library := models.Library{Name: "Main", Path: root, Enabled: true}
	if err := db.Create(&library).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}

	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrProcessConcurrency: 2, AutotaggerrVersion: "test"})
	if err := r.ForceRecorrelateLibrary(library.ID); err != nil {
		t.Fatalf("ForceRecorrelateLibrary: %v", err)
	}
	r.waitIdle(t)

	var ev models.Event
	if err := db.Where("type = ?", models.EventTypeProcess).First(&ev).Error; err != nil {
		t.Fatalf("scan event not recorded: %v", err)
	}
	if ev.Title != "Re-correlate Main" {
		t.Errorf("title = %q, want %q", ev.Title, "Re-correlate Main")
	}
}

func TestForceRecorrelateLibraryUnknown(t *testing.T) {
	db := newTestDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})
	if err := r.ForceRecorrelateLibrary(uuid.New()); err == nil {
		t.Error("unknown library should error")
	}
}

func TestForceTitle(t *testing.T) {
	if got := forceTitle("Radiohead", "artist"); got != "Re-correlate Radiohead" {
		t.Errorf("forceTitle named = %q", got)
	}
	if got := forceTitle("", "library"); got != "Re-correlate library" {
		t.Errorf("forceTitle fallback = %q", got)
	}
}

// RetagLibrary over an empty library is a clean no-op that still records the event, so
// the button never leaves the feed silent.
func TestRetagLibraryEmpty(t *testing.T) {
	db := newTestDB(t)
	library := models.Library{Name: "L", Path: t.TempDir(), Enabled: true}
	if err := db.Create(&library).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}

	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})
	r.RetagLibrary(library.ID)
	r.waitIdle(t)

	var ev models.Event
	if err := db.Where("type = ?", models.EventTypeDriftSync).First(&ev).Error; err != nil {
		t.Fatalf("re-tag event not recorded: %v", err)
	}
	if ev.Status != models.EventStatusOK || ev.FinishedAt == nil {
		t.Errorf("event should finish ok: %+v", ev)
	}
	if ev.Details["library"] != "L" {
		t.Errorf("event details lost the library: %#v", ev.Details)
	}
}

// TestRetagAllCoversEveryEnabledLibrary: Tag files at collection scope is one job
// over every enabled library, not one per library — so it records a single event, and
// a disabled library is not in it.
func TestRetagAllCoversEveryEnabledLibrary(t *testing.T) {
	db := newTestDB(t)
	for _, lib := range []models.Library{
		{Name: "A", Path: t.TempDir(), Enabled: true},
		{Name: "B", Path: t.TempDir(), Enabled: true},
		{Name: "Off", Path: t.TempDir(), Enabled: false},
	} {
		if err := db.Create(&lib).Error; err != nil {
			t.Fatalf("create library %s: %v", lib.Name, err)
		}
	}

	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})
	r.RetagAll()
	r.waitIdle(t)

	var events []models.Event
	if err := db.Where("type = ?", models.EventTypeDriftSync).Find(&events).Error; err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("recorded %d events, want 1 — the collection-wide re-tag is one job", len(events))
	}
	names, _ := events[0].Details["libraries"].([]any)
	if len(names) != 2 {
		t.Errorf("libraries in scope = %v, want the two enabled ones", events[0].Details["libraries"])
	}
}

// An unknown library is a warned no-op: the enqueued job runs, finds nothing, and the
// queue drains without a panic or a recorded event.
func TestRetagLibraryUnknown(t *testing.T) {
	db := newTestDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})
	r.RetagLibrary(uuid.New())
	r.waitIdle(t)

	var count int64
	db.Model(&models.Event{}).Where("type = ?", models.EventTypeDriftSync).Count(&count)
	if count != 0 {
		t.Errorf("unknown library recorded %d re-tag events, want 0", count)
	}
}

func TestRetagItemsEmptyAndUnknown(t *testing.T) {
	db := newTestDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})

	if results, err := r.RetagItems(nil); err != nil || results != nil {
		t.Errorf("RetagItems(nil) = %v, %v; want nil, nil", results, err)
	}

	id := uuid.New()
	results, err := r.RetagItems([]uuid.UUID{id})
	if err != nil {
		t.Fatalf("RetagItems: %v", err)
	}
	if len(results) != 1 || results[0].Err == nil {
		t.Errorf("unknown item should yield a per-item error: %#v", results)
	}

	if _, err := r.RetagItem(id); err == nil {
		t.Error("RetagItem with unknown id should return the load error")
	}
}

// TestRetagItemsNoWriteProfileIsANoop drives a real indexed item through the re-tag
// verb with a no-write tagger profile, so retagItem resolves the library and tagger and
// then returns early (no file write, no MusicBrainz call). This covers the interactive
// re-tag path — the item lookup, the per-item result and the nil-Plex flush — without a
// file on disk or a network round-trip.
func TestRetagItemsNoWriteProfileIsANoop(t *testing.T) {
	db := newTestDB(t)

	profile := models.TaggerProfile{Name: "NoWrite", WriteTags: false}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("create profile: %v", err)
	}
	lib := models.Library{Name: "L", Path: t.TempDir(), Enabled: true, TaggerProfileID: &profile.ID}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	item := models.LibraryItem{
		LibraryID: lib.ID, Path: filepath.Join(lib.Path, "01.flac"),
		MBReleaseID: "rel-1", MBReleaseTrackID: "trk-1", MBRecordingID: "rec-1",
		Status: models.LibraryItemStatusOK,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("create item: %v", err)
	}

	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})

	results, err := r.RetagItems([]uuid.UUID{item.ID})
	if err != nil {
		t.Fatalf("RetagItems: %v", err)
	}
	if len(results) != 1 || results[0].Err != nil || results[0].Written != 0 {
		t.Errorf("no-write RetagItems = %#v, want one result: written 0, no error", results)
	}

	if written, err := r.RetagItem(item.ID); err != nil || written != 0 {
		t.Errorf("RetagItem = %d, %v; want 0, nil", written, err)
	}
}

// RefreshArtist/RefreshLibrary for an unresolvable scope are warned no-ops: the job
// runs, the scope resolution fails, and nothing is fetched. This covers the enqueue +
// early-return path without touching MusicBrainz.
// Wait blocks until the queue has drained. On an idle runner it returns at once; after
// an enqueued no-op run it returns once the worker has finished it.
func TestWaitDrainsQueue(t *testing.T) {
	db := newTestDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})

	r.Wait() // idle: returns immediately

	r.RunAll() // no libraries -> a quick no-op job
	r.Wait()
	if r.Running() {
		t.Error("Wait returned while a job was still running")
	}
}

func TestRefresherExposesMetadataRunner(t *testing.T) {
	r := NewRunner(newTestDB(t), nil, models.ConfigStruct{AutotaggerrVersion: "test"})
	if r.Refresher() == nil {
		t.Error("Refresher should expose the metadata runner")
	}
}

func TestRefreshVerbsUnknownScope(t *testing.T) {
	db := newTestDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})

	r.RefreshArtist("no-such-artist")
	r.waitIdle(t)
	r.RefreshLibrary(uuid.New())
	r.waitIdle(t)

	if r.Running() {
		t.Error("runner should be idle after both refresh no-ops")
	}
}

// TestScopeIsFull: the end-of-run manager mirror is earned by covering a whole
// library. A per-artist or per-release-group run is an interactive action, and
// SyncLidarr has no scope narrower than "every Lidarr artist in the collection" — so a
// one-album button must not wait on a whole-collection mirror.
func TestScopeIsFull(t *testing.T) {
	lib := models.Library{Name: "L", Path: "/m"}

	cases := []struct {
		name  string
		scope Scope
		want  bool
	}{
		{"whole library", Scope{Targets: []Target{{Library: lib}}}, true},
		{"one artist folder", Scope{Targets: []Target{{Library: lib, Roots: []string{"/m/Artist"}}}}, false},
		{"nothing to scan", Scope{}, false},
		{
			name: "one full library among narrowed ones",
			scope: Scope{Targets: []Target{
				{Library: lib, Roots: []string{"/m/Artist"}},
				{Library: lib},
			}},
			want: true,
		},
	}
	for _, c := range cases {
		if got := scopeIsFull(c.scope); got != c.want {
			t.Errorf("%s: scopeIsFull = %v, want %v", c.name, got, c.want)
		}
	}
}

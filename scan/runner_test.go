package scan

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/database"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/sirupsen/logrus"
)

func init() {
	logger.Log = logrus.New()
	logger.Log.SetOutput(io.Discard)
}

// waitIdle blocks until the queue has drained and no job is executing. The trigger
// verbs are asynchronous now (they enqueue), so a test must wait for the worker before
// asserting on the result — and before its temp DB is torn down under a running job.
func (r *Runner) waitIdle(t *testing.T) {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		r.queueMu.Lock()
		idle := r.current == nil && len(r.queue) == 0
		r.queueMu.Unlock()
		if idle && !r.running.Load() {
			return
		}
		select {
		case <-deadline:
			t.Fatal("queue did not drain within 30s")
		case <-time.After(2 * time.Millisecond):
		}
	}
}

func TestRunnerRunAll(t *testing.T) {
	root := t.TempDir()
	albumDir := filepath.Join(root, "Artist", "Album (2020)")
	if err := os.MkdirAll(albumDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// An invalid FLAC: correlation fails, so the file is counted as an error and the
	// run completes without touching the network.
	if err := os.WriteFile(filepath.Join(albumDir, "01 track.flac"), []byte("not a real flac"), 0o644); err != nil {
		t.Fatal(err)
	}

	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := db.Create(&models.Library{Name: "L", Path: root, Enabled: true}).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}

	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrProcessConcurrency: 2, AutotaggerrVersion: "test"})
	r.RunAll()
	r.waitIdle(t)

	s := r.Status()
	if s.Running {
		t.Error("runner should be idle after RunAll returns")
	}
	if s.FinishedAt == nil || s.StartedAt == nil {
		t.Error("start/finish timestamps not set")
	}
	if s.Errors != 1 {
		t.Errorf("errors = %d, want 1 (the invalid flac)", s.Errors)
	}

	// The scan should have been recorded as an Activity event.
	var ev models.Event
	if err := db.Where("type = ?", models.EventTypeScan).First(&ev).Error; err != nil {
		t.Fatalf("scan event not recorded: %v", err)
	}
	if ev.Status != models.EventStatusError || ev.FinishedAt == nil {
		t.Errorf("scan event should be finished with error status: %+v", ev)
	}
	if errs, ok := ev.Details["errors"].(float64); !ok || errs != 1 {
		t.Errorf("event details errors = %#v, want 1", ev.Details["errors"])
	}

	// The flusher should have counted the one file and left the final progress on the
	// row: total==done==1, ending in the migrations phase.
	if ev.Total != 1 || ev.Done != 1 {
		t.Errorf("event progress total/done = %d/%d, want 1/1", ev.Total, ev.Done)
	}
	if ev.Phase != PhaseMigrations {
		t.Errorf("event ended in phase %q, want %q", ev.Phase, PhaseMigrations)
	}
}

func TestArtistFromPath(t *testing.T) {
	root := filepath.FromSlash("/music")
	cases := []struct {
		path, want string
	}{
		{filepath.FromSlash("/music/Radiohead/OK Computer (1997)/01 Airbag.flac"), "Radiohead"},
		{filepath.FromSlash("/music/Various Artists/Comp (2001)/01 One.mp3"), "Various Artists"},
		{filepath.FromSlash("/elsewhere/Artist/Album/1.flac"), ""}, // not under root
		{root, ""}, // the root itself has no artist segment
	}
	for _, c := range cases {
		if got := artistFromPath(root, c.path); got != c.want {
			t.Errorf("artistFromPath(%q, %q) = %q, want %q", root, c.path, got, c.want)
		}
	}
}

// SyncDrift is now the refresh verb at collection scope: it records a metadata
// refresh event and, critically, writes no files.
func TestRunnerSyncDriftEmitsRefreshEvent(t *testing.T) {
	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})
	r.SyncDrift() // empty collection -> a clean no-op refresh
	r.waitIdle(t)

	var ev models.Event
	if err := db.Where("type = ?", models.EventTypeMirror).First(&ev).Error; err != nil {
		t.Fatalf("metadata refresh event not recorded: %v", err)
	}
	if ev.Status != models.EventStatusOK || ev.FinishedAt == nil {
		t.Errorf("refresh event should finish ok: %+v", ev)
	}
	if fetched, ok := ev.Details["fetched"].(float64); !ok || fetched != 0 {
		t.Errorf("fetched = %#v, want 0", ev.Details["fetched"])
	}
	// The point of the split: a refresh must never rewrite audio files.
	if retagged, present := ev.Details["files_retagged"]; present {
		t.Errorf("a refresh reported file writes (%#v) — it must not touch files", retagged)
	}
}

func TestRunnerNoLibraries(t *testing.T) {
	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})
	r.RunAll() // no libraries -> no-op, no panic
	r.waitIdle(t)
	if r.Running() {
		t.Error("runner should be idle")
	}
}

// The per-artist actions. What is worth pinning is the resolution step in front of
// them — an artist with no files must be refused rather than run as an empty scan —
// and that a partial scan does not claim to have scanned the whole library.

func TestArtistScopeRefusesArtistWithoutFiles(t *testing.T) {
	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := db.Create(&models.CollectionArtist{MBID: "artist-1", Name: "Nobody"}).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})

	if _, err := r.ArtistScope("artist-1"); !errors.Is(err, ErrNothingToScan) {
		t.Errorf("err = %v, want ErrNothingToScan", err)
	}
	if _, err := r.ArtistScope("no-such-artist"); err == nil {
		t.Error("unknown artist should error")
	}
}

// A scan narrowed to one artist walks that artist's folder and leaves the rest of
// the library alone — including the library's last_scan timestamp, which would
// otherwise claim the whole library had just been read.
func TestRunArtistScansOnlyTheArtistFolder(t *testing.T) {
	root := t.TempDir()
	wanted := filepath.Join(root, "Artist", "Album (2020)")
	other := filepath.Join(root, "Someone Else", "Album (2021)")
	for _, dir := range []string{wanted, other} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		// Invalid FLACs: correlation fails, so each processed file lands in the error
		// count without any network access. That count is the assertion.
		if err := os.WriteFile(filepath.Join(dir, "01 track.flac"), []byte("not a real flac"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
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
	if err := db.Create(&models.CollectionRelease{MBID: "rel-1", ReleaseGroupMBID: "rg-1", ArtistMBID: "artist-1"}).Error; err != nil {
		t.Fatalf("create release: %v", err)
	}
	if err := db.Create(&models.LibraryItem{
		LibraryID: library.ID, Path: filepath.Join(wanted, "01 track.flac"),
		MBReleaseID: "rel-1", Status: models.LibraryItemStatusOK,
	}).Error; err != nil {
		t.Fatalf("create item: %v", err)
	}

	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrProcessConcurrency: 2, AutotaggerrVersion: "test"})
	if err := r.RunArtist("artist-1"); err != nil {
		t.Fatalf("RunArtist: %v", err)
	}
	r.waitIdle(t)

	if s := r.Status(); s.Errors != 1 {
		t.Errorf("errors = %d, want 1 — the other artist's file should not have been walked", s.Errors)
	}

	var after models.Library
	if err := db.First(&after, "id = ?", library.ID).Error; err != nil {
		t.Fatalf("reload library: %v", err)
	}
	if after.LastScan != nil {
		t.Error("a partial scan must not claim the library was scanned")
	}

	var ev models.Event
	if err := db.Where("type = ?", models.EventTypeScan).First(&ev).Error; err != nil {
		t.Fatalf("scan event not recorded: %v", err)
	}
	if ev.Title != "Scan of Artist" {
		t.Errorf("title = %q, want the artist's name", ev.Title)
	}
	if ev.Details["artist"] != "Artist" {
		t.Errorf("event details lost the scope: %#v", ev.Details)
	}
}

// A whole-library run still stamps last_scan — the counterpart to the assertion
// above, so narrowing the scope did not quietly disable the timestamp for everyone.
func TestRunAllStampsLastScan(t *testing.T) {
	root := t.TempDir()
	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	library := models.Library{Name: "L", Path: root, Enabled: true}
	if err := db.Create(&library).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}

	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})
	r.RunAll()
	r.waitIdle(t)

	var after models.Library
	if err := db.First(&after, "id = ?", library.ID).Error; err != nil {
		t.Fatalf("reload library: %v", err)
	}
	if after.LastScan == nil {
		t.Error("a full library scan should record last_scan")
	}
}

// RetagArtist with nothing to write is a clean no-op that still reports itself, so
// pressing the button never leaves the Activity feed silent.
func TestRetagArtistEmitsEvent(t *testing.T) {
	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := db.Create(&models.CollectionArtist{MBID: "artist-1", Name: "Artist"}).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})
	r.RetagArtist("artist-1")
	r.waitIdle(t)

	var ev models.Event
	if err := db.Where("type = ?", models.EventTypeDriftSync).First(&ev).Error; err != nil {
		t.Fatalf("re-tag event not recorded: %v", err)
	}
	if ev.Status != models.EventStatusOK || ev.FinishedAt == nil {
		t.Errorf("event should finish ok: %+v", ev)
	}
	if ev.Details["artist"] != "Artist" {
		t.Errorf("event details lost the artist: %#v", ev.Details)
	}
}

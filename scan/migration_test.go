package scan

import (
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/database"
	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/models"
)

// The queue is drained at a run boundary rather than where a redirect is detected,
// so this covers the wiring: a pending migration sitting in the database is applied
// by the next run, using the policy from the process config.
func TestApplyMigrationsDrainsTheQueue(t *testing.T) {
	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	lib := models.Library{Name: "L", Path: t.TempDir(), Enabled: true}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	if err := db.Create(&models.LibraryItem{
		LibraryID: lib.ID, Path: "/m/a.flac", Status: models.LibraryItemStatusOK, MBReleaseID: "rel-old",
	}).Error; err != nil {
		t.Fatalf("create item: %v", err)
	}
	if err := db.Create(&models.MusicbrainzMigration{
		EntityType: models.MigrationEntityRelease,
		OldMBID:    "rel-old",
		NewMBID:    "rel-new",
		Kind:       models.MigrationKindRedirect,
		Status:     models.MigrationStatusPending,
	}).Error; err != nil {
		t.Fatalf("create migration: %v", err)
	}

	original := files.ConfigFile
	files.ConfigFile = models.ConfigStruct{}
	t.Cleanup(func() { files.ConfigFile = original })

	runner := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})
	res := runner.applyMigrations()

	if res.Applied != 1 || res.Files != 1 {
		t.Errorf("result = %+v, want 1 applied / 1 file remapped", res)
	}

	var item models.LibraryItem
	if err := db.First(&item).Error; err != nil {
		t.Fatalf("find item: %v", err)
	}
	if item.MBReleaseID != "rel-new" {
		t.Errorf("item release = %q, want rel-new", item.MBReleaseID)
	}
}

// A run with nothing queued must not report migration activity, or every scan
// summary would grow a line about work that did not happen.
func TestApplyMigrationsQuietWhenNothingPending(t *testing.T) {
	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	runner := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})
	res := runner.applyMigrations()
	if res.Applied != 0 || res.Pending != 0 || res.Failed != 0 {
		t.Errorf("result = %+v, want an empty result", res)
	}

	// And the summary line stays as it was when there is nothing to say.
	summary := releaseRefresh{checked: 3}.summary()
	if want := "3 releases checked · 0 changed · 0 files re-tagged · 0 errors"; summary != want {
		t.Errorf("summary = %q, want %q", summary, want)
	}
}

// The sweep on an empty collection must still complete and record itself, rather
// than returning silently — "nothing to check" is a result the user asked for.
// The identity check is the refresh verb at collection scope with the cache
// ignored, so it records a metadata-refresh event distinguished by its title —
// the same way LibraryScope and ArtistScope both emit "scan".
func TestVerifyIdentitiesRecordsAFullRefreshEvent(t *testing.T) {
	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	original := files.ConfigFile
	files.ConfigFile = models.ConfigStruct{}
	t.Cleanup(func() { files.ConfigFile = original })

	runner := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})
	runner.VerifyIdentities()
	runner.waitIdle(t)

	var ev models.Event
	if err := db.Where("type = ?", models.EventTypeMirror).First(&ev).Error; err != nil {
		t.Fatalf("no metadata refresh event recorded: %v", err)
	}
	if ev.Status != models.EventStatusOK || ev.FinishedAt == nil {
		t.Errorf("event status = %q, finished = %v", ev.Status, ev.FinishedAt)
	}
	if ev.Title != "Full metadata refresh" {
		t.Errorf("title = %q — the feed has to tell a forced sweep from the nightly pass", ev.Title)
	}
	if ev.Details["fetched"] == nil {
		t.Errorf("event details = %+v, want the fetch counts", ev.Details)
	}

	// And the guard is released, so a sweep does not wedge every later run.
	if runner.Running() {
		t.Error("the run guard was left held")
	}
}

// When something did happen, the summary says so — the counts are otherwise
// invisible, since a merge changes no file content.
func TestSummaryReportsMigrations(t *testing.T) {
	res := releaseRefresh{checked: 10, goneReleases: 1}
	res.migrations.Applied = 2
	res.migrations.Pending = 1

	got := res.summary()
	want := "10 releases checked · 0 changed · 0 files re-tagged · 0 errors · 2 migrations applied · 1 awaiting review"
	if got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

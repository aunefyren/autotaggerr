package events

import (
	"errors"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// closedDB is a database whose connection has gone away: every query fails.
func closedDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return db
}

// failWrites makes every UPDATE (and, if deletes is set, every DELETE) on db fail
// while reads keep working — the partial failure a reconcile or prune has to survive
// after it has already enumerated its rows.
func failWrites(t *testing.T, db *gorm.DB, deletes bool) {
	t.Helper()
	fail := func(tx *gorm.DB) { _ = tx.AddError(errors.New("injected write failure")) }
	if err := db.Callback().Update().Before("gorm:update").Register("test:fail_update", fail); err != nil {
		t.Fatal(err)
	}
	if deletes {
		if err := db.Callback().Delete().Before("gorm:delete").Register("test:fail_delete", fail); err != nil {
			t.Fatal(err)
		}
	}
}

// TestNilDatabaseIsANoOp: the one-shot file mode records nothing, and every emitter
// has to accept that rather than make each caller branch on it.
func TestNilDatabaseIsANoOp(t *testing.T) {
	ev := Begin(nil, models.EventTypeProcess, "run")
	if ev == nil || ev.Status != models.EventStatusRunning {
		t.Fatalf("Begin(nil) = %+v, want an in-memory running event", ev)
	}

	writeProgress(nil, nil, Progress{Total: 1})
	writeProgress(nil, ev, Progress{Total: 3, Done: 1, Phase: "p", Current: "c"})
	if ev.Total != 3 || ev.Done != 1 || ev.Phase != "p" || ev.Current != "c" {
		t.Errorf("writeProgress without a database still has to update the struct, got %+v", ev)
	}

	Finish(nil, nil, models.EventStatusOK, "", nil)
	Finish(nil, ev, models.EventStatusOK, "done", nil)
	if ev.Status != models.EventStatusOK || ev.FinishedAt == nil {
		t.Errorf("Finish without a database still has to update the struct, got %+v", ev)
	}

	if rows, err := Children(nil, uuid.New()); rows != nil || err != nil {
		t.Errorf("Children(nil) = %v, %v", rows, err)
	}
	if rows, err := Children(testDB(t), uuid.Nil); rows != nil || err != nil {
		t.Errorf("Children(nil id) = %v, %v", rows, err)
	}
	if items, err := Items(nil, uuid.New()); len(items) != 0 || err != nil {
		t.Errorf("Items(nil) = %v, %v", items, err)
	}
	ReconcileRunning(nil)
	MigrateLegacyTypes(nil)
	Prune(nil, 10)
	ResolveRefs(nil, []models.EventItem{{Kind: models.EventItemKindEntity, Path: "x"}})
}

// TestFailingDatabaseNeverPanics: emitting is best-effort by contract, so a database
// that has gone away is logged and the work carries on.
func TestFailingDatabaseNeverPanics(t *testing.T) {
	db := closedDB(t)

	ev := Begin(db, models.EventTypeProcess, "run")
	if ev == nil {
		t.Fatal("Begin on a failing database must still return an event")
	}
	// Pretend it was persisted so the write paths are reached.
	ev.ID = uuid.New()
	writeProgress(db, ev, Progress{Total: 1})
	AddItems(db, ev, []models.EventItem{{Path: "a.flac"}})
	Finish(db, ev, models.EventStatusOK, "done", nil)

	if _, err := Children(db, ev.ID); err == nil {
		t.Error("Children on a closed database should return the error")
	}
	ReconcileRunning(db)
	MigrateLegacyTypes(db)
	Prune(db, 1)

	items := []models.EventItem{{Kind: models.EventItemKindEntity, Path: "some-mbid"}}
	ResolveRefs(db, items)
	if items[0].Related != nil {
		t.Errorf("nothing could be read, yet Related = %+v", items[0].Related)
	}
}

// TestReconcileSurvivesFailedUpdates: the reconcile has already enumerated what was
// running when the writes fail; it must log and leave the rows for the next boot
// rather than panic or report them closed.
func TestReconcileSurvivesFailedUpdates(t *testing.T) {
	db := testDB(t)
	run := Begin(db, models.EventTypeProcess, "run")
	BeginChild(db, run, models.EventTypeTagFiles, "Tagging")
	Begin(db, models.EventTypeProcess, "a run with no stage")
	failWrites(t, db, false)

	ReconcileRunning(db)
	MigrateLegacyTypes(db)

	var running int64
	db.Model(&models.Event{}).Where("status = ?", models.EventStatusRunning).Count(&running)
	if running != 3 {
		t.Errorf("%d events still running, want all 3 left alone by the failed writes", running)
	}
}

// TestPruneSurvivesFailedDeletes: a prune that cannot delete keeps the rows rather
// than half-removing a run.
func TestPruneSurvivesFailedDeletes(t *testing.T) {
	db := testDB(t)
	for i := 0; i < 3; i++ {
		ev := Begin(db, models.EventTypeProcess, "run")
		AddItems(db, ev, []models.EventItem{{Path: "a.flac"}})
	}
	failWrites(t, db, true)

	Prune(db, 1)

	var events int64
	db.Model(&models.Event{}).Count(&events)
	if events != 3 {
		t.Errorf("%d events left, want all 3 kept when deletes fail", events)
	}
}

// TestResolveRefsFallsBackToTheMigrationName: an identifier the collection no longer
// holds is named from the migration row that remembered it, with the kind translated
// into the detail rows' vocabulary.
func TestResolveRefsFallsBackToTheMigrationName(t *testing.T) {
	db := testDB(t)
	for _, m := range []models.MusicbrainzMigration{
		{EntityType: models.MigrationEntityArtist, OldMBID: "gone-artist", Name: "Old Artist", Kind: models.MigrationKindDeleted, Status: models.MigrationStatusApplied},
		{EntityType: models.MigrationEntityReleaseGroup, OldMBID: "gone-group", Name: "Old Album", Kind: models.MigrationKindDeleted, Status: models.MigrationStatusApplied},
		{EntityType: models.MigrationEntityRelease, OldMBID: "gone-release", Name: "Old Edition", Kind: models.MigrationKindDeleted, Status: models.MigrationStatusApplied},
	} {
		if err := db.Create(&m).Error; err != nil {
			t.Fatalf("create migration: %v", err)
		}
	}

	items := []models.EventItem{
		{Kind: models.EventItemKindEntity, Path: "gone-artist"},
		{Kind: models.EventItemKindEntity, Path: "gone-group"},
		{Kind: models.EventItemKindEntity, Path: "gone-release"},
	}
	ResolveRefs(db, items)

	want := []struct{ kind, name, artist string }{
		{models.EntityKindArtist, "Old Artist", "gone-artist"},
		{models.EntityKindReleaseGroup, "Old Album", ""},
		{models.EntityKindRelease, "Old Edition", ""},
	}
	for i, w := range want {
		ref := items[i].Related
		if ref == nil {
			t.Errorf("%s: no ref", items[i].Path)
			continue
		}
		if ref.Kind != w.kind || ref.Name != w.name || ref.ArtistMBID != w.artist {
			t.Errorf("%s: ref = %+v, want kind %s name %q artist %q", items[i].Path, ref, w.kind, w.name, w.artist)
		}
	}
}

// TestResolveRefsIgnoresFileRows: with no entity rows there is nothing to look up.
func TestResolveRefsIgnoresFileRows(t *testing.T) {
	db := closedDB(t) // would log if it were queried
	items := []models.EventItem{{Kind: models.EventItemKindFile, Path: "a.flac"}}
	ResolveRefs(db, items)
	ResolveRefs(db, nil)
	if items[0].Related != nil {
		t.Error("a file row gained a ref")
	}
}

// TestReconcileWithNothingRunning: the normal boot. A finished event is not touched.
func TestReconcileWithNothingRunning(t *testing.T) {
	db := testDB(t)
	ev := Begin(db, models.EventTypeProcess, "run")
	Finish(db, ev, models.EventStatusOK, "done", nil)

	ReconcileRunning(db)

	var row models.Event
	if err := db.First(&row, "id = ?", ev.ID).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != models.EventStatusOK || row.Summary != "done" {
		t.Errorf("a finished event was rewritten: %q / %q", row.Status, row.Summary)
	}
}

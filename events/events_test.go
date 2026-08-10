package events

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/database"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func init() {
	logger.Log = logrus.New()
	logger.Log.SetOutput(io.Discard)
}

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return db
}

func TestBeginFinish(t *testing.T) {
	db := testDB(t)

	ev := Begin(db, models.EventTypeProcess, "Library scan")
	if ev.ID == uuid.Nil {
		t.Fatal("Begin did not assign an id")
	}
	if ev.Status != models.EventStatusRunning {
		t.Errorf("status = %q, want running", ev.Status)
	}

	Finish(db, ev, models.EventStatusOK, "3 processed", map[string]any{"changed": 2})

	var got models.Event
	if err := db.First(&got, "id = ?", ev.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Status != models.EventStatusOK || got.FinishedAt == nil {
		t.Errorf("event not finished: %+v", got)
	}
	if got.Summary != "3 processed" {
		t.Errorf("summary = %q", got.Summary)
	}
	// JSON numbers decode as float64.
	if v, ok := got.Details["changed"].(float64); !ok || v != 2 {
		t.Errorf("details did not round-trip: %#v", got.Details)
	}
}

// TestStartProgressPersistsFinalSnapshot covers the two things the flusher promises:
// stop() lands the latest snapshot on the row, and a Finish that follows does not
// erase it (Finish Saves the struct, which writeProgress keeps in sync).
func TestStartProgressPersistsFinalSnapshot(t *testing.T) {
	db := testDB(t)
	ev := Begin(db, models.EventTypeProcess, "Library scan")

	var done int
	stop := StartProgress(db, ev, func() Progress {
		done++
		return Progress{Total: 100, Done: done, Phase: "scanning", Current: "Radiohead"}
	})
	stop() // synchronous final write

	var got models.Event
	if err := db.First(&got, "id = ?", ev.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Total != 100 || got.Done == 0 || got.Phase != "scanning" || got.Current != "Radiohead" {
		t.Errorf("progress not persisted: total=%d done=%d phase=%q current=%q", got.Total, got.Done, got.Phase, got.Current)
	}

	// Finish must not reset the progress fields it does not touch.
	Finish(db, ev, models.EventStatusOK, "done", nil)
	if err := db.First(&got, "id = ?", ev.ID).Error; err != nil {
		t.Fatalf("reload after finish: %v", err)
	}
	if got.Total != 100 || got.Phase != "scanning" {
		t.Errorf("Finish erased progress: total=%d phase=%q", got.Total, got.Phase)
	}
}

// A nil db (the flag-driven single-file path builds events without one) must yield a
// no-op stop rather than panicking.
func TestStartProgressNilDB(t *testing.T) {
	ev := Begin(nil, models.EventTypeProcess, "scan")
	stop := StartProgress(nil, ev, func() Progress { return Progress{} })
	stop() // must not panic
}

// TestReconcileRunning covers the restart cleanup: running events are closed as
// failed, while already-finished events are left untouched.
func TestReconcileRunning(t *testing.T) {
	db := testDB(t)
	running := Begin(db, models.EventTypeProcess, "interrupted scan")
	done := Begin(db, models.EventTypeProcess, "finished scan")
	Finish(db, done, models.EventStatusOK, "done", nil)

	ReconcileRunning(db)

	var gotRunning models.Event
	if err := db.First(&gotRunning, "id = ?", running.ID).Error; err != nil {
		t.Fatalf("reload running: %v", err)
	}
	if gotRunning.Status != models.EventStatusError || gotRunning.FinishedAt == nil {
		t.Errorf("interrupted event not closed: status=%q finished=%v", gotRunning.Status, gotRunning.FinishedAt)
	}
	if gotRunning.Summary == "" {
		t.Error("interrupted event should carry an explanatory summary")
	}

	// The already-finished event must be left exactly as it was.
	var gotDone models.Event
	if err := db.First(&gotDone, "id = ?", done.ID).Error; err != nil {
		t.Fatalf("reload done: %v", err)
	}
	if gotDone.Status != models.EventStatusOK || gotDone.Summary != "done" {
		t.Errorf("finished event was altered: %+v", gotDone)
	}
}

// TestReconcileRunningNamesTheStage covers the half a run cannot report itself: the
// stage that was in flight lives on a different row, so without this the feed says a
// run was interrupted but not what it was doing. The stage's own row needs no help —
// its title already says — so it keeps the plain summary.
func TestReconcileRunningNamesTheStage(t *testing.T) {
	db := testDB(t)
	run := Begin(db, models.EventTypeProcess, "interrupted run")
	stage := BeginChild(db, run, models.EventTypeTagFiles, "Tagging")

	// A run that crashed between stages has nothing to name, and a finished stage is
	// not what the run died in — neither may reach the run's summary.
	quiet := Begin(db, models.EventTypeProcess, "run with no stage open")
	Finish(db, BeginChild(db, run, models.EventTypeCountFiles, "Counting files"),
		models.EventStatusOK, "done", nil)

	ReconcileRunning(db)

	var gotRun models.Event
	if err := db.First(&gotRun, "id = ?", run.ID).Error; err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if !strings.Contains(gotRun.Summary, "Tagging") {
		t.Errorf("run summary should name the stage it died in, got %q", gotRun.Summary)
	}
	if strings.Contains(gotRun.Summary, "Counting files") {
		t.Errorf("run summary named a stage that had already finished: %q", gotRun.Summary)
	}
	if gotRun.Status != models.EventStatusError || gotRun.FinishedAt == nil {
		t.Errorf("run not closed: status=%q finished=%v", gotRun.Status, gotRun.FinishedAt)
	}

	var gotStage models.Event
	if err := db.First(&gotStage, "id = ?", stage.ID).Error; err != nil {
		t.Fatalf("reload stage: %v", err)
	}
	if gotStage.Status != models.EventStatusError || gotStage.FinishedAt == nil {
		t.Errorf("stage not closed: status=%q finished=%v", gotStage.Status, gotStage.FinishedAt)
	}
	if strings.Contains(gotStage.Summary, "during") {
		t.Errorf("stage should keep the plain summary, got %q", gotStage.Summary)
	}

	var gotQuiet models.Event
	if err := db.First(&gotQuiet, "id = ?", quiet.ID).Error; err != nil {
		t.Fatalf("reload quiet run: %v", err)
	}
	if gotQuiet.Status != models.EventStatusError || strings.Contains(gotQuiet.Summary, "during") {
		t.Errorf("run with no open stage should get the plain summary: status=%q summary=%q",
			gotQuiet.Status, gotQuiet.Summary)
	}
}

func TestPruneKeepsNewest(t *testing.T) {
	db := testDB(t)
	for i := 0; i < 5; i++ {
		Finish(db, Begin(db, models.EventTypeProcess, "scan"), models.EventStatusOK, "", nil)
	}
	Prune(db, 2)

	var n int64
	if err := db.Model(&models.Event{}).Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("after prune count = %d, want 2", n)
	}
}

// TestAddItemsAndFetch covers the per-file detail round trip: rows are stamped with
// their parent event, the field-level diff survives JSON serialization, and they come
// back for that event only.
func TestAddItemsAndFetch(t *testing.T) {
	db := testDB(t)
	ev := Begin(db, models.EventTypeProcess, "scan")
	other := Begin(db, models.EventTypeProcess, "another scan")

	AddItems(db, ev, []models.EventItem{
		{
			Path:        "/music/A/Album (2020)/01 One.flac",
			Status:      models.EventItemStatusChanged,
			TagsWritten: 2,
			Changes: []models.TagChange{
				{Field: "ARTIST", Old: "Old Name", New: "New Name"},
				{Field: "DATE", Old: "", New: "2020"},
			},
		},
		{Path: "/music/A/Album (2020)/02 Two.flac", Status: models.EventItemStatusError, Error: "boom"},
	})
	AddItems(db, other, []models.EventItem{{Path: "/elsewhere.flac", Status: models.EventItemStatusChanged}})

	items, err := Items(db, ev.ID)
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2 (the other event's row must not leak in)", len(items))
	}

	var changed, failed *models.EventItem
	for i := range items {
		switch items[i].Status {
		case models.EventItemStatusChanged:
			changed = &items[i]
		case models.EventItemStatusError:
			failed = &items[i]
		}
	}
	if changed == nil || failed == nil {
		t.Fatalf("expected one changed and one error row, got %+v", items)
	}
	if changed.EventID != ev.ID {
		t.Errorf("event_id = %v, want %v", changed.EventID, ev.ID)
	}
	if changed.TagsWritten != 2 || len(changed.Changes) != 2 {
		t.Fatalf("changed row lost detail: %+v", changed)
	}
	if changed.Changes[0].Field != "ARTIST" || changed.Changes[0].Old != "Old Name" || changed.Changes[0].New != "New Name" {
		t.Errorf("diff did not round-trip: %+v", changed.Changes[0])
	}
	// An absent previous value stays empty rather than becoming a literal.
	if changed.Changes[1].Old != "" || changed.Changes[1].New != "2020" {
		t.Errorf("added-field diff wrong: %+v", changed.Changes[1])
	}
	if failed.Error != "boom" {
		t.Errorf("error row lost its message: %+v", failed)
	}
}

// TestAddItemsIgnoresUnsavedEvent guards the DB-less paths: a nil DB or an event that
// was never persisted must be a no-op, not a panic or a row with a nil parent.
func TestAddItemsIgnoresUnsavedEvent(t *testing.T) {
	db := testDB(t)
	items := []models.EventItem{{Path: "/x.flac", Status: models.EventItemStatusChanged}}

	AddItems(nil, &models.Event{}, items)     // no DB
	AddItems(db, nil, items)                  // no event
	AddItems(db, &models.Event{}, items)      // event never saved (zero ID)
	AddItems(db, Begin(db, "scan", "s"), nil) // nothing to add

	var n int64
	if err := db.Model(&models.EventItem{}).Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("wrote %d rows, want 0", n)
	}
}

// TestPruneDeletesDetailRows is the retention half. Nothing in the schema cascades,
// so without an explicit delete the events table would stay capped at 200 while
// event_items grew forever — orphans no feed would ever show.
func TestPruneDeletesDetailRows(t *testing.T) {
	db := testDB(t)

	var kept, dropped uuid.UUID
	for i := 0; i < 4; i++ {
		ev := Begin(db, models.EventTypeProcess, "scan")
		Finish(db, ev, models.EventStatusOK, "", nil)
		AddItems(db, ev, []models.EventItem{{Path: "/f.flac", Status: models.EventItemStatusChanged}})
		if i == 0 {
			dropped = ev.ID // oldest
		}
		kept = ev.ID // newest wins the loop
	}

	Prune(db, 1)

	var events int64
	if err := db.Model(&models.Event{}).Count(&events).Error; err != nil {
		t.Fatalf("count events: %v", err)
	}
	if events != 1 {
		t.Fatalf("events after prune = %d, want 1", events)
	}

	var orphans int64
	if err := db.Model(&models.EventItem{}).Where("event_id = ?", dropped).Count(&orphans).Error; err != nil {
		t.Fatalf("count orphans: %v", err)
	}
	if orphans != 0 {
		t.Errorf("%d detail rows survived their pruned event", orphans)
	}

	// The surviving event keeps its own detail.
	survivors, err := Items(db, kept)
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if len(survivors) != 1 {
		t.Errorf("kept event has %d detail rows, want 1", len(survivors))
	}
}

// TestMigrateLegacyTypes: runs recorded before the verbs were named apart carry the
// old type, and the feed would otherwise show them under a verb that now means
// something else entirely. Other types are left alone, and a second call is a no-op.
func TestMigrateLegacyTypes(t *testing.T) {
	db := testDB(t)

	old := Begin(db, models.EventTypeLegacyScan, "an old run")
	walk := Begin(db, models.EventTypeProcessFiles, "an old walk")
	other := Begin(db, models.EventTypeMirror, "a refresh")

	MigrateLegacyTypes(db)

	var migrated models.Event
	if err := db.First(&migrated, "id = ?", old.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if migrated.Type != models.EventTypeProcess {
		t.Errorf("type = %q, want %q", migrated.Type, models.EventTypeProcess)
	}

	// The walk was a tagging pass under another name. Leaving it would put two entries
	// in the feed's type filter for one kind of work. (Its own variable: a reloaded
	// struct carries its primary key into the next First as an extra condition.)
	var migratedWalk models.Event
	if err := db.First(&migratedWalk, "id = ?", walk.ID).Error; err != nil {
		t.Fatalf("reload the walk: %v", err)
	}
	if migratedWalk.Type != models.EventTypeTagFiles {
		t.Errorf("walk type = %q, want %q", migratedWalk.Type, models.EventTypeTagFiles)
	}

	var untouched models.Event
	if err := db.First(&untouched, "id = ?", other.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if untouched.Type != models.EventTypeMirror {
		t.Errorf("an unrelated event type was rewritten: %q", untouched.Type)
	}

	// Idempotent: nothing left to rename on the next boot.
	MigrateLegacyTypes(db)
	var legacy int64
	db.Model(&models.Event{}).
		Where("type IN ?", []string{models.EventTypeLegacyScan, models.EventTypeProcessFiles}).
		Count(&legacy)
	if legacy != 0 {
		t.Errorf("%d legacy rows remain", legacy)
	}
}

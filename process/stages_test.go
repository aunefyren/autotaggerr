package process

import (
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/database"
	"github.com/aunefyren/autotaggerr/models"
)

// stageTypes returns the types of the stage events recorded under a run, so a test can
// assert what a run reported without depending on the order two stages happened to
// finish in.
func stageTypes(t *testing.T, r *Runner, runID any) map[string]int {
	t.Helper()
	var stages []models.Event
	if err := r.db.Where("parent_id = ?", runID).Find(&stages).Error; err != nil {
		t.Fatalf("load stages: %v", err)
	}
	byType := map[string]int{}
	for _, s := range stages {
		byType[s.Type]++
	}
	return byType
}

// A run does several distinct things and used to report as one row, so five of them
// had nowhere to put their counters. Each stage now records its own event under the
// run — which is what makes "what did this run actually do" answerable.
func TestARunRecordsItsStages(t *testing.T) {
	root := t.TempDir()
	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := db.Create(&models.Library{Name: "L", Path: root, Enabled: true}).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}

	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})
	r.RunAll()
	r.waitIdle(t)

	var run models.Event
	if err := db.Where("type = ? AND parent_id IS NULL", models.EventTypeProcess).First(&run).Error; err != nil {
		t.Fatalf("load the run: %v", err)
	}

	stages := stageTypes(t, r, run.ID)
	// Counting, the metadata refresh, tagging and the collection scan happen on every
	// run, however empty the library is. Plex and migrations are conditional and are
	// deliberately not asserted here.
	for _, want := range []string{
		models.EventTypeCountFiles,
		models.EventTypeMirror,
		models.EventTypeTagFiles,
		models.EventTypeCollectionScan,
	} {
		if stages[want] == 0 {
			t.Errorf("run recorded no %q stage; got %+v", want, stages)
		}
	}
	// Tagging is one activity however a run reached the files. Two rows is what put the
	// walk's counters next to a row whose only content was release IDs the metadata
	// stage had already listed.
	if stages[models.EventTypeProcessFiles] > 0 {
		t.Errorf("run recorded a separate walk stage; tagging is one activity: %+v", stages)
	}
}

// The run's own row stops carrying the walk's per-file detail: every row belongs to the
// stage that produced it, and duplicating them onto the parent would show the same file
// twice to anyone opening the run.
func TestTheRunItselfCarriesNoFileDetail(t *testing.T) {
	root := t.TempDir()
	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := db.Create(&models.Library{Name: "L", Path: root, Enabled: true}).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}

	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})
	r.RunAll()
	r.waitIdle(t)

	var run models.Event
	if err := db.Where("type = ? AND parent_id IS NULL", models.EventTypeProcess).First(&run).Error; err != nil {
		t.Fatalf("load the run: %v", err)
	}
	var rows int64
	db.Model(&models.EventItem{}).Where("event_id = ?", run.ID).Count(&rows)
	if rows != 0 {
		t.Errorf("the run row carries %d detail row(s); they belong to its stages", rows)
	}
}

// Draining an empty migration queue is one indexed query. A row per run saying
// "0 applied" would bury the runs that actually re-pointed a record, which is the
// opposite of what putting identity changes in the feed is for.
func TestAnEmptyMigrationQueueRecordsNothing(t *testing.T) {
	root := t.TempDir()
	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := db.Create(&models.Library{Name: "L", Path: root, Enabled: true}).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}

	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})
	r.RunAll()
	r.waitIdle(t)

	var migrations int64
	db.Model(&models.Event{}).Where("type = ?", models.EventTypeMigration).Count(&migrations)
	if migrations != 0 {
		t.Errorf("recorded %d identity-change event(s) with nothing to apply", migrations)
	}
}

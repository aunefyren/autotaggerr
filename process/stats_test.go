package process

import (
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/database"
	"github.com/aunefyren/autotaggerr/models"
)

// statByLabel finds a declared counter, so a test can assert on one without depending
// on the order an emitter happens to list them in.
func statByLabel(stats []models.EventStat, label string) (models.EventStat, bool) {
	for _, s := range stats {
		if s.Label == label {
			return s, true
		}
	}
	return models.EventStat{}, false
}

// The detail view used to be a hardcoded branch per event type, so a new type rendered
// as a raw JSON blob until someone wrote it one. An emitter declares its own counters
// now — which means every event a run produces must actually carry some.
func TestEveryEventARunProducesDeclaresItsCounters(t *testing.T) {
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

	var rows []models.Event
	if err := db.Find(&rows).Error; err != nil {
		t.Fatalf("load events: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("the run recorded nothing at all")
	}
	for _, ev := range rows {
		if len(ev.Stats) == 0 {
			t.Errorf("event %q (%s) declares no counters, so its detail view has nothing to show", ev.Title, ev.Type)
		}
	}
}

// A counter that names an EventItem status becomes a chip over the detail list — that
// is what turns "12 changed" into a way to see which twelve. The link is the status
// string, so it has to match what the rows are actually written with.
func TestChangedCounterSelectsTheRowsItCounts(t *testing.T) {
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

	var tagging models.Event
	if err := db.Where("type = ? AND parent_id IS NOT NULL", models.EventTypeTagFiles).First(&tagging).Error; err != nil {
		t.Fatalf("load the tagging event: %v", err)
	}

	// "Files changed", not "Changed": the row mixes files with tags written, so each
	// counter names its unit (see style-guide.md, Voice & copy).
	changed, ok := statByLabel(tagging.Stats, "Files changed")
	if !ok {
		t.Fatalf("tagging declares no Files changed counter: %+v", tagging.Stats)
	}
	if changed.Filter != models.EventItemStatusChanged {
		t.Errorf("Files changed selects %q, want %q — a filter that matches no row is a dead chip",
			changed.Filter, models.EventItemStatusChanged)
	}

	failed, ok := statByLabel(tagging.Stats, "Failed")
	if !ok || failed.Kind != models.EventStatBad {
		t.Errorf("Failed counter = %+v, want it marked bad", failed)
	}
}

// The run rolls up stages that do not share a unit, so its counters must not claim to
// select rows: there is no single list behind "12 files changed and 3 releases
// refreshed", and the run carries no detail rows at all.
func TestTheRunsRollupCountersSelectNothing(t *testing.T) {
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
	for _, s := range run.Stats {
		if s.Filter != "" {
			t.Errorf("run counter %q selects %q, but the run holds no rows to select", s.Label, s.Filter)
		}
	}
}

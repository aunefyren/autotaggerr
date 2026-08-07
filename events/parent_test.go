package events

import (
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// A stage records itself under the run that performed it, which is what lets the feed
// list runs and still reach everything a run did.
func TestBeginChildLinksToItsRun(t *testing.T) {
	db := testDB(t)

	run := Begin(db, models.EventTypeProcess, "Process all libraries")
	stage := BeginChild(db, run, models.EventTypeMirror, "Metadata refresh")

	if stage.ParentID == nil || *stage.ParentID != run.ID {
		t.Fatalf("stage parent = %v, want the run %s", stage.ParentID, run.ID)
	}

	children, err := Children(db, run.ID)
	if err != nil {
		t.Fatalf("Children: %v", err)
	}
	if len(children) != 1 || children[0].ID != stage.ID {
		t.Fatalf("children = %+v, want just the stage", children)
	}
}

// Every emitter here is best-effort by contract. A stage whose parent could not be
// written must still report itself rather than vanishing with it.
func TestBeginChildWithoutAParentIsTopLevel(t *testing.T) {
	db := testDB(t)

	stage := BeginChild(db, nil, models.EventTypeCollectionScan, "Collection scan")
	if stage.ParentID != nil {
		t.Errorf("parent = %v, want top-level", stage.ParentID)
	}

	unsaved := &models.Event{} // never persisted, so its ID is the zero UUID
	orphan := BeginChild(db, unsaved, models.EventTypeCollectionScan, "Collection scan")
	if orphan.ParentID != nil {
		t.Errorf("parent = %v — a parent that was never written is not a parent", orphan.ParentID)
	}
}

// Retention counts runs, not rows. A run emits a row per stage, so counting rows would
// cut history by however many stages the runs happened to have.
func TestPruneKeepsRunsNotRows(t *testing.T) {
	db := testDB(t)

	// Three runs of four stages each: twelve rows, three runs.
	var runs []*models.Event
	for i := 0; i < 3; i++ {
		run := Begin(db, models.EventTypeProcess, "Process all libraries")
		for j := 0; j < 4; j++ {
			BeginChild(db, run, models.EventTypeCollectionScan, "Collection scan")
		}
		runs = append(runs, run)
	}

	Prune(db, 2)

	var topLevel int64
	db.Model(&models.Event{}).Where("parent_id IS NULL").Count(&topLevel)
	if topLevel != 2 {
		t.Errorf("kept %d runs, want 2", topLevel)
	}

	// The oldest run went, and took its stages with it rather than orphaning them.
	var orphaned int64
	db.Model(&models.Event{}).Where("parent_id = ?", runs[0].ID).Count(&orphaned)
	if orphaned != 0 {
		t.Errorf("%d stage(s) outlived their run", orphaned)
	}

	// The surviving runs kept theirs.
	var kept int64
	db.Model(&models.Event{}).Where("parent_id = ?", runs[2].ID).Count(&kept)
	if kept != 4 {
		t.Errorf("newest run kept %d stages, want 4", kept)
	}
}

// A run finishes by pruning, and several of its stages prune as they close. Counting
// rows would let a long run delete its own earlier stages out from under itself.
func TestPruneDoesNotDropTheRunningRunsOwnStages(t *testing.T) {
	db := testDB(t)

	for i := 0; i < 3; i++ {
		Begin(db, models.EventTypeProcess, "an older run")
	}
	run := Begin(db, models.EventTypeProcess, "Process all libraries")
	for j := 0; j < 6; j++ {
		BeginChild(db, run, models.EventTypeCollectionScan, "Collection scan")
		// Each stage prunes as it finishes, exactly as the real emitters do.
		Prune(db, 2)
	}

	var kept int64
	db.Model(&models.Event{}).Where("parent_id = ?", run.ID).Count(&kept)
	if kept != 6 {
		t.Errorf("the running run kept %d of its own 6 stages", kept)
	}
}

// Detail rows belong to whichever event produced them, so pruning a run has to take
// the rows of its stages too — nothing in the schema cascades.
func TestPruneDropsDetailRowsOfDroppedStages(t *testing.T) {
	db := testDB(t)

	old := Begin(db, models.EventTypeProcess, "an older run")
	stage := BeginChild(db, old, models.EventTypeProcessFiles, "Processing files")
	AddItems(db, stage, []models.EventItem{{Path: "/music/a.flac", Status: models.EventItemStatusChanged}})

	Begin(db, models.EventTypeProcess, "a newer run")
	Prune(db, 1)

	var rows int64
	db.Model(&models.EventItem{}).Where("event_id = ?", stage.ID).Count(&rows)
	if rows != 0 {
		t.Errorf("%d detail row(s) survived the stage they belonged to", rows)
	}
}

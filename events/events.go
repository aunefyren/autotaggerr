// Package events records what the app does (scans, refreshes, ...) into the Event
// table that backs the Activity feed. Emitting an event is best-effort: a failure
// to record is logged, never propagated, so it can't break the work it describes.
package events

import (
	"sync"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// progressFlushInterval is how often a running event's live progress is written to
// the database. It is a compromise: often enough that a feed watching a long job
// sees it move, rare enough that a scan touching thousands of files does not turn
// into thousands of UPDATEs. The per-item hot path never writes — StartProgress
// polls the job's own counters on this tick.
const progressFlushInterval = 2 * time.Second

// Begin creates a running top-level event and returns it. The caller finishes it
// with Finish.
func Begin(db *gorm.DB, evType, title string) *models.Event {
	return begin(db, nil, evType, title)
}

// BeginChild creates a running event owned by parent — one stage of a run.
//
// A nil parent, or one that was never persisted, yields a top-level event rather
// than an error: every emitter here is best-effort by contract, and a stage that
// silently vanished because its parent could not be written would be worse than one
// that reports itself on its own.
func BeginChild(db *gorm.DB, parent *models.Event, evType, title string) *models.Event {
	return begin(db, parent, evType, title)
}

func begin(db *gorm.DB, parent *models.Event, evType, title string) *models.Event {
	ev := &models.Event{
		Type:      evType,
		Status:    models.EventStatusRunning,
		StartedAt: time.Now(),
		Title:     title,
	}
	if parent != nil && parent.ID != uuid.Nil {
		id := parent.ID
		ev.ParentID = &id
	}
	if db == nil {
		return ev
	}
	if err := db.Create(ev).Error; err != nil {
		logger.Log.Warnf("failed to record event: %s", err.Error())
	}
	return ev
}

// Children loads the stage events belonging to one run, oldest first so they read as
// the order things happened in.
func Children(db *gorm.DB, parentID uuid.UUID) ([]models.Event, error) {
	if db == nil || parentID == uuid.Nil {
		return nil, nil
	}
	var rows []models.Event
	if err := db.Where("parent_id = ?", parentID).Order("started_at asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// Progress is one live snapshot of a running job. Total/Done drive the feed's
// progress bar; Phase names the current stage; Current is what is being worked on
// right now (a library name, an artist).
type Progress struct {
	Total, Done    int
	Phase, Current string
}

// writeProgress records a snapshot onto the event, both in memory and in the
// database. Setting the fields on the struct as well as the row matters: Finish
// later calls Save(ev), which would write the struct's zero values back over the
// row and erase the last progress if the struct had never been updated.
func writeProgress(db *gorm.DB, ev *models.Event, p Progress) {
	if ev == nil {
		return
	}
	ev.Total, ev.Done, ev.Phase, ev.Current = p.Total, p.Done, p.Phase, p.Current
	if db == nil || ev.ID == uuid.Nil {
		return
	}
	updates := map[string]any{
		"total": p.Total, "done": p.Done, "phase": p.Phase, "current_item": p.Current,
	}
	if err := db.Model(ev).Updates(updates).Error; err != nil {
		logger.Log.Warnf("failed to record event progress: %s", err.Error())
	}
}

// StartProgress launches a background flusher that writes the event's progress on a
// ticker until the returned stop func is called. `snapshot` is polled, not pushed:
// it reads the job's own live counters (guarded by the job), so the per-item hot
// path never touches the database. stop() waits for the ticker goroutine to quit
// before its final write, so the last snapshot is the value left on the row — and it
// must be called before Finish, which then Saves that value rather than racing it.
//
// A nil db or an unpersisted event yields a no-op stop: the caller wires this in
// unconditionally and need not special-case the db-less test path.
func StartProgress(db *gorm.DB, ev *models.Event, snapshot func() Progress) (stop func()) {
	if db == nil || ev == nil || ev.ID == uuid.Nil || snapshot == nil {
		return func() {}
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(progressFlushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				writeProgress(db, ev, snapshot())
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			<-stopped
			writeProgress(db, ev, snapshot())
		})
	}
}

// Finish marks an event complete with a final status, one-line summary, and a
// type-specific details payload.
func Finish(db *gorm.DB, ev *models.Event, status, summary string, details map[string]any) {
	if ev == nil {
		return
	}
	now := time.Now()
	ev.Status = status
	ev.Summary = summary
	ev.FinishedAt = &now
	ev.Details = details
	if db == nil || ev.ID == uuid.Nil {
		return
	}
	if err := db.Save(ev).Error; err != nil {
		logger.Log.Warnf("failed to update event: %s", err.Error())
	}
}

// AddItems stores an event's per-file detail rows (see models.EventItem). The rows
// are written in one batch after the work finishes rather than as each file is
// processed: a scan would otherwise interleave thousands of small inserts with the
// tag writes it is timing. Best-effort, like the rest of this package — losing the
// detail must never fail the scan that produced it.
func AddItems(db *gorm.DB, ev *models.Event, items []models.EventItem) {
	if db == nil || ev == nil || ev.ID == uuid.Nil || len(items) == 0 {
		return
	}
	for i := range items {
		items[i].EventID = ev.ID
	}
	if err := db.CreateInBatches(items, 200).Error; err != nil {
		logger.Log.Warnf("failed to record %d event detail rows: %s", len(items), err.Error())
	}
}

// Items returns an event's per-file detail rows, oldest first (the order they were
// processed in).
func Items(db *gorm.DB, eventID uuid.UUID) ([]models.EventItem, error) {
	var items []models.EventItem
	if db == nil {
		return items, nil
	}
	err := db.Where("event_id = ?", eventID).Order("created_at, path").Find(&items).Error
	return items, err
}

// ReconcileRunning closes out events left in the running state by a previous process.
// A running event whose owning process is gone can never finish itself, so on startup
// — before any new work begins — every such row is marked failed and stamped finished.
// Without this an interrupted scan (or refresh, or sweep) shows as "running" in the
// feed forever, and nothing distinguishes it from a live one.
//
// Startup-only by contract: it must run before the process starts any job of its own,
// because it cannot tell "a previous process left this" from "this process just began
// it". The caller places it ahead of every auto-start and schedule.
func ReconcileRunning(db *gorm.DB) {
	if db == nil {
		return
	}
	now := time.Now()
	res := db.Model(&models.Event{}).
		Where("status = ?", models.EventStatusRunning).
		Updates(map[string]any{
			"status":      models.EventStatusError,
			"finished_at": now,
			"summary":     "interrupted — the service restarted while this was running",
		})
	if res.Error != nil {
		logger.Log.Warnf("failed to reconcile interrupted events: %s", res.Error.Error())
		return
	}
	if res.RowsAffected > 0 {
		logger.Log.Infof("marked %d interrupted event(s) as failed on startup", res.RowsAffected)
	}
}

// MigrateLegacyTypes rewrites event rows recorded under a type name that has since
// been renamed. Today that is one: the full pipeline was recorded as "scan" before
// Scan came to mean the collection re-derivation, so old rows would otherwise sit in
// the feed under a verb that no longer describes what they did — and the feed's type
// filter would offer two entries for the same thing.
//
// It runs at startup beside ReconcileRunning, and is a no-op once the rows are
// rewritten: events are capped at a few hundred, so the pass costs one indexed update
// against an empty set on every boot after the first.
func MigrateLegacyTypes(db *gorm.DB) {
	if db == nil {
		return
	}
	res := db.Model(&models.Event{}).
		Where("type = ?", models.EventTypeLegacyScan).
		Update("type", models.EventTypeProcess)
	if res.Error != nil {
		logger.Log.Warnf("failed to migrate legacy event types: %s", res.Error.Error())
		return
	}
	if res.RowsAffected > 0 {
		logger.Log.Infof("renamed %d legacy scan event(s) to the process verb", res.RowsAffected)
	}
}

// Prune keeps only the newest `keep` **runs**, deleting the rest along with their
// stage events and every detail row belonging to either. Retention runs after each
// recorded action so the tables stay bounded.
//
// It counts top-level events, not rows. A run emits one row per stage it performed,
// so counting rows would silently cut history by however many stages the runs
// happened to have — and worse, would let a long run prune its own earlier stages
// out from under itself, since several stages call this as they finish.
//
// The child rows are deleted explicitly: nothing in the schema cascades, so without
// this the events table would stay capped while event_items grew without limit —
// orphaned rows that no feed would ever show again.
func Prune(db *gorm.DB, keep int) {
	if db == nil || keep < 1 {
		return
	}
	var ids []uuid.UUID
	if err := db.Model(&models.Event{}).Where("parent_id IS NULL").
		Order("started_at desc").Offset(keep).Pluck("id", &ids).Error; err != nil {
		logger.Log.Warnf("failed to enumerate events for pruning: %s", err.Error())
		return
	}
	if len(ids) == 0 {
		return
	}

	// A dropped run takes its stages with it. Collected before the delete, because
	// afterwards there is nothing left to find them by.
	var childIDs []uuid.UUID
	if err := db.Model(&models.Event{}).Where("parent_id IN ?", ids).Pluck("id", &childIDs).Error; err != nil {
		logger.Log.Warnf("failed to enumerate stage events for pruning: %s", err.Error())
	}
	ids = append(ids, childIDs...)

	if err := db.Where("event_id IN ?", ids).Delete(&models.EventItem{}).Error; err != nil {
		logger.Log.Warnf("failed to prune event detail rows: %s", err.Error())
	}
	if err := db.Where("id IN ?", ids).Delete(&models.Event{}).Error; err != nil {
		logger.Log.Warnf("failed to prune events: %s", err.Error())
	}
}

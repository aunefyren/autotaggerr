// Package events records what the app does (scans, refreshes, ...) into the Event
// table that backs the Activity feed. Emitting an event is best-effort: a failure
// to record is logged, never propagated, so it can't break the work it describes.
package events

import (
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Begin creates a running event and returns it. The caller finishes it with Finish.
func Begin(db *gorm.DB, evType, title string) *models.Event {
	ev := &models.Event{
		Type:      evType,
		Status:    models.EventStatusRunning,
		StartedAt: time.Now(),
		Title:     title,
	}
	if db == nil {
		return ev
	}
	if err := db.Create(ev).Error; err != nil {
		logger.Log.Warnf("failed to record event: %s", err.Error())
	}
	return ev
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

// Prune keeps only the newest `keep` events, deleting the rest along with their
// detail rows. Retention runs after each recorded action so the tables stay bounded.
//
// The child rows are deleted explicitly: nothing in the schema cascades, so without
// this the events table would stay capped while event_items grew without limit —
// orphaned rows that no feed would ever show again.
func Prune(db *gorm.DB, keep int) {
	if db == nil || keep < 1 {
		return
	}
	var ids []uuid.UUID
	if err := db.Model(&models.Event{}).Order("started_at desc").Offset(keep).Pluck("id", &ids).Error; err != nil {
		logger.Log.Warnf("failed to enumerate events for pruning: %s", err.Error())
		return
	}
	if len(ids) == 0 {
		return
	}
	if err := db.Where("event_id IN ?", ids).Delete(&models.EventItem{}).Error; err != nil {
		logger.Log.Warnf("failed to prune event detail rows: %s", err.Error())
	}
	if err := db.Where("id IN ?", ids).Delete(&models.Event{}).Error; err != nil {
		logger.Log.Warnf("failed to prune events: %s", err.Error())
	}
}

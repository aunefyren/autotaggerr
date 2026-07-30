package routers

import (
	"net/http"

	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/gin-gonic/gin"
)

const (
	defaultEventsLimit = 50
	maxEventsLimit     = 200
)

// listEvents returns the Activity feed, newest first, filterable by type/status.
func (a *API) listEvents(c *gin.Context) {
	q := a.DB.Model(&models.Event{})
	if t := c.Query("type"); t != "" {
		q = q.Where("type = ?", t)
	}
	if s := c.Query("status"); s != "" {
		q = q.Where("status = ?", s)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to count events"})
		return
	}

	limit := parseIntDefault(c.Query("limit"), defaultEventsLimit)
	if limit < 1 {
		limit = defaultEventsLimit
	}
	if limit > maxEventsLimit {
		limit = maxEventsLimit
	}
	offset := parseIntDefault(c.Query("offset"), 0)
	if offset < 0 {
		offset = 0
	}

	var rows []models.Event
	if err := q.Order("started_at desc").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list events"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"total": total, "limit": limit, "offset": offset, "events": rows})
}

func (a *API) getEvent(c *gin.Context) {
	id, ok := a.idParam(c)
	if !ok {
		return
	}
	var ev models.Event
	if err := a.DB.First(&ev, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "event not found"})
		return
	}

	// The per-file detail rows come with the single-event fetch, not the feed: they
	// are the reason to open an event, and a list of 50 events would otherwise carry
	// thousands of them. Attached to the event rather than returned alongside it, so
	// the response shape stays what it was.
	items, err := events.Items(a.DB, ev.ID)
	if err != nil {
		logger.Log.Warnf("failed to load detail rows for event %s: %s", ev.ID, err.Error())
	}
	ev.Items = items

	c.JSON(http.StatusOK, ev)
}

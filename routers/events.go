package routers

import (
	"net/http"

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
	c.JSON(http.StatusOK, ev)
}

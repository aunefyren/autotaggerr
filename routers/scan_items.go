package routers

import (
	"net/http"
	"strconv"

	"github.com/aunefyren/autotaggerr/components"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	defaultItemsLimit = 100
	maxItemsLimit     = 500
)

// triggerScan starts a full scan of all enabled libraries in the background.
func (a *API) triggerScan(c *gin.Context) {
	if a.Scan == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "scanner unavailable"})
		return
	}
	if a.Scan.Running() {
		c.JSON(http.StatusConflict, gin.H{"error": "a scan is already running"})
		return
	}
	go a.Scan.RunAll()
	c.JSON(http.StatusAccepted, gin.H{"status": "scan started"})
}

// scanLibrary starts a background scan of a single library.
func (a *API) scanLibrary(c *gin.Context) {
	if a.Scan == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "scanner unavailable"})
		return
	}
	id, ok := a.idParam(c)
	if !ok {
		return
	}
	var lib models.Library
	if err := a.DB.First(&lib, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "library not found"})
		return
	}
	if a.Scan.Running() {
		c.JSON(http.StatusConflict, gin.H{"error": "a scan is already running"})
		return
	}
	go func() {
		if err := a.Scan.RunLibrary(id); err != nil {
			logger.Log.Error("failed to scan library. error: " + err.Error())
		}
	}()
	c.JSON(http.StatusAccepted, gin.H{"status": "scan started"})
}

// triggerSync starts a background metadata sync: re-check due MusicBrainz releases
// and re-tag files whose release changed upstream.
func (a *API) triggerSync(c *gin.Context) {
	if a.Scan == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "scanner unavailable"})
		return
	}
	if a.Scan.Running() {
		c.JSON(http.StatusConflict, gin.H{"error": "a scan or sync is already running"})
		return
	}
	go a.Scan.SyncDrift()
	c.JSON(http.StatusAccepted, gin.H{"status": "sync started"})
}

// scanStatus reports the current or most recent scan summary.
func (a *API) scanStatus(c *gin.Context) {
	if a.Scan == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "scanner unavailable"})
		return
	}
	c.JSON(http.StatusOK, a.Scan.Status())
}

// listLibraryItems returns a paginated, filterable view of the correlation index.
// Filters: library_id, status, and q (path substring). Pagination: limit/offset.
func (a *API) listLibraryItems(c *gin.Context) {
	q := a.DB.Model(&models.LibraryItem{})

	if raw := c.Query("library_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid library_id"})
			return
		}
		q = q.Where("library_id = ?", id)
	}
	if status := c.Query("status"); status != "" {
		q = q.Where("status = ?", status)
	}
	if search := c.Query("q"); search != "" {
		q = q.Where("path LIKE ?", "%"+search+"%")
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to count items"})
		return
	}

	limit := parseIntDefault(c.Query("limit"), defaultItemsLimit)
	if limit < 1 {
		limit = defaultItemsLimit
	}
	if limit > maxItemsLimit {
		limit = maxItemsLimit
	}
	offset := parseIntDefault(c.Query("offset"), 0)
	if offset < 0 {
		offset = 0
	}

	var items []models.LibraryItem
	if err := q.Order("path").Limit(limit).Offset(offset).Find(&items).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list items"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"total":  total,
		"limit":  limit,
		"offset": offset,
		"items":  items,
	})
}

// itemTags returns the current-vs-desired tag diff for one indexed file.
func (a *API) itemTags(c *gin.Context) {
	id, ok := a.idParam(c)
	if !ok {
		return
	}
	var item models.LibraryItem
	if err := a.DB.First(&item, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "item not found"})
		return
	}
	tags, err := components.ComputeItemDiff(a.DB, item)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"item": item, "tags": tags})
}

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

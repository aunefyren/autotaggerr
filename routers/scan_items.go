package routers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/components"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/scan"
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

// The per-artist actions.
//
// All three are narrowed versions of work the app already does to a whole library,
// and they exist because the whole library is the wrong unit for a fix: noticing one
// artist is wrong should not cost a seven-hour cold scan. They differ in what they
// re-read, cheapest first:
//
//	retag   — rewrite tags from correlations already stored. No disk walk, no network.
//	refresh — re-read the artist's catalogue and editions from MusicBrainz, re-tag
//	          what changed upstream. No disk walk.
//	scan    — walk the artist's folders through the full pipeline. Finds new,
//	          changed and never-correlated files.
//
// Each reports its progress through the same scan status and Activity feed as a full
// run, because each *is* a full run with a narrower scope.

// artistAction resolves the artist and the shared preconditions of all three
// actions, then starts work in the background. It returns the artist so callers can
// name it in their response.
func (a *API) artistAction(c *gin.Context) (models.CollectionArtist, bool) {
	var artist models.CollectionArtist
	if a.Scan == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "scanner unavailable"})
		return artist, false
	}
	if err := a.DB.Where("mb_id = ?", c.Param("mbid")).First(&artist).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "artist not found"})
		return artist, false
	}
	// Checked here as well as inside the runner: the runner drops an overlapping run
	// silently (right for a cron job), whereas a user who pressed a button needs to be
	// told why nothing happened.
	if a.Scan.Running() {
		c.JSON(http.StatusConflict, gin.H{"error": "a scan or sync is already running"})
		return artist, false
	}
	return artist, true
}

// scanArtist walks just this artist's folders. The scope is resolved before
// responding so "this artist has no files yet" is an answer, not a scan that reports
// zero files a minute later.
func (a *API) scanArtist(c *gin.Context) {
	artist, ok := a.artistAction(c)
	if !ok {
		return
	}
	scope, err := a.Scan.ArtistScope(artist.MBID)
	if err != nil {
		if errors.Is(err, scan.ErrNothingToScan) {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		logger.Log.Error("failed to resolve artist scan scope. error: " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to resolve what to scan"})
		return
	}
	go a.Scan.Run(scope)
	c.JSON(http.StatusAccepted, gin.H{"status": "scan started", "artist": artist.Name, "folders": scope.Detail["folders"]})
}

// refreshArtist re-reads this artist's catalogue and editions from MusicBrainz,
// ignoring the cache TTL, and re-tags the files of anything that changed.
func (a *API) refreshArtist(c *gin.Context) {
	artist, ok := a.artistAction(c)
	if !ok {
		return
	}
	go a.Scan.RefreshArtist(artist.MBID)
	c.JSON(http.StatusAccepted, gin.H{"status": "refresh started", "artist": artist.Name})
}

// retagArtist rewrites this artist's indexed files from their stored correlations.
func (a *API) retagArtist(c *gin.Context) {
	artist, ok := a.artistAction(c)
	if !ok {
		return
	}
	items, err := collection.ArtistItemIDs(a.DB, artist.MBID)
	if err != nil {
		logger.Log.Error("failed to load artist items. error: " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to resolve what to tag"})
		return
	}
	if len(items) == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "no indexed files for this artist — run a scan first"})
		return
	}
	go a.Scan.RetagArtist(artist.MBID)
	c.JSON(http.StatusAccepted, gin.H{"status": "tagging started", "artist": artist.Name, "files": len(items)})
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

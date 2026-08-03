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
	"gorm.io/gorm"
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
	// No busy check: overlapping requests queue rather than 409, and an identical scan
	// already queued or running is collapsed by the runner's dedup.
	a.Scan.RunAll()
	c.JSON(http.StatusAccepted, gin.H{"status": "scan queued"})
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
	if err := a.Scan.RunLibrary(id); err != nil {
		logger.Log.Error("failed to queue library scan. error: " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to queue the scan"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": "scan queued"})
}

// triggerSync starts a background metadata sync: re-check due MusicBrainz releases
// and re-tag files whose release changed upstream.
func (a *API) triggerSync(c *gin.Context) {
	if a.Scan == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "scanner unavailable"})
		return
	}
	a.Scan.SyncDrift()
	c.JSON(http.StatusAccepted, gin.H{"status": "sync queued"})
}

// refreshLibrary and retagLibrary are the library-scoped halves of the verb grid:
// the same two actions the artist page offers, aimed at one library instead.
func (a *API) refreshLibrary(c *gin.Context) {
	lib, ok := a.libraryAction(c)
	if !ok {
		return
	}
	a.Scan.RefreshLibrary(lib.ID)
	c.JSON(http.StatusAccepted, gin.H{"status": "refresh queued", "library": lib.Name})
}

func (a *API) retagLibrary(c *gin.Context) {
	lib, ok := a.libraryAction(c)
	if !ok {
		return
	}

	// Still refused when there is nothing to tag — a "0 files tagged" event would look
	// like the action silently failed — but no longer refused for a running job: it
	// queues behind whatever is ahead of it.
	var count int64
	a.DB.Model(&models.LibraryItem{}).
		Where("library_id = ? AND status = ?", lib.ID, models.LibraryItemStatusOK).Count(&count)
	if count == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "no indexed files in this library — run a scan first"})
		return
	}

	a.Scan.RetagLibrary(lib.ID)
	c.JSON(http.StatusAccepted, gin.H{"status": "tagging queued", "library": lib.Name, "files": count})
}

// libraryAction resolves the library a scoped action targets, answering the shared
// failure cases so each handler is only about its own verb.
//
// Only the file-writing verbs are gated on a running scan. A metadata refresh is
// not — it runs alongside and yields, which is the whole point of splitting the
// guards.
func (a *API) libraryAction(c *gin.Context) (models.Library, bool) {
	if a.Scan == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "scanner unavailable"})
		return models.Library{}, false
	}
	id, ok := a.idParam(c)
	if !ok {
		return models.Library{}, false
	}
	var lib models.Library
	if err := a.DB.First(&lib, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "library not found"})
		return models.Library{}, false
	}
	return lib, true
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
	// No running-scan refusal: an overlapping action queues rather than being dropped,
	// and the runner collapses a duplicate of one already queued or running.
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
	a.Scan.Run(scope)
	c.JSON(http.StatusAccepted, gin.H{"status": "scan queued", "artist": artist.Name, "folders": scope.Detail["folders"]})
}

// refreshArtist re-reads this artist's catalogue and editions from MusicBrainz,
// ignoring the cache TTL, and re-tags the files of anything that changed.
func (a *API) refreshArtist(c *gin.Context) {
	artist, ok := a.artistAction(c)
	if !ok {
		return
	}
	a.Scan.RefreshArtist(artist.MBID)
	c.JSON(http.StatusAccepted, gin.H{"status": "refresh queued", "artist": artist.Name})
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
	a.Scan.RetagArtist(artist.MBID)
	c.JSON(http.StatusAccepted, gin.H{"status": "tagging queued", "artist": artist.Name, "files": len(items)})
}

// recorrelateReleaseGroup forces one album's files to be re-correlated from their
// manager — the narrowest repair. The `:mbid` segment is the release-group MB ID (named
// for the sibling route `/release-groups/:mbid/releases`, not for an artist).
func (a *API) recorrelateReleaseGroup(c *gin.Context) {
	if a.Scan == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "scanner unavailable"})
		return
	}
	rgMBID := c.Param("mbid")
	if err := a.Scan.ForceRecorrelateReleaseGroup(rgMBID); err != nil {
		switch {
		case errors.Is(err, scan.ErrNothingToScan):
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		case errors.Is(err, gorm.ErrRecordNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "release group not found"})
		default:
			logger.Log.Error("failed to resolve release-group re-correlate scope. error: " + err.Error())
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to resolve what to re-correlate"})
		}
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": "re-correlate queued", "release_group": rgMBID})
}

// recorrelateLibrary forces every file in a library to be re-correlated from its
// manager — the widest repair, for a re-pointed Lidarr instance.
func (a *API) recorrelateLibrary(c *gin.Context) {
	lib, ok := a.libraryAction(c)
	if !ok {
		return
	}
	if err := a.Scan.ForceRecorrelateLibrary(lib.ID); err != nil {
		logger.Log.Error("failed to queue library re-correlate. error: " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to queue the re-correlate"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": "re-correlate queued", "library": lib.Name})
}

// recorrelateArtist forces this artist's files to be re-correlated from their manager,
// repairing the case a plain scan cannot: a release selection changed in Lidarr does
// not touch a byte on disk, so the unchanged-file skip means an ordinary scan never
// re-reads them. This busts the Lidarr caches, clears any manual pins on the artist's
// Lidarr-governed files, and re-walks with the skip disabled so Lidarr's current
// release is written. It discards hand-attached pins for those files by design — under
// Lidarr, identity is the manager's to decide.
func (a *API) recorrelateArtist(c *gin.Context) {
	artist, ok := a.artistAction(c)
	if !ok {
		return
	}
	if err := a.Scan.ForceRecorrelateArtist(artist.MBID); err != nil {
		if errors.Is(err, scan.ErrNothingToScan) {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		logger.Log.Error("failed to resolve artist re-correlate scope. error: " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to resolve what to re-correlate"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": "re-correlate queued", "artist": artist.Name})
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

	// Annotate each row with whether its file's identity may be set by hand, resolved
	// once per library (a page is usually one library, often one manager). A resolution
	// failure fails closed to not-editable: hiding the attach control is the safe
	// default, and the attach endpoint itself is still gated regardless.
	editableByLibrary := map[uuid.UUID]bool{}
	views := make([]libraryItemView, 0, len(items))
	for _, item := range items {
		editable, seen := editableByLibrary[item.LibraryID]
		if !seen {
			var err error
			if editable, err = a.libraryIdentityEditable(item.LibraryID); err != nil {
				logger.Log.Warnf("list items: failed to resolve manager for library %s: %s", item.LibraryID, err.Error())
				editable = false
			}
			editableByLibrary[item.LibraryID] = editable
		}
		views = append(views, libraryItemView{LibraryItem: item, IdentityEditable: editable})
	}

	c.JSON(http.StatusOK, gin.H{
		"total":  total,
		"limit":  limit,
		"offset": offset,
		"items":  views,
	})
}

// libraryItemView is a library item plus whether its MB identity may be set by hand.
// The flag lets the attach picker hide its controls for Lidarr-managed files instead of
// offering an action the API would reject.
type libraryItemView struct {
	models.LibraryItem
	IdentityEditable bool `json:"identity_editable"`
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
	tags, err := components.ComputeItemDiff(a.DB, a.meta(), item)
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

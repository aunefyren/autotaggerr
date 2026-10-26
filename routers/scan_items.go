package routers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/components"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/process"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	defaultItemsLimit = 100
	maxItemsLimit     = 500
)

// The four verbs, at every scope they are offered.
//
//	Process — walk the folders, resolve metadata, write tags. The full pipeline.
//	Scan    — re-derive what the collection holds from the files already indexed.
//	Refresh — re-read MusicBrainz. Writes no files.
//	Tag     — rewrite tags from correlations already stored.
//
// Process is the only one that reads the disk and the only one that can discover a
// file; Scan is the cheap one that makes the collection view agree with the index.
// None of them triggers another.

// processAll runs the full pipeline over every enabled library in the background.
func (a *API) processAll(c *gin.Context) {
	res, err := a.queueProcess(verbTarget{})
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": res.Status})
}

// processLibrary runs the full pipeline over a single library.
func (a *API) processLibrary(c *gin.Context) {
	if a.Scan == nil {
		respondErr(c, errProcessorUnavailable)
		return
	}
	id, ok := a.idParam(c)
	if !ok {
		return
	}
	res, err := a.queueProcess(verbTarget{LibraryID: &id})
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": res.Status})
}

// refreshAll re-reads the MusicBrainz metadata that is due, for the whole
// collection. Writes no files: what changed upstream is reported, and the next
// process run (or Tag files) applies it.
func (a *API) refreshAll(c *gin.Context) {
	res, err := a.queueRefresh(verbTarget{}, false)
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": res.Status})
}

// retagAll rewrites every indexed file in every enabled library from the metadata
// already stored — Tag files at collection scope. Like its narrower twins it refuses
// when there is nothing indexed, rather than recording a run that tagged nothing.
func (a *API) retagAll(c *gin.Context) {
	res, err := a.queueTag(verbTarget{})
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": res.Status, "files": res.Files})
}

// refreshLibrary and retagLibrary are the library-scoped halves of the verb grid:
// the same two actions the artist page offers, aimed at one library instead.
func (a *API) refreshLibrary(c *gin.Context) {
	lib, ok := a.libraryAction(c)
	if !ok {
		return
	}
	res, err := a.queueRefresh(verbTarget{LibraryID: &lib.ID}, false)
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": res.Status, "library": res.Library})
}

func (a *API) retagLibrary(c *gin.Context) {
	lib, ok := a.libraryAction(c)
	if !ok {
		return
	}
	res, err := a.queueTag(verbTarget{LibraryID: &lib.ID})
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": res.Status, "library": res.Library, "files": res.Files})
}

// libraryAction resolves the library a scoped action targets, answering the shared
// failure cases so each handler is only about its own verb.
//
// Only the file-writing verbs are gated on a running scan. A metadata refresh is
// not — it runs alongside and yields, which is the whole point of splitting the
// guards.
func (a *API) libraryAction(c *gin.Context) (models.Library, bool) {
	if a.Scan == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "processor unavailable"})
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

// The per-artist actions: the same four verbs, narrowed to one artist. They exist
// because the whole library is the wrong unit for a fix — noticing one artist is
// wrong should not cost a seven-hour cold run. Cheapest first:
//
//	scan    — re-derive this artist's albums from the files already indexed. No disk
//	          walk, no network, no file writes.
//	retag   — rewrite tags from correlations already stored. No disk walk, no network.
//	refresh — re-read the artist's catalogue and editions from MusicBrainz. No disk
//	          walk, no file writes.
//	process — walk the artist's folders through the full pipeline. Finds new, changed
//	          and never-correlated files.
//
// The three queued ones report through the same status and Activity feed as a full
// run, because each *is* a full run with a narrower scope. Scan answers inline: it is
// a database pass measured in milliseconds, so a queued job and an event would be
// more machinery than the work.

// artistAction resolves the artist and the shared preconditions of all three
// actions, then starts work in the background. It returns the artist so callers can
// name it in their response.
func (a *API) artistAction(c *gin.Context) (models.CollectionArtist, bool) {
	var artist models.CollectionArtist
	if a.Scan == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "processor unavailable"})
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

// processArtist walks just this artist's folders. The scope is resolved before
// responding so "this artist has no files yet" is an answer, not a run that reports
// zero files a minute later.
func (a *API) processArtist(c *gin.Context) {
	artist, ok := a.artistAction(c)
	if !ok {
		return
	}
	res, err := a.queueProcess(verbTarget{ArtistMBID: artist.MBID})
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": res.Status, "artist": res.Artist, "folders": res.Folders})
}

// scanArtist re-derives one artist's albums from the files already indexed — the
// *Scan* verb at artist scope. It writes no audio file and asks MusicBrainz nothing,
// so it answers inline with what it found. It does stat each of the artist's indexed
// files and drop the rows it proves gone (see collection.pruneGoneFiles) — a handful
// of files at this scope, so still inline-cheap.
//
// With walk_disk set it is queued instead: the artist's folders are walked first, so
// files that moved or arrived are found before the scan decides what is gone (see
// process.DiscoverArtist). Still no audio file is written.
func (a *API) scanArtist(c *gin.Context) {
	artist, ok := a.artistAction(c)
	if !ok {
		return
	}
	res, err := a.runScan(verbTarget{ArtistMBID: artist.MBID}, walkDiskRequested(c))
	if err != nil {
		respondErr(c, err)
		return
	}
	if res.Queued {
		c.JSON(http.StatusAccepted, gin.H{"status": "scan queued", "artist": res.Artist})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"artist":               res.Artist,
		"artists":              res.Stats.Artists,
		"owned_release_groups": res.Stats.Owned,
		"credit_changes":       res.Stats.CreditChanges,
		"files_removed":        res.Stats.FilesRemoved,
	})
}

// refreshArtist re-reads this artist's catalogue and editions from MusicBrainz and
// re-tags the files of anything that changed.
//
// It **honours the cache**, unlike what this comment used to claim. ArtistScope
// stopped forcing when forcing became a deliberate, collection-scoped choice: what a
// user wants from a per-artist refresh is "up to date", which an unforced pass
// delivers, and making the narrow button the expensive reading of words that mean the
// cheap thing everywhere else was the trap. There is no per-artist force, and
// mirror.TestOnlyAnExplicitForceIgnoresTheCache holds that line.
func (a *API) refreshArtist(c *gin.Context) {
	artist, ok := a.artistAction(c)
	if !ok {
		return
	}
	// force is the artist-scoped reading of the same choice the collection offers, and
	// it arrives the same way: an explicit argument from the refresh dialog. Absent, the
	// pass honours the cache — which is what the button on its own means everywhere.
	force := c.Query("force") == "true"
	res, err := a.queueRefresh(verbTarget{ArtistMBID: artist.MBID}, force)
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": res.Status, "artist": res.Artist, "force": force})
}

// retagArtist rewrites this artist's indexed files from their stored correlations.
func (a *API) retagArtist(c *gin.Context) {
	artist, ok := a.artistAction(c)
	if !ok {
		return
	}
	res, err := a.queueTag(verbTarget{ArtistMBID: artist.MBID})
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": res.Status, "artist": res.Artist, "files": res.Files})
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
		case errors.Is(err, process.ErrNothingToProcess):
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
		if errors.Is(err, process.ErrNothingToProcess) {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		logger.Log.Error("failed to resolve artist re-correlate scope. error: " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to resolve what to re-correlate"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": "re-correlate queued", "artist": artist.Name})
}

// cancelJob asks the running job to stop between files (Runner.CancelCurrent). Jobs
// queued behind it are left alone. 409 when nothing is running, so a stale button says
// so rather than pretending it stopped something.
func (a *API) cancelJob(c *gin.Context) {
	if a.Scan == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "processor unavailable"})
		return
	}
	job, ok := a.Scan.CancelCurrent()
	if !ok {
		c.JSON(http.StatusConflict, gin.H{"error": "nothing is running"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": "stopping", "job": job})
}

// processStatus reports the job queue and the current or most recent run. It covers
// every queued verb, not only processing — the queue is one, so its status is one.
func (a *API) processStatus(c *gin.Context) {
	if a.Scan == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "processor unavailable"})
		return
	}
	c.JSON(http.StatusOK, a.Scan.Status())
}

// listLibraryItems returns a paginated, filterable view of the correlation index.
// Filters: library_id, status, transient, q (path substring), mbid and pin_held.
// Pagination: limit/offset.
func (a *API) listLibraryItems(c *gin.Context) {
	filter := itemFilter{
		Status:  c.Query("status"),
		Search:  c.Query("q"),
		MBID:    c.Query("mbid"),
		PinHeld: c.Query("pin_held") == "true",
		Limit:   parseIntDefault(c.Query("limit"), defaultItemsLimit),
		Offset:  parseIntDefault(c.Query("offset"), 0),
	}
	if raw := c.Query("library_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid library_id"})
			return
		}
		filter.LibraryID = &id
	}
	switch c.Query("transient") {
	case "true":
		filter.Transient = boolPtr(true)
	case "false":
		filter.Transient = boolPtr(false)
	case "":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "transient must be true or false"})
		return
	}

	page, err := a.libraryItems(filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"total":  page.Total,
		"limit":  page.Limit,
		"offset": page.Offset,
		"items":  page.Items,
	})
}

func boolPtr(v bool) *bool { return &v }

// itemFilter is the correlation index's query, shared by GET /library-items and the
// MCP list_files tool. Limit and Offset are clamped by libraryItems.
type itemFilter struct {
	LibraryID *uuid.UUID
	Status    string
	// Transient, when set, keeps only rows whose last error will (true) or will not
	// (false) clear by itself.
	Transient *bool
	Search    string
	MBID      string
	PinHeld   bool
	Limit     int
	Offset    int
}

type itemPage struct {
	Total         int64
	Limit, Offset int
	Items         []libraryItemView
}

// libraryItems runs one page of the index, ordered by path. Its errors are safe to
// show a client.
func (a *API) libraryItems(f itemFilter) (itemPage, error) {
	q := a.DB.Model(&models.LibraryItem{})

	if f.LibraryID != nil {
		q = q.Where("library_id = ?", *f.LibraryID)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	// "Will this fix itself?" — the split LastErrorTransient exists for. With
	// status=error it separates an outage (MusicBrainz down, every file failing at
	// once) from the failures someone has to look at; with status=unmatched it picks
	// out the files waiting on a manager's second answer (models.InDiskView).
	if f.Transient != nil {
		q = q.Where("last_error_transient = ?", *f.Transient)
	}
	if f.Search != "" {
		q = q.Where("path LIKE ?", "%"+f.Search+"%")
	}
	// Pins held for review (LibraryItem.PinHeld) — their own filter rather than a
	// status, because a held file's status says what the last attempt did, and what
	// it needs is a person.
	if f.PinHeld {
		q = q.Where("pin_held = ?", true)
	}
	// "Show me the files behind this identifier" — the other half of the question
	// GET /mb/:mbid/files answers, asked of a page the user can then act on rather than
	// of a read-only detail row. Any of the three kinds, resolved the same way, because
	// the caller is a link on a row that knows an MBID and not which kind of thing it
	// stands for.
	//
	// An identifier nothing points at yields no rows rather than every row: an empty
	// result is the honest answer to "which files depend on this", and quietly ignoring
	// the filter would show a whole library under a heading naming one album.
	if mbid := strings.TrimSpace(f.MBID); mbid != "" {
		q = q.Where("mb_release_id IN ?", a.releasesUnder(mbid))
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return itemPage{}, errors.New("failed to count items")
	}

	limit := f.Limit
	if limit < 1 {
		limit = defaultItemsLimit
	}
	if limit > maxItemsLimit {
		limit = maxItemsLimit
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	var items []models.LibraryItem
	if err := q.Order("path").Limit(limit).Offset(offset).Find(&items).Error; err != nil {
		return itemPage{}, errors.New("failed to list items")
	}

	// Annotate each row with whether its file's identity may be set by hand, by the
	// same resolver the attach gate uses. A resolution failure fails closed to
	// not-editable: hiding the attach control is the safe default, and the attach
	// endpoint itself is still gated regardless.
	editable, err := a.itemsIdentityEditable(items)
	if err != nil {
		logger.Log.Warnf("list items: failed to resolve who identifies these files: %s", err.Error())
	}
	// An unidentified file in a Lidarr library can still be attached to a detached
	// artist's release (filesIdentityEditable reads the artist from the target), so it
	// is offered — but only once some artist is detached, or the offer is one every
	// attempt would be refused.
	anyDetached, err := collection.AnyArtistDetached(a.DB)
	if err != nil {
		logger.Log.Warnf("list items: failed to read whether any artist is detached: %s", err.Error())
	}
	views := make([]libraryItemView, 0, len(items))
	for _, item := range items {
		views = append(views, libraryItemView{LibraryItem: item, IdentityEditable: editable[item.ID],
			AttachIfDetached: anyDetached && !editable[item.ID] && item.MBReleaseID == ""})
	}
	return itemPage{Total: total, Limit: limit, Offset: offset, Items: views}, nil
}

// libraryItemView is a library item plus whether its MB identity may be set by hand.
// The flag lets the attach picker hide its controls for Lidarr-managed files instead of
// offering an action the API would reject.
type libraryItemView struct {
	models.LibraryItem
	IdentityEditable bool `json:"identity_editable"`
	// AttachIfDetached marks a file that is not editable as it stands — it is in a
	// Lidarr library — but has no identity yet, so it may be attached to a release of
	// an artist detached from Lidarr. The UI offers the attach and says so.
	AttachIfDetached bool `json:"attach_if_detached,omitempty"`
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
	diff, err := a.itemTagDiff(item)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}
	response := gin.H{"item": item, "tags": diff.Tags}
	if diff.PathError != "" {
		response["path_error"] = diff.PathError
	} else {
		response["path"] = diff.Path
	}
	c.JSON(http.StatusOK, response)
}

// itemTagDiffData is a file's current-vs-desired tags and where its profile would
// put it, for GET /library-items/:id/tags and the MCP get_file tool.
type itemTagDiffData struct {
	Tags      []models.TagDiffEntry
	Path      *models.PathDiff
	PathError string
}

// itemTagDiff computes what a write would change. It reads the release through the
// metadata source, so it may reach MusicBrainz. An error means no diff exists (the
// file is not identified, or its release cannot be read); a path that cannot be
// rendered is reported beside the tags, never instead of them — a release the naming
// cannot render is no reason to hide what the tags would be.
func (a *API) itemTagDiff(item models.LibraryItem) (itemTagDiffData, error) {
	tags, err := components.ComputeItemDiff(a.DB, a.meta(), item)
	if err != nil {
		return itemTagDiffData{}, err
	}
	out := itemTagDiffData{Tags: tags}
	if path, err := components.ComputeItemPath(a.DB, a.meta(), item); err != nil {
		out.PathError = err.Error()
	} else {
		out.Path = path
	}
	return out, nil
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

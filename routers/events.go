package routers

import (
	"net/http"
	"strings"

	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	defaultEventsLimit = 50
	maxEventsLimit     = 200
)

// listEvents returns the Activity feed, newest first, filterable by type/status.
//
// **The feed is flat.** Every activity is its own row at its own start time, whether it
// was pressed or spawned by a run — one thing that happened, one row, ordered by when
// it happened. It used to list runs and hide their stages behind an expander, which
// made the same work render two different ways depending on what started it, and put
// the interesting rows one disclosure and two modals away.
//
// Relation is annotated rather than structural: a stage row carries the title of the
// run it belongs to, a run row carries how many activities it spawned, and `parent`
// narrows the feed to one cascade. `nested=0` still returns runs only, for a caller
// that wants the old shape.
func (a *API) listEvents(c *gin.Context) {
	evType := c.Query("type")
	status := c.Query("status")
	search := strings.ToLower(strings.TrimSpace(c.Query("q")))
	parent := strings.TrimSpace(c.Query("parent"))

	q := a.DB.Model(&models.Event{})
	if evType != "" {
		q = q.Where("type = ?", evType)
	}
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if search != "" {
		// LOWER on both sides rather than relying on LIKE's collation: SQLite folds
		// ASCII case by default and Postgres does not, so the same query would find
		// different things depending on which database is behind it.
		q = q.Where("LOWER(title) LIKE ?", "%"+search+"%")
	}
	// One cascade: the run itself and everything it spawned. The run is included
	// because "show me this run" that omitted the run would be answering a narrower
	// question than it was asked.
	if parent != "" {
		parentID, err := uuid.Parse(parent)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "parent must be an event id"})
			return
		}
		q = q.Where("parent_id = ? OR id = ?", parentID, parentID)
	}

	if c.Query("nested") == "0" {
		q = q.Where("parent_id IS NULL")
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
	a.annotateFeed(rows)

	c.JSON(http.StatusOK, gin.H{
		"total":  total,
		"limit":  limit,
		"offset": offset,
		"events": rows,
		"facets": a.eventFacets(evType, status, search, parent),
	})
}

// eventFacets counts what each filter option would return, so a control can state its
// own result before it is pressed and disable itself when there is nothing behind it.
//
// Two rules make the numbers mean something:
//
//   - **Each facet excludes its own filter.** Type counts are computed with the status
//     and search filters applied but not the type one, so the list of types stays a list
//     of what you could switch to rather than collapsing to the one already chosen.
//   - **Counts are over the same set the feed lists** — every activity, stages
//     included. A chip has one job, which is to predict its own result; a count taken
//     over runs only would promise 2 and deliver 15.
func (a *API) eventFacets(evType, status, search, parent string) map[string]map[string]int64 {
	base := func() *gorm.DB {
		q := a.DB.Model(&models.Event{})
		if search != "" {
			q = q.Where("LOWER(title) LIKE ?", "%"+search+"%")
		}
		// The cascade is a scope, not a facet: narrowing to one run narrows what the
		// chips are counting, the same way it narrows the feed.
		if parentID, err := uuid.Parse(parent); err == nil {
			q = q.Where("parent_id = ? OR id = ?", parentID, parentID)
		}
		return q
	}

	count := func(q *gorm.DB, column string) map[string]int64 {
		var rows []struct {
			K string
			N int64
		}
		if err := q.Select(column + " as k, count(*) as n").Group(column).Scan(&rows).Error; err != nil {
			logger.Log.Warnf("failed to count event %s facets: %s", column, err.Error())
			return map[string]int64{}
		}
		out := make(map[string]int64, len(rows))
		for _, row := range rows {
			out[row.K] = row.N
		}
		return out
	}

	typeQ := base()
	if status != "" {
		typeQ = typeQ.Where("status = ?", status)
	}
	statusQ := base()
	if evType != "" {
		statusQ = statusQ.Where("type = ?", evType)
	}

	return map[string]map[string]int64{
		"type":   count(typeQ, "type"),
		"status": count(statusQ, "status"),
	}
}

// annotateFeed fills in the two facts a feed row needs about its relatives: how many
// activities a run spawned, and which run a stage belongs to.
//
// This is what carries the relationship in a flat feed. The rows are all the same kind
// of thing and are ordered only by when they started, so a row says where it came from
// in words rather than by its position in a tree.
//
// Two grouped queries for the whole page rather than a lookup per row — the alternative
// is 50 queries to draw one screen.
func (a *API) annotateFeed(rows []models.Event) {
	if len(rows) == 0 {
		return
	}

	runIDs := make([]uuid.UUID, 0, len(rows))
	parentIDs := make([]uuid.UUID, 0)
	for _, ev := range rows {
		if ev.ParentID == nil {
			runIDs = append(runIDs, ev.ID)
		} else {
			parentIDs = append(parentIDs, *ev.ParentID)
		}
	}

	counts := map[uuid.UUID]int{}
	if len(runIDs) > 0 {
		var agg []struct {
			ParentID uuid.UUID
			N        int
		}
		if err := a.DB.Model(&models.Event{}).
			Select("parent_id, count(*) as n").
			Where("parent_id IN ?", runIDs).
			Group("parent_id").Scan(&agg).Error; err != nil {
			logger.Log.Warnf("failed to count stage events for the feed: %s", err.Error())
		}
		for _, row := range agg {
			counts[row.ParentID] = row.N
		}
	}

	titles := map[uuid.UUID]string{}
	if len(parentIDs) > 0 {
		var parents []models.Event
		if err := a.DB.Select("id", "title", "type").Where("id IN ?", parentIDs).Find(&parents).Error; err != nil {
			logger.Log.Warnf("failed to load parent titles for the feed: %s", err.Error())
		}
		for _, p := range parents {
			titles[p.ID] = p.Title
		}
	}

	for i := range rows {
		if rows[i].ParentID == nil {
			rows[i].ChildCount = counts[rows[i].ID]
			continue
		}
		rows[i].ParentTitle = titles[*rows[i].ParentID]
	}
}

// maxMBFiles bounds one lookup's file list. A release holds a dozen files, but an
// artist MBID reaches every file of every edition of theirs, and a detail row asking
// "which files" does not want a thousand of them — it wants to recognise the album.
const maxMBFiles = 200

// mbFiles answers "which of my files does this MusicBrainz identifier stand for?".
//
// It is the question an Activity row cannot answer on its own. A metadata pass reports
// a 404 against a UUID, and the useful next thought is always *what have I got that
// points at it* — until now that meant copying the ID and searching Items by hand, if
// you could work out which field to search.
//
// It accepts any of the three kinds, because the question is the same for all of them
// and the caller is a detail row that knows only what a pass told it. Files hang off
// releases, so an artist or a release-group resolves through the collection's editions
// first; an MBID nothing knows returns an empty list rather than a 404, since "nothing
// points at it" is the answer, not a missing page.
func (a *API) mbFiles(c *gin.Context) {
	mbid := strings.TrimSpace(c.Param("mbid"))
	if mbid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "an identifier is required"})
		return
	}

	releaseIDs := a.releasesUnder(mbid)

	type fileRow struct {
		Path        string `json:"path"`
		Library     string `json:"library"`
		Status      string `json:"status"`
		Error       string `json:"error,omitempty"`
		MBReleaseID string `json:"mb_release_id"`
	}

	q := a.DB.Model(&models.LibraryItem{}).
		Select("library_items.path, library_items.status, library_items.error, library_items.mb_release_id, libraries.name as library").
		Joins("LEFT JOIN libraries ON libraries.id = library_items.library_id").
		Where("library_items.mb_release_id IN ?", releaseIDs)

	var total int64
	if err := q.Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to count files"})
		return
	}

	var rows []fileRow
	if err := q.Order("library_items.path").Limit(maxMBFiles).Scan(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list files"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"mb_id": mbid, "total": total, "files": rows})
}

// releasesUnder is every release MBID an identifier reaches, which is what files are
// keyed by. An artist or a release-group resolves through the collection's editions;
// a release is its own answer.
//
// The identifier itself is always a candidate, whatever kind it turns out to be — a
// release whose collection row was pruned still has files pointing at it, and that is
// exactly the case worth surfacing rather than hiding behind a missing row.
func (a *API) releasesUnder(mbid string) []string {
	releaseIDs := []string{mbid}
	var related []string
	if err := a.DB.Model(&models.CollectionRelease{}).
		Where("release_group_mb_id = ? OR artist_mb_id = ?", mbid, mbid).
		Distinct().Pluck("mb_id", &related).Error; err != nil {
		logger.Log.Warnf("failed to resolve editions for %s: %s", mbid, err.Error())
	}
	return append(releaseIDs, related...)
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
	// What the collection calls the MBIDs those rows are about. A metadata pass reports
	// against identifiers because that is what it read, and a page of UUIDs cannot be
	// acted on — this is what makes "these forty 404s are one artist's discography"
	// readable from the row rather than from four searches.
	events.ResolveRefs(a.DB, items)
	ev.Items = items

	// The stages this run performed, in the order they happened. Attached here rather
	// than fetched per stage: a run has a handful of them, and the order is the thing
	// a reader opens a run to see.
	children, err := events.Children(a.DB, ev.ID)
	if err != nil {
		logger.Log.Warnf("failed to load stage events for event %s: %s", ev.ID, err.Error())
	}
	ev.Children = children

	c.JSON(http.StatusOK, ev)
}

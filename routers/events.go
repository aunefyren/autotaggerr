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

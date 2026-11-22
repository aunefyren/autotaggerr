package routers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aunefyren/autotaggerr/auth"
	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/insights"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/process"
	"github.com/gin-gonic/gin"
)

// The Insights page: duplicates and library stats. Both read the index, the
// collection tables and the release cache only, so they answer inline; the one
// thing that reads audio — checking whether duplicates are the same audio — is a
// queued job. See docs/insights.md.

// insightsDuplicates lists the files that share a recording.
//
//	?library=<id>        one library (default: every enabled one)
//	?across_libraries=1  compare files in different libraries
//	?across_releases=1   include the same recording on different albums
//	?compilations=1      ...including compilations
//	?limit=100&offset=0  a page of groups (default 100, at most 500); matched is the total
func (a *API) insightsDuplicates(c *gin.Context) {
	library, err := parseLibraryParam(c.Query("library"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	limit, offset := 100, 0
	if raw := c.Query("limit"); raw != "" {
		if limit, err = strconv.Atoi(raw); err != nil || limit < 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "limit must be a positive whole number"})
			return
		}
	}
	if raw := c.Query("offset"); raw != "" {
		if offset, err = strconv.Atoi(raw); err != nil || offset < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "offset must be a whole number"})
			return
		}
	}
	report, err := insights.Duplicates(a.DB, insights.DuplicateOptions{
		Library:               library,
		AcrossLibraries:       c.Query("across_libraries") == "1",
		IncludeAcrossReleases: c.Query("across_releases") == "1",
		IncludeCompilations:   c.Query("compilations") == "1",
		Limit:                 min(limit, 500),
		Offset:                offset,
	})
	if err != nil {
		logger.Log.Errorf("failed to list duplicates: %s", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list duplicates"})
		return
	}
	c.JSON(http.StatusOK, report)
}

// checkDuplicates queues the job hashing the audio of unchecked candidates. A press
// with nothing to check is refused with the reason rather than queued to do nothing.
func (a *API) checkDuplicates(c *gin.Context) {
	if a.Scan == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "processor unavailable"})
		return
	}
	if err := a.Scan.CheckDuplicates(); err != nil {
		if errors.Is(err, process.ErrNoDuplicatesToCheck) {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		logger.Log.Errorf("failed to queue the duplicate check: %s", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to queue the duplicate check"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"queued": true})
}

// insightsStats describes what the library is made of. ?library=<id> narrows it to
// one library; the catalogue comparison is collection-wide and only present without.
func (a *API) insightsStats(c *gin.Context) {
	library, err := parseLibraryParam(c.Query("library"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	stats, err := insights.LibraryStats(a.DB, library)
	if err != nil {
		logger.Log.Errorf("failed to read library stats: %s", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read library stats"})
		return
	}
	c.JSON(http.StatusOK, stats)
}

// insightsGraph is the collaboration graph (insights.Graph). Each filter is a query
// parameter; adding one means a field on insights.GraphOptions and a line here.
//
//	?library=<id>              one library
//	?roles=credit,performer    which credits connect artists (default: credit)
//	?min_weight=2              fewest shared recordings per edge
//	?min_tracks=3              fewest recordings per artist
//	?special=1                 let Various Artists and placeholder artists in
//	?artist=<mbid>&hops=1|2    centre on one artist
//	?limit=150                 node cap (max insights.MaxGraphNodes)
//	?everyone=1                every artist credited, not only the managed ones
func (a *API) insightsGraph(c *gin.Context) {
	library, err := parseLibraryParam(c.Query("library"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	number := func(key string) (int, bool) {
		raw := strings.TrimSpace(c.Query(key))
		if raw == "" {
			return 0, true
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": key + " must be a whole number"})
			return 0, false
		}
		return n, true
	}
	opts := insights.GraphOptions{
		Library:        library,
		IncludeSpecial: c.Query("special") == "1",
		Artist:         strings.TrimSpace(c.Query("artist")),
	}
	if raw := strings.TrimSpace(c.Query("roles")); raw != "" {
		opts.Roles = strings.Split(raw, ",")
	}
	var ok bool
	if opts.MinWeight, ok = number("min_weight"); !ok {
		return
	}
	if opts.MinTracks, ok = number("min_tracks"); !ok {
		return
	}
	if opts.Hops, ok = number("hops"); !ok {
		return
	}
	if opts.Limit, ok = number("limit"); !ok {
		return
	}
	if opts.MinPlays, ok = number("min_plays"); !ok {
		return
	}
	// The listening overlay is the signed-in user's own plays (docs/listening.md).
	if user, signedIn := auth.CurrentUser(c); signedIn {
		id := user.ID
		opts.Listener = &id
	}
	opts.ListenedOnly = c.Query("listened") == "1"
	switch edges := strings.TrimSpace(c.Query("edges")); edges {
	case "", insights.EdgesCredits, insights.EdgesListening, insights.EdgesBoth:
		opts.Edges = edges
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "edges must be credits, listening or both"})
		return
	}
	// "off" draws the library alone: no plays counted, whatever else is asked.
	opts.ListeningOff = strings.TrimSpace(c.Query("since")) == "off"
	since, err := parsePeriod(c.Query("since"), time.Now())
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	opts.Since = since
	// Managed artists only, unless asked for everyone: the credits name every
	// featured artist, producer and writer, most of them strangers to the owner. Read
	// per request — three queries and the artist rows, small beside the graph itself.
	managed, err := collection.ManagedArtists(a.DB)
	if err != nil {
		logger.Log.Errorf("failed to read which artists are managed: %s", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build the collaboration graph"})
		return
	}
	opts.Managed = make(map[string]bool, len(managed))
	for id := range managed {
		opts.Managed[id] = true
	}
	opts.ManagedOnly = c.Query("everyone") != "1"
	graph, err := insights.Graph(a.DB, opts)
	if err != nil {
		logger.Log.Errorf("failed to build the collaboration graph: %s", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build the collaboration graph"})
		return
	}
	c.JSON(http.StatusOK, graph)
}

// insightsGraphArtists finds credited artists by name (?q=), for the graph's search:
// one drawn is selected, one not drawn is centred on.
func (a *API) insightsGraphArtists(c *gin.Context) {
	found, err := insights.SearchGraphArtists(a.DB, c.Query("q"), 10)
	if err != nil {
		logger.Log.Errorf("failed to search the graph's artists: %s", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to search artists"})
		return
	}
	c.JSON(http.StatusOK, found)
}

// parsePeriod reads the overlay's period: a number of days ("30d"), or empty or
// "all" for all time.
func parsePeriod(raw string, now time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "all" || raw == "off" {
		return time.Time{}, nil
	}
	days, err := strconv.Atoi(strings.TrimSuffix(raw, "d"))
	if err != nil || days < 1 || !strings.HasSuffix(raw, "d") {
		return time.Time{}, errors.New("since must be a number of days, like 30d, or all")
	}
	return now.AddDate(0, 0, -days), nil
}

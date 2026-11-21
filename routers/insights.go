package routers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

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
func (a *API) insightsDuplicates(c *gin.Context) {
	library, err := parseLibraryParam(c.Query("library"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	report, err := insights.Duplicates(a.DB, insights.DuplicateOptions{
		Library:               library,
		AcrossLibraries:       c.Query("across_libraries") == "1",
		IncludeAcrossReleases: c.Query("across_releases") == "1",
		IncludeCompilations:   c.Query("compilations") == "1",
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
	graph, err := insights.Graph(a.DB, opts)
	if err != nil {
		logger.Log.Errorf("failed to build the collaboration graph: %s", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build the collaboration graph"})
		return
	}
	c.JSON(http.StatusOK, graph)
}

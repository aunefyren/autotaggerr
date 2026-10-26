package routers

import (
	"errors"
	"net/http"

	"github.com/aunefyren/autotaggerr/process"
	"github.com/gin-gonic/gin"
)

// Organise: moving files to where their library's naming templates put them, after a
// review. See docs/naming.md#organise.

type organiseRequest struct {
	process.OrganiseScope
	// Rows is the reviewed plan, Skip set on the moves left out. Only the commit reads it.
	Rows []process.OrganiseRow `json:"rows"`
}

// organiseFailure turns the runner's refusals into statuses: a Lidarr library and a
// plan with conflicts are 409s, an empty or mixed scope a 400.
func organiseFailure(c *gin.Context, err error) {
	switch {
	case errors.Is(err, process.ErrOrganiseManaged), errors.Is(err, process.ErrOrganiseNotReady):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	}
}

// previewOrganise is the dry run: every move the scope's files would make, the files
// that cannot be moved and why, and how many are already in place. Nothing moves. It
// may read MusicBrainz for a release the cache does not hold.
func (a *API) previewOrganise(c *gin.Context) {
	if a.Scan == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "processor unavailable"})
		return
	}
	var in organiseRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	plan, err := a.Scan.PlanOrganise(in.OrganiseScope)
	if err != nil {
		organiseFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, plan)
}

// organise queues the reviewed moves. The job plans again and makes only the moves
// that are still exactly what was reviewed; the rest are reported, not guessed at.
func (a *API) organise(c *gin.Context) {
	if a.Scan == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "processor unavailable"})
		return
	}
	var in organiseRequest
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if err := a.Scan.Organise(in.OrganiseScope, in.Rows); err != nil {
		organiseFailure(c, err)
		return
	}
	moves := 0
	for _, row := range in.Rows {
		if !row.Skip {
			moves++
		}
	}
	c.JSON(http.StatusAccepted, gin.H{"status": "organise queued", "moves": moves})
}

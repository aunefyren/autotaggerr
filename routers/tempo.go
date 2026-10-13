package routers

import (
	"errors"
	"net/http"

	"github.com/aunefyren/autotaggerr/tempo"
	"github.com/gin-gonic/gin"
)

// The *Analyse tempo* verb's three routes, in the shape the metadata and artwork verbs
// already use — status, start, cancel — so the three background passes read alike.

// tempoStatus reports the current or most recent pass, how many files have a tempo on
// record, and whether a pass could do anything at all (the detector installed, a
// library opted in).
func (a *API) tempoStatus(c *gin.Context) {
	if a.Tempo == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "tempo analysis unavailable"})
		return
	}
	c.JSON(http.StatusOK, a.Tempo.Status())
}

// triggerTempoAnalysis queues a pass and returns immediately; a first pass over a
// library runs for hours. A press that could do nothing — no detector, nothing opted
// in — is refused with the reason rather than queued to record nothing.
func (a *API) triggerTempoAnalysis(c *gin.Context) {
	if a.Tempo == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "tempo analysis unavailable"})
		return
	}
	if err := a.Tempo.Check(); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, tempo.ErrDetectorMissing) || errors.Is(err, tempo.ErrNothingOptedIn) {
			status = http.StatusConflict
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	a.Tempo.AnalyseAll()
	c.JSON(http.StatusAccepted, a.Tempo.Status())
}

// cancelTempoAnalysis stops a running pass. Safe at any point: results are stored as
// they are produced, so the next pass resumes by skipping them.
func (a *API) cancelTempoAnalysis(c *gin.Context) {
	if a.Tempo == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "tempo analysis unavailable"})
		return
	}
	a.Tempo.Cancel()
	c.JSON(http.StatusOK, a.Tempo.Status())
}

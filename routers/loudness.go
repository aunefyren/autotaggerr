package routers

import (
	"errors"
	"net/http"

	"github.com/aunefyren/autotaggerr/loudness"
	"github.com/gin-gonic/gin"
)

// The *Analyse loudness* verb's routes, in the shape tempo's use — status, start,
// cancel — so the background passes read alike. See docs/replaygain.md.

func (a *API) loudnessStatus(c *gin.Context) {
	if a.Loudness == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "loudness analysis unavailable"})
		return
	}
	c.JSON(http.StatusOK, a.Loudness.Status())
}

// triggerLoudnessAnalysis queues a pass. A press that could do nothing — no rsgain,
// nothing opted in — is refused with the reason rather than queued.
func (a *API) triggerLoudnessAnalysis(c *gin.Context) {
	if a.Loudness == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "loudness analysis unavailable"})
		return
	}
	if err := a.Loudness.Check(); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, loudness.ErrAnalyserMissing) || errors.Is(err, loudness.ErrNothingOptedIn) {
			status = http.StatusConflict
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	a.Loudness.AnalyseAll()
	c.JSON(http.StatusAccepted, a.Loudness.Status())
}

func (a *API) cancelLoudnessAnalysis(c *gin.Context) {
	if a.Loudness == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "loudness analysis unavailable"})
		return
	}
	a.Loudness.Cancel()
	c.JSON(http.StatusOK, a.Loudness.Status())
}

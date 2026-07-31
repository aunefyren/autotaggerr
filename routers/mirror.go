package routers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// mirrorStatus reports the current or most recent mirror pass, plus how much of
// the collection is cached right now. The UI polls it while a pass runs — a full
// pass takes hours on a large collection, so "it is working" has to be visible.
func (a *API) mirrorStatus(c *gin.Context) {
	if a.Mirror == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "mirror unavailable"})
		return
	}
	c.JSON(http.StatusOK, a.Mirror.Status())
}

// triggerMirror starts a mirror pass in the background and returns immediately.
// The pass is far too long to hold a request open for, and it reports through
// mirrorStatus and the Activity feed instead.
//
// context.Background rather than the request context: the pass must outlive the
// HTTP request that asked for it, and tying it to the request would cancel the
// whole thing the moment the browser tab closed.
func (a *API) triggerMirror(c *gin.Context) {
	if a.Mirror == nil || a.Scan == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "mirror unavailable"})
		return
	}

	// force is what the Migrations page's "Refresh metadata" sends, and what the
	// "ignore cached copies" option on the Metadata page sets. Same verb, same
	// endpoint — the only difference is whether cached copies are trusted.
	//
	// Routed through the scan runner's queue rather than started directly, so a mirror
	// pass takes its turn behind any file-writing work instead of running alongside it.
	// Dedup collapses a second press onto the pass already queued or running.
	if c.Query("force") == "true" {
		a.Scan.VerifyIdentities()
	} else {
		a.Scan.SyncDrift()
	}

	c.JSON(http.StatusAccepted, a.Mirror.Status())
}

// cancelMirror stops a running pass at the next entity boundary. Stopping is safe
// and cheap because a pass keeps no cursor — the next one resumes by skipping
// whatever is already fresh.
func (a *API) cancelMirror(c *gin.Context) {
	if a.Mirror == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "mirror unavailable"})
		return
	}
	a.Mirror.Cancel()
	c.JSON(http.StatusOK, a.Mirror.Status())
}

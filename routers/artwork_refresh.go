package routers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// The *Refresh artwork* verb's three routes. They mirror the metadata verb's shape —
// status, start, cancel — because it is the same operation on a different kind of
// source, and an API that described it differently would be the first place the two
// names drifted apart.

// artworkStatus reports the current or most recent artwork pass, plus how many images
// are cached right now and whether the providers can serve anything at all.
//
// The capability flags matter as much as the counters: without them the page cannot
// tell "nothing was fetched because everything is current" from "nothing was fetched
// because no artwork source is configured", which are opposite situations that look
// identical in a row of zeroes.
func (a *API) artworkStatus(c *gin.Context) {
	if a.Artwork == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "artwork refresh unavailable"})
		return
	}
	c.JSON(http.StatusOK, a.Artwork.Status())
}

// triggerArtworkRefresh queues a pass and returns immediately. A first pass over a
// cold collection runs for the better part of an hour, which is far too long to hold
// a request open for; it reports through artworkStatus and the Activity feed instead.
//
// force ignores cached copies — both halves, so images already on disk are
// re-downloaded and remembered "no image" answers are re-asked. It is only ever sent
// by the button on /data-sources, behind its own confirmation: nothing scheduled and
// nothing automatic forces.
func (a *API) triggerArtworkRefresh(c *gin.Context) {
	if a.Artwork == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "artwork refresh unavailable"})
		return
	}
	a.Artwork.RefreshCollection(c.Query("force") == "true")
	c.JSON(http.StatusAccepted, a.Artwork.Status())
}

// cancelArtworkRefresh stops a running pass at the next image. Safe and cheap at any
// point, because a pass keeps no cursor — the next one resumes by skipping whatever
// is already cached.
func (a *API) cancelArtworkRefresh(c *gin.Context) {
	if a.Artwork == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "artwork refresh unavailable"})
		return
	}
	a.Artwork.Cancel()
	c.JSON(http.StatusOK, a.Artwork.Status())
}

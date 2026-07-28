package routers

import (
	"fmt"
	"net/http"

	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/gin-gonic/gin"
)

type artistSummary struct {
	models.CollectionArtist
	OwnedCount    int `json:"owned_count"`
	CompleteCount int `json:"complete_count"`
	PartialCount  int `json:"partial_count"`
	MissingCount  int `json:"missing_count"`
	// MismatchCount is albums where the disk view and the manager's catalog view
	// disagree — worth a look, but not an error on either side.
	MismatchCount int `json:"mismatch_count"`
}

// releaseGroupView is a release-group plus the comparisons the UI renders, so the
// disk-vs-catalog rules live in the model rather than being reimplemented in TS.
type releaseGroupView struct {
	models.CollectionReleaseGroup
	Complete    bool   `json:"complete"`
	Discrepancy string `json:"discrepancy"`
}

func newReleaseGroupView(rg models.CollectionReleaseGroup, hasCatalog bool) releaseGroupView {
	return releaseGroupView{
		CollectionReleaseGroup: rg,
		Complete:               rg.Complete(),
		Discrepancy:            rg.Discrepancy(hasCatalog),
	}
}

// hasCatalog reports, per artist MBID, whether any release-group carries catalog
// state — i.e. whether there is a manager view to compare the disk against.
func hasCatalog(groups []models.CollectionReleaseGroup) map[string]bool {
	out := map[string]bool{}
	for _, rg := range groups {
		if rg.InCatalog {
			out[rg.ArtistMBID] = true
		}
	}
	return out
}

// listArtists returns the collection: artists with owned/missing counts.
func (a *API) listArtists(c *gin.Context) {
	var artists []models.CollectionArtist
	if err := a.DB.Order("name").Find(&artists).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list artists"})
		return
	}

	// Aggregated in Go rather than SQL so the complete/discrepancy rules have exactly
	// one definition (the model) shared by this list, the artist detail, and the UI.
	type agg struct{ total, owned, complete, mismatch int }
	byArtist := map[string]*agg{}

	var groups []models.CollectionReleaseGroup
	if err := a.DB.Find(&groups).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list release groups"})
		return
	}
	catalogued := hasCatalog(groups)
	for _, rg := range groups {
		g := byArtist[rg.ArtistMBID]
		if g == nil {
			g = &agg{}
			byArtist[rg.ArtistMBID] = g
		}
		g.total++
		if rg.Owned {
			g.owned++
		}
		if rg.Complete() {
			g.complete++
		}
		if rg.Discrepancy(catalogued[rg.ArtistMBID]) != models.DiscrepancyNone {
			g.mismatch++
		}
	}

	out := make([]artistSummary, 0, len(artists))
	for _, ar := range artists {
		g := byArtist[ar.MBID]
		if g == nil {
			g = &agg{}
		}
		out = append(out, artistSummary{
			CollectionArtist: ar,
			OwnedCount:       g.owned,
			CompleteCount:    g.complete,
			PartialCount:     g.owned - g.complete,
			MissingCount:     g.total - g.owned,
			MismatchCount:    g.mismatch,
		})
	}
	c.JSON(http.StatusOK, out)
}

// getArtist returns an artist and its release-groups (owned + wanted).
func (a *API) getArtist(c *gin.Context) {
	mbid := c.Param("mbid")
	var artist models.CollectionArtist
	if err := a.DB.Where("mb_id = ?", mbid).First(&artist).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "artist not found"})
		return
	}
	var groups []models.CollectionReleaseGroup
	a.DB.Where("artist_mb_id = ?", mbid).Order("owned desc, first_release_date desc").Find(&groups)
	catalogued := hasCatalog(groups)[mbid]
	views := make([]releaseGroupView, 0, len(groups))
	for _, rg := range groups {
		views = append(views, newReleaseGroupView(rg, catalogued))
	}
	c.JSON(http.StatusOK, gin.H{"artist": artist, "release_groups": views})
}

// setArtistMonitored toggles monitoring. Enabling triggers a discography sync so
// the wanted (missing) release-groups are discovered.
func (a *API) setArtistMonitored(c *gin.Context) {
	mbid := c.Param("mbid")
	var body struct {
		Monitored bool `json:"monitored"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	var artist models.CollectionArtist
	if err := a.DB.Where("mb_id = ?", mbid).First(&artist).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "artist not found"})
		return
	}
	if err := a.DB.Model(&artist).Update("monitored", body.Monitored).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update artist"})
		return
	}

	wanted := 0
	if body.Monitored {
		n, err := collection.SyncArtist(a.DB, mbid)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "failed to sync discography: " + err.Error()})
			return
		}
		wanted = n
	}
	c.JSON(http.StatusOK, gin.H{"monitored": body.Monitored, "wanted": wanted})
}

// rebuildCollection recomputes the owned (present) side from the index. Fast and
// network-free (reads only cached releases).
func (a *API) rebuildCollection(c *gin.Context) {
	artists, owned, err := collection.Rebuild(a.DB)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to rebuild collection"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"artists": artists, "owned_release_groups": owned})
}

// syncLidarr mirrors Lidarr's monitored/missing albums for Lidarr-managed artists
// in the background, recording the result as an Activity event.
func (a *API) syncLidarr(c *gin.Context) {
	var lidarrManagers int64
	a.DB.Model(&models.Manager{}).Where("type = ? AND enabled = ?", models.ManagerTypeLidarr, true).Count(&lidarrManagers)
	if lidarrManagers == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no enabled Lidarr manager configured"})
		return
	}

	go func() {
		ev := events.Begin(a.DB, models.EventTypeLidarrSync, "Sync from Lidarr")
		artists, groups, err := collection.SyncLidarr(a.DB)
		status := models.EventStatusOK
		if err != nil {
			status = models.EventStatusError
		}
		summary := fmt.Sprintf("%d artists synced · %d albums", artists, groups)
		details := map[string]any{"artists": artists, "albums": groups}
		if err != nil {
			details["error"] = err.Error()
		}
		events.Finish(a.DB, ev, status, summary, details)
	}()

	c.JSON(http.StatusAccepted, gin.H{"status": "lidarr sync started"})
}

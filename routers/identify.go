package routers

import (
	"errors"
	"net/http"
	"os"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// acoustidSource returns the enabled AcoustID data source, if one is configured.
// No row means the feature was never set up, which is a normal state and not an
// error — the first of the three switches that make this whole pass detachable.
func acoustidSource(db *gorm.DB) (models.DataSource, bool) {
	var row models.DataSource
	err := db.Where("type = ? AND enabled = ?", models.DataSourceTypeAcoustID, true).First(&row).Error
	return row, err == nil
}

// acoustidAvailability describes why identification can or cannot run. Reported as
// its own endpoint so the attach UI can hide the button with an explanation rather
// than offering an action that always fails.
type acoustidAvailability struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// identifyAvailability checks the two global switches (a configured data source and
// fpcalc on PATH). The per-library opt-in is checked per file, in identifyItem.
func (a *API) identifyAvailability(c *gin.Context) {
	source, ok := acoustidSource(a.DB)
	switch {
	case !ok:
		c.JSON(http.StatusOK, acoustidAvailability{
			Reason: "No enabled AcoustID data source. Add one with your API key to enable fingerprint identification.",
		})
	case source.APIKey == "":
		c.JSON(http.StatusOK, acoustidAvailability{
			Reason: "The AcoustID data source has no API key.",
		})
	case !modules.FpcalcAvailable():
		c.JSON(http.StatusOK, acoustidAvailability{
			Reason: "fpcalc is not installed on the server, so files cannot be fingerprinted.",
		})
	default:
		c.JSON(http.StatusOK, acoustidAvailability{Available: true})
	}
}

// identifyItem fingerprints one file and returns ranked suggestions of what it is.
//
// It deliberately writes nothing. AcoustID identifies a *recording*, and a
// recording appears on many releases — album, single, compilation, every remaster —
// so applying its answer automatically would write a plausible wrong album into the
// file's tags, which then self-heals into looking correct forever after. The result
// is autofill for the attach picker, and a human still confirms the track.
func (a *API) identifyItem(c *gin.Context) {
	id, ok := a.idParam(c)
	if !ok {
		return
	}

	var item models.LibraryItem
	if err := a.DB.First(&item, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "library item not found"})
		return
	}

	source, configured := acoustidSource(a.DB)
	if !configured {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "no enabled AcoustID data source is configured",
		})
		return
	}
	if !modules.FpcalcAvailable() {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "fpcalc is not installed on the server, so files cannot be fingerprinted",
		})
		return
	}

	// Third switch: the library must have opted in. Checked per file rather than
	// globally so one library can use fingerprinting while another does not.
	var library models.Library
	if err := a.DB.First(&library, "id = ?", item.LibraryID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "the item's library no longer exists"})
		return
	}
	if !library.UseAcoustID {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "fingerprint identification is switched off for the library " + library.Name,
		})
		return
	}

	info, err := os.Stat(item.Path)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "the file could not be read: " + err.Error()})
		return
	}

	matches, err := modules.IdentifyFile(item.Path, source.APIKey, source.BaseURL, info.Size(), info.ModTime())
	if err != nil {
		logger.Log.Errorf("acoustid: failed to identify %s: %s", item.Path, err.Error())
		status := http.StatusBadGateway
		if errors.Is(err, os.ErrNotExist) {
			status = http.StatusUnprocessableEntity
		}
		c.JSON(status, gin.H{"error": "identification failed: " + err.Error()})
		return
	}

	// An empty list is a real answer, not an error: AcoustID may not know this
	// audio, or every candidate may have fallen below the confidence floor. Saying
	// so plainly is the point of failing closed.
	c.JSON(http.StatusOK, gin.H{
		"matches":          matches,
		"confidence_floor": modules.AcoustIDConfidenceFloor,
	})
}

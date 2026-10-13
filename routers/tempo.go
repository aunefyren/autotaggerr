package routers

import (
	"errors"
	"net/http"

	"github.com/aunefyren/autotaggerr/components"
	"github.com/aunefyren/autotaggerr/models"
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

// itemTempo returns one file's stored tempo together with the facts needed to read
// it: whether its library analyses at all, what the library does with a result, and
// whether the library's tagger profile writes tags in the first place. Without those, "no tempo" and "a tempo that is not written"
// would each look like the feature not working, for three different reasons.
//
// The BPM *tag* is not here — it is in the tag diff beside it, under the same rules a
// write would apply.
func (a *API) itemTempo(c *gin.Context) {
	id, ok := a.idParam(c)
	if !ok {
		return
	}
	var item models.LibraryItem
	if err := a.DB.First(&item, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "item not found"})
		return
	}

	var stored *models.TrackTempo
	var row models.TrackTempo
	if a.DB.Where("path = ?", item.Path).Limit(1).Find(&row).RowsAffected > 0 {
		stored = &row
	}

	analyses, writesTags, bpmWrite, floor := false, false, models.BPMWriteOff, models.DefaultBPMMinConfidence
	var library models.Library
	if err := a.DB.First(&library, "id = ?", item.LibraryID).Error; err == nil {
		analyses = library.AnalyseTempo
		tagger := components.TaggerForLibrary(a.DB, library)
		settings := tagger.Settings()
		// A profile that writes no tags at all writes no BPM either, whatever the
		// library's BPM setting says — report what will actually happen.
		writesTags = tagger.WriteEnabled()
		if writesTags {
			bpmWrite = settings.BPMWrite
		}
		if settings.BPMMinConfidence > 0 {
			floor = settings.BPMMinConfidence
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"tempo":            stored,
		"library_analyses": analyses,
		"writes_tags":      writesTags,
		"bpm_write":        bpmWrite,
		"min_confidence":   floor,
	})
}

package routers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/quality"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Quality profiles: what a library (or an artist) wants its copies to be. The rules
// are the quality package's; these handlers store rows and say who uses them. See
// docs/quality.md.

// qualityProfileView is a profile with who it applies to, so the editor can say what
// deleting it would change.
type qualityProfileView struct {
	models.QualityProfile
	Libraries []string `json:"libraries"`
	Artists   int      `json:"artists"`
}

// qualityInfo is the ladder the editor arranges into tiers: every known quality,
// worst first.
func (a *API) qualityInfo(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"qualities": quality.Ladder()})
}

func (a *API) listQualityProfiles(c *gin.Context) {
	var rows []models.QualityProfile
	if err := a.DB.Order("name").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list quality profiles"})
		return
	}
	var libraries []models.Library
	a.DB.Select("id", "name", "quality_profile_id").Where("quality_profile_id IS NOT NULL").Order("name").Find(&libraries)
	var artists []struct {
		QualityProfileID uuid.UUID
		N                int
	}
	a.DB.Model(&models.CollectionArtist{}).Select("quality_profile_id, count(*) AS n").
		Where("quality_profile_id IS NOT NULL").Group("quality_profile_id").Scan(&artists)

	out := make([]qualityProfileView, 0, len(rows))
	for _, p := range rows {
		view := qualityProfileView{QualityProfile: p, Libraries: []string{}}
		for _, l := range libraries {
			if *l.QualityProfileID == p.ID {
				view.Libraries = append(view.Libraries, l.Name)
			}
		}
		for _, ar := range artists {
			if ar.QualityProfileID == p.ID {
				view.Artists = ar.N
			}
		}
		out = append(out, view)
	}
	c.JSON(http.StatusOK, out)
}

func (a *API) getQualityProfile(c *gin.Context) { getEntity[models.QualityProfile](a, c) }

type qualityProfileInput struct {
	Name           *string        `json:"name"`
	Tiers          *quality.Tiers `json:"tiers"`
	CutoffTier     *int           `json:"cutoff_tier"`
	UpgradeAllowed *bool          `json:"upgrade_allowed"`
}

func (in qualityProfileInput) apply(p *models.QualityProfile) {
	if in.Name != nil {
		p.Name = strings.TrimSpace(*in.Name)
	}
	if in.Tiers != nil {
		p.Tiers = *in.Tiers
	}
	if in.CutoffTier != nil {
		p.CutoffTier = *in.CutoffTier
	}
	if in.UpgradeAllowed != nil {
		p.UpgradeAllowed = *in.UpgradeAllowed
	}
}

// checkQualityProfile refuses a nameless profile or one the engine cannot judge with
// (quality.Profile.Validate), writing the 400.
func checkQualityProfile(c *gin.Context, p models.QualityProfile) bool {
	if p.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return false
	}
	if err := p.Profile().Validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": strings.TrimPrefix(err.Error(), quality.ErrInvalidProfile.Error()+": ")})
		return false
	}
	return true
}

// createQualityProfile starts from the whole ladder, nothing allowed, when the body
// gives no tiers — the editor's blank slate — so a name alone is not a valid profile.
func (a *API) createQualityProfile(c *gin.Context) {
	var in qualityProfileInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	p := models.QualityProfile{Tiers: quality.LadderTiers(), UpgradeAllowed: true}
	in.apply(&p)
	if !checkQualityProfile(c, p) {
		return
	}
	if err := a.DB.Create(&p).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create failed"})
		return
	}
	c.JSON(http.StatusCreated, p)
}

func (a *API) updateQualityProfile(c *gin.Context) {
	id, ok := a.idParam(c)
	if !ok {
		return
	}
	var p models.QualityProfile
	if err := a.DB.First(&p, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	var in qualityProfileInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	in.apply(&p)
	if !checkQualityProfile(c, p) {
		return
	}
	if err := a.DB.Save(&p).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update failed"})
		return
	}
	c.JSON(http.StatusOK, p)
}

// deleteQualityProfile removes a profile and clears it from every library and artist
// using it. Unlike a tagger profile this is never refused: no profile is a valid
// state — it means no opinion on quality — so the libraries simply stop flagging
// upgrades. The editor names who uses a profile before asking.
func (a *API) deleteQualityProfile(c *gin.Context) {
	id, ok := a.idParam(c)
	if !ok {
		return
	}
	var deleted int64
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Library{}).Where("quality_profile_id = ?", id).
			Update("quality_profile_id", nil).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.CollectionArtist{}).Where("quality_profile_id = ?", id).
			Update("quality_profile_id", nil).Error; err != nil {
			return err
		}
		res := tx.Where("id = ?", id).Delete(&models.QualityProfile{})
		deleted = res.RowsAffected
		return res.Error
	})
	switch {
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete failed"})
	case deleted == 0:
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
	default:
		c.Status(http.StatusNoContent)
	}
}

// errQualityProfileNotFound is a reference to a profile that does not exist.
var errQualityProfileNotFound = errors.New("quality profile not found")

// qualityProfileExists checks a reference an edit names; nil (clearing it) is always
// allowed.
func (a *API) qualityProfileExists(id *uuid.UUID) error {
	if id == nil {
		return nil
	}
	var count int64
	if err := a.DB.Model(&models.QualityProfile{}).Where("id = ?", *id).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return errQualityProfileNotFound
	}
	return nil
}

// checkLibraryQualityProfile validates the profile a library edit names: it must
// exist, and a Lidarr library cannot take one — Lidarr decides quality there, and a
// profile would sit on the form promising something nothing reads. managerID is the
// library's manager after the edit. Writes the 400 and reports false.
func (a *API) checkLibraryQualityProfile(c *gin.Context, ref clearableUUID, managerID *uuid.UUID) bool {
	if !ref.Set || ref.Value == nil {
		return true
	}
	if err := a.qualityProfileExists(ref.Value); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "quality profile not found"})
		return false
	}
	if managerID != nil {
		var manager models.Manager
		if err := a.DB.Select("type").First(&manager, "id = ?", *managerID).Error; err == nil &&
			manager.Type == models.ManagerTypeLidarr {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Lidarr decides quality in its own libraries — set the quality profile in Lidarr"})
			return false
		}
	}
	return true
}

// setArtistQualityProfile sets or clears (null) an artist's override of their
// libraries' quality profiles.
func (a *API) setArtistQualityProfile(c *gin.Context) {
	var body struct {
		QualityProfileID clearableUUID `json:"quality_profile_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || !body.QualityProfileID.Set {
		c.JSON(http.StatusBadRequest, gin.H{"error": "quality_profile_id is required (null for the libraries' own)"})
		return
	}
	var artist models.CollectionArtist
	if err := a.DB.Where("mb_id = ?", c.Param("mbid")).First(&artist).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "artist not found"})
		return
	}
	if err := a.qualityProfileExists(body.QualityProfileID.Value); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "quality profile not found"})
		return
	}
	if err := a.DB.Model(&artist).Update("quality_profile_id", body.QualityProfileID.Value).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update artist"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"quality_profile_id": body.QualityProfileID.Value})
}

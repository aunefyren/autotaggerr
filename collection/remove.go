package collection

import (
	"errors"
	"strings"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
)

// Removing an artist.
//
// Only an artist with nothing on disk can be removed. A file credits the artist it
// belongs to, so Rebuild would put the row straight back on the next scan, and the
// button would have done nothing a user could keep. For an artist found in files,
// Stop managing (SetManaged) is the answer to "not one of mine". Removal is for the
// other kind: one added by hand from search, or one that only ever held wants.
//
// An artist Lidarr governs or lists is refused too: the mirror would re-add them, so
// they are removed in Lidarr.

var (
	// ErrArtistHasFiles refuses removing an artist something on disk credits.
	ErrArtistHasFiles = errors.New("files on disk credit this artist, so the next scan would bring them back — stop managing them instead")
	// ErrArtistGovernedByLidarr refuses removing an artist Lidarr governs or lists.
	ErrArtistGovernedByLidarr = errors.New("Lidarr lists this artist and would add them back — remove them in Lidarr")
)

// RemoveResult reports what a removal took with it, for the confirmation the caller
// shows.
type RemoveResult struct {
	Name string `json:"name"`
	// Picks is how many wants were deleted.
	Picks int `json:"picks"`
	// Albums is how many stored release-groups were deleted: the ones only this
	// artist was credited on. A collaboration credited to someone else stays, unlinked.
	Albums int `json:"albums"`
}

// RemoveArtist deletes an artist with nothing on disk, with their wants and the
// stored albums nobody else is credited on. gorm.ErrRecordNotFound when there is no
// such artist.
func RemoveArtist(db *gorm.DB, artistMBID string) (RemoveResult, error) {
	artistMBID = strings.TrimSpace(artistMBID)
	var artist models.CollectionArtist
	if err := db.Where("mb_id = ?", artistMBID).First(&artist).Error; err != nil {
		return RemoveResult{}, err
	}

	in, err := readManagedInputs(db, []string{artistMBID})
	if err != nil {
		return RemoveResult{}, err
	}
	if GovernedByLidarr(artist) || in.lidarr[artistMBID] {
		return RemoveResult{}, ErrArtistGovernedByLidarr
	}

	groups, err := ReleaseGroupsForArtist(db, artistMBID)
	if err != nil {
		return RemoveResult{}, err
	}
	for _, rg := range groups {
		if rg.Owned {
			return RemoveResult{}, ErrArtistHasFiles
		}
	}
	var copies int64
	if err := db.Model(&models.CollectionReleaseCopy{}).Where("artist_mb_id = ?", artistMBID).
		Count(&copies).Error; err != nil {
		return RemoveResult{}, err
	}
	if copies > 0 {
		return RemoveResult{}, ErrArtistHasFiles
	}

	result := RemoveResult{Name: artist.Name}
	err = db.Transaction(func(tx *gorm.DB) error {
		res := tx.Where("artist_mb_id = ?", artistMBID).Delete(&models.CollectionDesire{})
		if res.Error != nil {
			return res.Error
		}
		result.Picks = int(res.RowsAffected)

		if err := tx.Where("artist_mb_id = ?", artistMBID).
			Delete(&models.CollectionReleaseGroupArtist{}).Error; err != nil {
			return err
		}

		// An album goes with the artist only when nobody else is linked to it or
		// wants it: a collaboration is someone else's album too.
		for _, rg := range groups {
			var others int64
			if err := tx.Model(&models.CollectionReleaseGroupArtist{}).
				Where("release_group_mb_id = ?", rg.MBID).Count(&others).Error; err != nil {
				return err
			}
			if others == 0 {
				if err := tx.Model(&models.CollectionDesire{}).
					Where("release_group_mb_id = ?", rg.MBID).Count(&others).Error; err != nil {
					return err
				}
			}
			if others > 0 {
				continue
			}
			if err := tx.Where("release_group_mb_id = ?", rg.MBID).Delete(&models.CollectionRelease{}).Error; err != nil {
				return err
			}
			if err := tx.Where("mb_id = ?", rg.MBID).Delete(&models.CollectionReleaseGroup{}).Error; err != nil {
				return err
			}
			result.Albums++
		}

		if err := tx.Where("entity_type = ? AND mb_id = ?", models.ManagerLinkArtist, artistMBID).
			Delete(&models.ManagerLink{}).Error; err != nil {
			return err
		}
		return tx.Where("mb_id = ?", artistMBID).Delete(&models.CollectionArtist{}).Error
	})
	if err != nil {
		return RemoveResult{}, err
	}
	logger.Log.Infof("removed artist %q with %d want(s) and %d stored album(s)", artist.Name, result.Picks, result.Albums)
	return result, nil
}

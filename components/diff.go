package components

import (
	"fmt"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// ComputeItemDiff returns the current-vs-desired tag diff for one indexed file,
// without writing anything. It resolves the file's library tagger settings, fetches
// the release (cached), finds the matched track, builds the desired tags, and diffs
// them against what is on disk.
func ComputeItemDiff(db *gorm.DB, item models.LibraryItem) ([]models.TagDiffEntry, error) {
	if item.MBReleaseID == "" || item.MBReleaseTrackID == "" {
		return nil, fmt.Errorf("this file has no MusicBrainz correlation yet — scan it first")
	}

	var library models.Library
	if err := db.First(&library, "id = ?", item.LibraryID).Error; err != nil {
		return nil, fmt.Errorf("owning library not found")
	}
	_, tagger, err := BuildForLibrary(db, library)
	if err != nil {
		return nil, err
	}

	response, err := modules.GetMusicBrainzRelease(item.MBReleaseID)
	if err != nil {
		return nil, err
	}

	for _, media := range response.Media {
		for _, track := range media.Tracks {
			if track.ID == item.MBReleaseTrackID {
				desired, err := modules.BuildFileTags(track, media, response, tagger.Config())
				if err != nil {
					return nil, err
				}
				return modules.DiffFileTags(item.Path, desired, tagger.Config())
			}
		}
	}
	return nil, fmt.Errorf("matched track no longer present in the release data")
}

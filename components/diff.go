package components

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/aunefyren/autotaggerr/metadata"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"golang.org/x/text/unicode/norm"
	"gorm.io/gorm"
)

// ComputeItemDiff returns the current-vs-desired tag diff for one indexed file,
// without writing anything. It resolves the file's library tagger settings, fetches
// the release (through the injected metadata source, cached), finds the matched
// track, builds the desired tags, and diffs them against what is on disk.
func ComputeItemDiff(db *gorm.DB, meta metadata.MetadataSource, item models.LibraryItem) ([]models.TagDiffEntry, error) {
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

	response, err := meta.GetRelease(item.MBReleaseID)
	if err != nil {
		return nil, err
	}

	for _, media := range response.Media {
		for _, track := range media.Tracks {
			if track.ID == item.MBReleaseTrackID {
				desired, err := modules.BuildFileTags(track, media, response, tagger.Settings())
				if err != nil {
					return nil, err
				}
				// Same as the write path, so the preview shows the BPM a scan would write.
				desired.BPM = modules.TempoTagValue(item.Path, tagger.Settings())
				return modules.DiffFileTags(item.Path, desired, tagger.Settings())
			}
		}
	}
	return nil, fmt.Errorf("matched track no longer present in the release data")
}

// ComputeItemPath returns where one indexed file is against where its library's
// naming templates would put it. It renders through modules.RenderLibraryPath, the
// renderer a move would use, so the preview cannot disagree with the move.
func ComputeItemPath(db *gorm.DB, meta metadata.MetadataSource, item models.LibraryItem) (*models.PathDiff, error) {
	if item.MBReleaseID == "" || item.MBReleaseTrackID == "" {
		return nil, fmt.Errorf("this file has no MusicBrainz correlation yet — scan it first")
	}

	var library models.Library
	if err := db.First(&library, "id = ?", item.LibraryID).Error; err != nil {
		return nil, fmt.Errorf("owning library not found")
	}
	response, err := meta.GetRelease(item.MBReleaseID)
	if err != nil {
		return nil, err
	}
	desired, err := modules.RenderLibraryPath(TaggerForLibrary(db, library).Naming(), response, item.MBReleaseTrackID, filepath.Ext(item.Path))
	if err != nil {
		return nil, err
	}

	current := item.Path
	if rel, err := filepath.Rel(library.Path, item.Path); err == nil && !strings.HasPrefix(rel, "..") {
		current = rel
	}
	diff := &models.PathDiff{
		Current: norm.NFC.String(filepath.ToSlash(current)),
		Desired: norm.NFC.String(filepath.ToSlash(desired.Rel())),
	}
	diff.Changed = diff.Current != diff.Desired

	// Lidarr tracks its files by path, so moving one under it would orphan Lidarr's
	// record of the file. The proposal is still shown — it is what this profile would
	// do — but it says it will not be done here.
	if diff.Changed {
		if managerRow, err := resolveManagerRow(db, library, true); err == nil && managerRow.Type == models.ManagerTypeLidarr {
			diff.Note = "Lidarr names this library's files, so Autotaggerr will not move them."
		}
	}
	return diff, nil
}

package collection

import (
	"path/filepath"
	"sort"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/utilities"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Locating an artist's work on disk.
//
// Nothing stores an artist -> file link: library_items know their release, not who
// made it. The link is derived instead, and derived is the right answer rather than
// a shortcut — an added column would need backfilling, would go stale the moment a
// file is re-attached to a different release, and would still not know about the
// second artist on a collaboration. The chain below reads only tables a scan already
// maintains, so it is correct by construction:
//
//	artist -> release-groups (credit links, so collaborations count)
//	       -> owned editions (collection_releases)
//	       -> files          (library_items.mb_release_id)
//	       -> folders        (the artist segment of each file's path)
//
// The folder step relies on the library layout <root>/<ARTIST>/<ALBUM>/... which the
// whole pipeline already assumes, but it *reads* the artist segment from real files
// rather than guessing it from the artist's name. That matters: the folder is
// whatever the user called it ("Beatles, The", "AC-DC"), which no amount of
// normalising the MusicBrainz name reliably reproduces.

// ArtistTarget is one artist folder to work on, with the library that contains it.
// An artist can have several: one per library holding their files, and more than one
// within a library if their releases sit under differently-spelled folders.
type ArtistTarget struct {
	Library models.Library
	Path    string
}

// ArtistReleaseMBIDs returns the MusicBrainz release IDs of every edition the
// collection holds for this artist, credited or primary. This is the set an artist's
// files can point at.
func ArtistReleaseMBIDs(db *gorm.DB, artistMBID string) ([]string, error) {
	if db == nil || artistMBID == "" {
		return nil, nil
	}

	groups, err := ReleaseGroupMBIDsForArtist(db, artistMBID)
	if err != nil {
		return nil, err
	}

	// The primary-credit column is a claim in its own right, exactly as it is in
	// ReleaseGroupsForArtist: an edition written before the link table existed still
	// belongs to this artist.
	q := db.Model(&models.CollectionRelease{}).Where("artist_mb_id = ?", artistMBID)
	if len(groups) > 0 {
		q = q.Or("release_group_mb_id IN ?", groups)
	}

	var mbids []string
	if err := q.Pluck("mb_id", &mbids).Error; err != nil {
		return nil, err
	}
	return mbids, nil
}

// ArtistItems returns the indexed files that belong to this artist: successfully
// correlated items pointing at one of the artist's owned editions. Files that failed
// to correlate are excluded — there is nothing to re-tag them from.
func ArtistItems(db *gorm.DB, artistMBID string) ([]models.LibraryItem, error) {
	releases, err := ArtistReleaseMBIDs(db, artistMBID)
	if err != nil || len(releases) == 0 {
		return nil, err
	}

	var items []models.LibraryItem
	err = db.Where("mb_release_id IN ? AND status = ?", releases, models.LibraryItemStatusOK).
		Order("path").Find(&items).Error
	return items, err
}

// ArtistItemIDs is ArtistItems reduced to the IDs a re-tag needs.
func ArtistItemIDs(db *gorm.DB, artistMBID string) ([]uuid.UUID, error) {
	items, err := ArtistItems(db, artistMBID)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids, nil
}

// ArtistTargets returns the folders to walk to scan this artist, derived from where
// their indexed files actually sit. Folders are deduplicated and sorted, so a scan
// of an artist with fifty albums walks one directory, not fifty.
//
// A file whose path does not fit the <root>/<ARTIST>/<ALBUM>/... layout (sitting
// directly in the library root, say) contributes its own directory instead of being
// dropped: scanning too narrow a folder still processes the file, whereas skipping it
// would silently leave it out of a scan the user asked for.
func ArtistTargets(db *gorm.DB, artistMBID string) ([]ArtistTarget, error) {
	items, err := ArtistItems(db, artistMBID)
	if err != nil || len(items) == 0 {
		return nil, err
	}

	libraries := map[uuid.UUID]models.Library{}
	var rows []models.Library
	if err := db.Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, lib := range rows {
		libraries[lib.ID] = lib
	}

	seen := map[string]bool{}
	targets := make([]ArtistTarget, 0, 1)
	for _, item := range items {
		library, ok := libraries[item.LibraryID]
		if !ok {
			// The item outlived its library. Its folder is no longer part of any
			// configured root, so there is nothing meaningful to scan.
			logger.Log.Warnf("skipping item %q: library %s no longer exists", item.Path, item.LibraryID)
			continue
		}

		folder := filepath.Dir(item.Path)
		if artistDir, err := utilities.ExtractArtistNameFromTrackFilePath(library.Path, item.Path); err == nil {
			folder = filepath.Join(library.Path, artistDir)
		}
		if seen[folder] {
			continue
		}
		seen[folder] = true
		targets = append(targets, ArtistTarget{Library: library, Path: folder})
	}

	sort.Slice(targets, func(i, j int) bool { return targets[i].Path < targets[j].Path })
	return targets, nil
}

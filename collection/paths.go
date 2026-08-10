package collection

import (
	"path/filepath"
	"sort"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
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

// ArtistItems returns the indexed files that belong to this artist: items pointing at
// one of the artist's owned editions.
//
// Membership follows **identity**, not the outcome of the last attempt on the file.
// This used to also require status = ok, which inverted the meaning of a failure: a
// file that failed to tag was dropped from its own artist, so the re-tag that would
// have fixed it could not see it. Excluding an errored file from a repair is precisely
// what stops it recovering once the cause is fixed — the same reasoning that took the
// status filter out of the disk view (see ownedItemRows), one level up.
//
// models.TaggableItems carries the rule, so this and the library-scoped re-tags cannot
// disagree about what a file being "one of these" means: it needs an identity, and one
// the manager has not withdrawn.
func ArtistItems(db *gorm.DB, artistMBID string) ([]models.LibraryItem, error) {
	releases, err := ArtistReleaseMBIDs(db, artistMBID)
	if err != nil || len(releases) == 0 {
		return nil, err
	}

	var items []models.LibraryItem
	err = db.Where("mb_release_id IN ?", releases).Scopes(models.TaggableItems).
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

// ReleaseGroupReleaseMBIDs returns the MusicBrainz release IDs the collection holds
// for one release-group: the editions its owned files can point at.
func ReleaseGroupReleaseMBIDs(db *gorm.DB, releaseGroupMBID string) ([]string, error) {
	if db == nil || releaseGroupMBID == "" {
		return nil, nil
	}
	var mbids []string
	err := db.Model(&models.CollectionRelease{}).
		Where("release_group_mb_id = ?", releaseGroupMBID).
		Pluck("mb_id", &mbids).Error
	return mbids, err
}

// ReleaseGroupItems returns the indexed files owned of a release-group's editions,
// mirroring ArtistItems one level down — and following identity for the same reason.
func ReleaseGroupItems(db *gorm.DB, releaseGroupMBID string) ([]models.LibraryItem, error) {
	releases, err := ReleaseGroupReleaseMBIDs(db, releaseGroupMBID)
	if err != nil || len(releases) == 0 {
		return nil, err
	}
	var items []models.LibraryItem
	err = db.Where("mb_release_id IN ?", releases).Scopes(models.TaggableItems).
		Order("path").Find(&items).Error
	return items, err
}

// disownedItems are the files the disk view deliberately drops: status `unmatched`
// means the manager was asked and answered that it does not know this file, so no
// owned edition is recorded for it and `collection_releases` has nothing to match on.
//
// They still carry the release ID they were last correlated to — recordItem clears the
// outcome of an attempt, never the identity — which is the only thing left to find
// them by. It is a stale answer and unfit to *write* from, which is why these never
// reach a re-tag. It is perfectly good for deciding **which folder to walk**, which is
// all the repair verbs need.
//
// The query is narrow on purpose: the whole point is the one status ownedItemRows
// excludes. Anything else with an identity is reachable through its owned edition.
func disownedItems(db *gorm.DB) ([]models.LibraryItem, error) {
	var items []models.LibraryItem
	err := db.Where("status = ? AND mb_release_id <> ''", models.LibraryItemStatusUnmatched).
		Order("path").Find(&items).Error
	return items, err
}

// withDisowned appends the disowned files that belong belongs() to a target list,
// deduplicated by ID — a release can be half owned and half disowned, which puts the
// same file in reach of both routes.
func withDisowned(db *gorm.DB, items []models.LibraryItem, belongs func(models.LibraryItem) bool) ([]models.LibraryItem, error) {
	disowned, err := disownedItems(db)
	if err != nil {
		return nil, err
	}
	if len(disowned) == 0 {
		return items, nil
	}

	seen := make(map[uuid.UUID]bool, len(items))
	for _, item := range items {
		seen[item.ID] = true
	}
	for _, item := range disowned {
		if seen[item.ID] || !belongs(item) {
			continue
		}
		seen[item.ID] = true
		items = append(items, item)
	}

	sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	return items, nil
}

// creditedTo reports whether a file's last known release was credited to this artist,
// read from the release cache — the same lookup Rebuild derives collection artists
// with. A release that is no longer cached simply does not match: this is a widening
// step, and a miss costs the user the narrow verb, not correctness.
func creditedTo(artistMBID string) func(models.LibraryItem) bool {
	return func(item models.LibraryItem) bool {
		release, ok := modules.CachedRelease(item.MBReleaseID)
		if !ok {
			return false
		}
		for _, credit := range albumArtistCredit(release) {
			if credit.Artist.ID == artistMBID {
				return true
			}
		}
		return false
	}
}

// partOf reports whether a file's last known release belongs to this release-group.
func partOf(releaseGroupMBID string) func(models.LibraryItem) bool {
	return func(item models.LibraryItem) bool {
		rgID, ok := modules.CachedReleaseGroupID(item.MBReleaseID)
		return ok && rgID == releaseGroupMBID
	}
}

// ReleaseGroupTargets returns the folders to walk to re-process one release-group's
// files. Unlike ArtistTargets it narrows to each file's own directory (the album or
// media folder) rather than the artist folder, so a release-group action touches one
// album and not the whole discography. Recursion in the walk still catches per-disc
// subfolders, and files sharing a folder share an album by the layout convention, so a
// directory is the right granularity here.
func ReleaseGroupTargets(db *gorm.DB, releaseGroupMBID string) ([]ArtistTarget, error) {
	items, err := ReleaseGroupItems(db, releaseGroupMBID)
	if err != nil {
		return nil, err
	}
	items, err = withDisowned(db, items, partOf(releaseGroupMBID))
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
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
			logger.Log.Warnf("skipping item %q: library %s no longer exists", item.Path, item.LibraryID)
			continue
		}
		folder := filepath.Dir(item.Path)
		if seen[folder] {
			continue
		}
		seen[folder] = true
		targets = append(targets, ArtistTarget{Library: library, Path: folder})
	}

	sort.Slice(targets, func(i, j int) bool { return targets[i].Path < targets[j].Path })
	return targets, nil
}

// ArtistTargets returns the folders to walk to scan this artist, derived from where
// their indexed files actually sit. Folders are deduplicated and sorted, so a scan
// of an artist with fifty albums walks one directory, not fifty.
//
// A file whose path does not fit the <root>/<ARTIST>/<ALBUM>/... layout (sitting
// directly in the library root, say) contributes its own directory instead of being
// dropped: scanning too narrow a folder still processes the file, whereas skipping it
// would silently leave it out of a scan the user asked for.
//
// Disowned files count here (see disownedItems), and this is the one place they do.
// Without them the repair verbs were unavailable exactly when they were needed: one
// stale Lidarr trackfile cache turns every file of an album `unmatched`, the next
// rebuild prunes the owned-edition rows those files were reachable through, and this
// returns nothing — so force re-correlate, whose whole job is repairing an artist whose
// files diverged from what the manager says, refused to start. The only way out was
// ForceRecorrelateLibrary, which is library-wide and discards every Lidarr-governed pin
// in it. Deciding a folder is worth walking is a much weaker claim than owning a
// release, and it is the claim a repair actually needs.
func ArtistTargets(db *gorm.DB, artistMBID string) ([]ArtistTarget, error) {
	items, err := ArtistItems(db, artistMBID)
	if err != nil {
		return nil, err
	}
	items, err = withDisowned(db, items, creditedTo(artistMBID))
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
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

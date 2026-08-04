package collection

import (
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
)

// PruneOrphanReleaseGroups removes stored release-groups that MusicBrainz no longer
// lists for an artist.
//
// This is the release-group half of entity migration, and it has to work by
// subtraction because release-groups are the one entity Autotaggerr never fetches by
// ID: they arrive inside release payloads and inside a discography listing. There is
// therefore no redirect to observe when two groups are merged — the old one simply
// stops appearing in the artist's discography, and its row would otherwise sit in the
// collection forever, showing an album that upstream says does not exist.
//
// Absence is weak evidence, so every reading of it that could be innocent is guarded
// against. A row is only removed when *all* of these hold:
//
//   - It is absent from the live discography. The caller must pass the unfiltered
//     list, and must only call this when that list is complete — against a truncated
//     discography, "absent" means "past the page limit".
//   - Nothing on disk resolves to it. Ownership is derived by Rebuild from real
//     files; if files say the album exists, MusicBrainz's listing is not the
//     authority that gets to delete it.
//   - No manager lists it. Lidarr's catalog is a separate authority, and its view of
//     an album is not invalidated by a MusicBrainz browse result.
//   - No desire references it. Authored intent is never collateral damage — the same
//     rule the deletion path follows.
//   - No *other* artist is credited on it. A collaboration dropping off one artist's
//     discography says nothing about the other's claim to it.
//
// What survives all five is a row for an album that nobody owns, no manager knows,
// nobody asked for, and no artist is credited on — which is what a merged-away
// release-group looks like.
func PruneOrphanReleaseGroups(db *gorm.DB, artistMBID string, live []models.MusicBrainzArtistReleaseGroup) (int, error) {
	if db == nil || artistMBID == "" {
		return 0, nil
	}

	liveIDs := make(map[string]bool, len(live))
	for _, rg := range live {
		liveIDs[rg.ID] = true
	}
	// An empty live discography is not evidence that an artist's whole catalogue was
	// merged away; it is much more likely a filter or a service quirk. Refuse to act
	// on it rather than emptying the artist.
	if len(liveIDs) == 0 {
		return 0, nil
	}

	stored, err := ReleaseGroupsForArtist(db, artistMBID)
	if err != nil {
		return 0, err
	}

	pruned := 0
	for _, rg := range stored {
		if liveIDs[rg.MBID] || rg.Owned || rg.InCatalog {
			continue
		}

		orphan, err := isOrphanReleaseGroup(db, artistMBID, rg.MBID)
		if err != nil {
			return pruned, err
		}
		if !orphan {
			continue
		}

		if err := db.Where("release_group_mb_id = ?", rg.MBID).
			Delete(&models.CollectionReleaseGroupArtist{}).Error; err != nil {
			return pruned, err
		}
		if err := db.Where("mb_id = ?", rg.MBID).Delete(&models.CollectionReleaseGroup{}).Error; err != nil {
			return pruned, err
		}
		pruned++
	}

	return pruned, nil
}

// isOrphanReleaseGroup checks the two claims that live outside the release-group row
// itself: an authored want, and another artist's credit.
func isOrphanReleaseGroup(db *gorm.DB, artistMBID, releaseGroupMBID string) (bool, error) {
	var desires int64
	if err := db.Model(&models.CollectionDesire{}).
		Where("release_group_mb_id = ?", releaseGroupMBID).Count(&desires).Error; err != nil {
		return false, err
	}
	if desires > 0 {
		return false, nil
	}

	var otherCredits int64
	if err := db.Model(&models.CollectionReleaseGroupArtist{}).
		Where("release_group_mb_id = ? AND artist_mb_id <> ?", releaseGroupMBID, artistMBID).
		Count(&otherCredits).Error; err != nil {
		return false, err
	}
	if otherCredits > 0 {
		return false, nil
	}

	// An edition row pointing at this group means files resolved to it at some point,
	// even if the group's own owned flag has not caught up yet.
	var editions int64
	if err := db.Model(&models.CollectionRelease{}).
		Where("release_group_mb_id = ?", releaseGroupMBID).Count(&editions).Error; err != nil {
		return false, err
	}
	return editions == 0, nil
}

// pruneOrphanArtists removes collection artists that nothing points at any more.
//
// It exists because unlinking a release-group from an artist can leave that artist
// holding nothing at all — the placeholder an album was migrated away from is the
// ordinary case — and an artist row with an empty page is not a neutral leftover: it
// sits in the collection list claiming to be part of the library.
//
// Like PruneOrphanReleaseGroups this works by subtraction, so the guards are the
// design. A row goes only when every reading that could be innocent is ruled out:
//
//   - **Origin is `library`.** An artist added by hand is a statement of intent that
//     owning nothing does not retract — that is the whole reason Origin exists.
//   - **Not monitored.** Following someone is authored state, and an artist followed
//     before their first file arrives owns nothing by definition.
//   - **No desires.** Same rule the deletion path follows: what the user asked for is
//     never collateral damage.
//   - **No release-group claims it**, by credit link or by the primary-credit column,
//     and **no owned edition** points at it. Any of the three means there is still a
//     page worth opening.
//
// It runs inside Rebuild's transaction, after the links have been re-derived, so it
// sees the post-prune credit graph rather than the one the pass started with.
func pruneOrphanArtists(db *gorm.DB) (int, error) {
	if db == nil {
		return 0, nil
	}

	var artists []models.CollectionArtist
	if err := db.Where("origin = ? AND monitored = ?", models.CollectionOriginLibrary, false).
		Find(&artists).Error; err != nil {
		return 0, err
	}

	pruned := 0
	for _, artist := range artists {
		orphan, err := isOrphanArtist(db, artist.MBID)
		if err != nil {
			return pruned, err
		}
		if !orphan {
			continue
		}
		if err := db.Where("mb_id = ?", artist.MBID).Delete(&models.CollectionArtist{}).Error; err != nil {
			return pruned, err
		}
		logger.Log.Infof("removed collection artist %q: nothing in the collection is credited to them any more", artist.Name)
		pruned++
	}
	return pruned, nil
}

// isOrphanArtist reports whether nothing in the collection references this artist.
func isOrphanArtist(db *gorm.DB, artistMBID string) (bool, error) {
	counts := []struct {
		model any
		query string
	}{
		{&models.CollectionReleaseGroupArtist{}, "artist_mb_id = ?"},
		{&models.CollectionReleaseGroup{}, "artist_mb_id = ?"},
		{&models.CollectionRelease{}, "artist_mb_id = ?"},
		{&models.CollectionDesire{}, "artist_mb_id = ?"},
	}
	for _, c := range counts {
		var n int64
		if err := db.Model(c.model).Where(c.query, artistMBID).Count(&n).Error; err != nil {
			return false, err
		}
		if n > 0 {
			return false, nil
		}
	}
	return true, nil
}

// AllMBIDs is every release and artist the collection is keyed on — the full set a
// metadata refresh should cover.
//
// Releases come from both the file index and the owned-editions table because the two
// can disagree — a file can point at a release no edition row exists for (not yet
// rebuilt), and an edition can outlive the files that produced it (pruned on the next
// rebuild, not before). Reading only the editions table is what made a collection-wide
// refresh quietly skip releases that files on disk actually point at.
func AllMBIDs(db *gorm.DB) (releases []string, artists []string, err error) {
	if db == nil {
		return nil, nil, nil
	}

	seen := map[string]bool{}
	add := func(ids []string) {
		for _, id := range ids {
			if id != "" && !seen[id] {
				seen[id] = true
				releases = append(releases, id)
			}
		}
	}

	var fromItems []string
	if err := db.Model(&models.LibraryItem{}).
		Where("mb_release_id <> ''").
		Distinct().Pluck("mb_release_id", &fromItems).Error; err != nil {
		return nil, nil, err
	}
	add(fromItems)

	var fromEditions []string
	if err := db.Model(&models.CollectionRelease{}).
		Distinct().Pluck("mb_id", &fromEditions).Error; err != nil {
		return nil, nil, err
	}
	add(fromEditions)

	// Filtered like the releases above: a blank MBID cannot identify anything, and
	// letting one through means a refresh spends a rate-limited request to be told
	// so.
	if err := db.Model(&models.CollectionArtist{}).
		Where("mb_id <> ''").
		Distinct().Pluck("mb_id", &artists).Error; err != nil {
		return nil, nil, err
	}

	return releases, artists, nil
}

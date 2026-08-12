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

// RetireReleaseGroup removes a release-group MusicBrainz has been *confirmed* not to
// have, and reports why it declined when it declines.
//
// This is PruneOrphanReleaseGroups' sibling, separated by the strength of the evidence
// rather than by what it deletes. Prune works by subtraction — absent from a
// discography listing — and needs a complete, untruncated discography to say even
// that. Here the evidence is a direct lookup that answered 404, so no discography
// fetch is involved and a single row can be retired on demand.
//
// Every guard prune applies still applies, and `in_catalog` is among them — for a
// blunter reason than prune's. Prune defers to the manager as a competing authority on
// what exists. This does not: an ID that resolves nowhere cannot be read whoever lists
// it. It defers because `SyncManagers` upserts a row for every album the manager
// reports, so deleting one the manager still lists achieves nothing — the next sync
// puts it straight back. The album has to stop being listed *there* before removing it
// here means anything, which is what the manager-refresh repair path is for.
//
// The returned reason is empty when the group was removed. It is a sentence for the
// migration row, so a held migration can say why it will not apply rather than failing
// silently or retrying forever — and for the catalog case it names the fix.
func RetireReleaseGroup(db *gorm.DB, releaseGroupMBID string) (removed bool, reason string, err error) {
	if db == nil || releaseGroupMBID == "" {
		return false, "no release-group given", nil
	}

	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", releaseGroupMBID).First(&rg).Error; err != nil {
		// Already gone — from an earlier retirement, or an artist prune that took it.
		// Not an error: the row being absent is the state this function wants.
		return false, "", nil
	}

	reason, err = releaseGroupRetirementBlock(db, rg)
	if err != nil || reason != "" {
		return false, reason, err
	}

	if err := db.Where("release_group_mb_id = ?", releaseGroupMBID).
		Delete(&models.CollectionReleaseGroupArtist{}).Error; err != nil {
		return false, "", err
	}
	if err := db.Where("mb_id = ?", releaseGroupMBID).
		Delete(&models.CollectionReleaseGroup{}).Error; err != nil {
		return false, "", err
	}
	return true, "", nil
}

// ReleaseGroupRetirable reports whether RetireReleaseGroup would remove this group if
// it were called now, without removing anything.
//
// It exists so a *failed* retirement can be retried at the moment its blocker clears
// rather than never. The common blocker is the manager still listing the album, and
// that is precisely the condition a manager refresh is expected to change a run or a
// week later — so "failed" here means "not yet", not "no".
//
// An absent row reports retirable: there is nothing to remove and nothing blocking, so
// a caller retrying will succeed and can stop carrying the row as failed.
func ReleaseGroupRetirable(db *gorm.DB, releaseGroupMBID string) (bool, string, error) {
	if db == nil || releaseGroupMBID == "" {
		return false, "no release-group given", nil
	}
	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", releaseGroupMBID).First(&rg).Error; err != nil {
		return true, "", nil
	}
	reason, err := releaseGroupRetirementBlock(db, rg)
	if err != nil {
		return false, "", err
	}
	return reason == "", reason, nil
}

// releaseGroupRetirementBlock names the claim that stops this group being retired, or
// returns "" when nothing does. One function so the check and the act cannot disagree:
// a retry that tested different conditions from the retirement would either loop on a
// row it can never remove, or skip one it could.
func releaseGroupRetirementBlock(db *gorm.DB, rg models.CollectionReleaseGroup) (string, error) {
	if rg.Owned {
		return "files on disk still resolve to this album", nil
	}
	if rg.InCatalog {
		return "the manager still lists this album — refresh the artist there first, " +
			"or it will be restored on the next sync", nil
	}

	// Passing the row's own artist keeps "another artist is credited" meaning the same
	// thing it means in prune: a collaboration is not orphaned by one credit going.
	orphan, err := isOrphanReleaseGroup(db, rg.ArtistMBID, rg.MBID)
	if err != nil {
		return "", err
	}
	if !orphan {
		return "an authored want, another credited artist, or an owned edition still references it", nil
	}
	return "", nil
}

// GhostReleaseGroups is the manager-mirrored albums whose MusicBrainz ID resolves
// nowhere: the release-groups with a confirmed deletion recorded against them that a
// manager's catalog still lists.
//
// It reads the migration table rather than re-probing, so the Lidarr sync can report
// the finding without spending a single request. The finding belongs on that pass
// because that is where the cause is — a metadata refresh can only ever show the
// symptom, one failed row at a time, on the far side of the collection from the
// catalog that supplied the ID.
func GhostReleaseGroups(db *gorm.DB) ([]string, error) {
	if db == nil {
		return nil, nil
	}
	var mbids []string
	err := db.Model(&models.CollectionReleaseGroup{}).
		Joins("JOIN musicbrainz_migrations ON musicbrainz_migrations.old_mb_id = collection_release_groups.mb_id").
		Where("musicbrainz_migrations.entity_type = ? AND musicbrainz_migrations.kind = ?",
			models.MigrationEntityReleaseGroup, models.MigrationKindDeleted).
		Where("collection_release_groups.in_catalog = ?", true).
		Order("collection_release_groups.mb_id").
		Distinct().Pluck("collection_release_groups.mb_id", &mbids).Error
	return mbids, err
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

package collection

import (
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
)

// Artist credits for a release-group.
//
// A release-group has one *primary* credited artist and possibly more. Everything
// that asks "what belongs to this artist" goes through the link table here rather
// than CollectionReleaseGroup.ArtistMBID, which can only hold one answer.
//
// The links are **additive on purpose**. Writers know different amounts: Rebuild
// reads the release's full MusicBrainz artist credit, while a discography sync only
// knows that the artist it is syncing is credited somehow. If a partial writer could
// remove links, syncing artist two would delete artist one's claim on the same album
// — which is the bug this table exists to fix, reintroduced from the other end.

// linkReleaseGroupArtists records that each of artistMBIDs is credited on a
// release-group, in credit order. Existing links keep their position unless this
// caller knows better (see authoritative). Best-effort: the collection view is
// derived data, so a failure to link is logged, never propagated into a scan.
func linkReleaseGroupArtists(db *gorm.DB, releaseGroupMBID string, artistMBIDs []string, authoritative bool) {
	if db == nil || releaseGroupMBID == "" {
		return
	}

	for position, artistMBID := range artistMBIDs {
		if artistMBID == "" {
			continue
		}

		var link models.CollectionReleaseGroupArtist
		err := db.Where("release_group_mb_id = ? AND artist_mb_id = ?", releaseGroupMBID, artistMBID).
			First(&link).Error
		if err == nil {
			// Only a caller that read the real artist credit may renumber, so a
			// single-artist writer cannot demote a collaborator to primary.
			if authoritative && link.Position != position {
				db.Model(&link).Update("position", position)
			}
			continue
		}
		if err != gorm.ErrRecordNotFound {
			logger.Log.Warnf("failed to read release-group artist link %s/%s: %s", releaseGroupMBID, artistMBID, err.Error())
			continue
		}

		if err := db.Create(&models.CollectionReleaseGroupArtist{
			ReleaseGroupMBID: releaseGroupMBID,
			ArtistMBID:       artistMBID,
			Position:         position,
		}).Error; err != nil {
			logger.Log.Warnf("failed to link release-group %s to artist %s: %s", releaseGroupMBID, artistMBID, err.Error())
		}
	}
}

// ReleaseGroupMBIDsForArtist returns every release-group this artist is credited on,
// primary or not. This is what an artist page lists.
func ReleaseGroupMBIDsForArtist(db *gorm.DB, artistMBID string) ([]string, error) {
	var mbids []string
	if db == nil || artistMBID == "" {
		return mbids, nil
	}
	err := db.Model(&models.CollectionReleaseGroupArtist{}).
		Where("artist_mb_id = ?", artistMBID).
		Order("position").
		Pluck("release_group_mb_id", &mbids).Error
	return mbids, err
}

// ReleaseGroupsForArtist loads the artist's release-groups themselves, ordered the
// way the artist page wants them (owned first, then newest).
//
// It is the **union** of the link table and the primary-credit column, not the link
// table alone. The column is a claim in its own right, so a row that has one but no
// link — written before the link table existed, or by a writer that only set the
// column — still appears. That makes BackfillReleaseGroupArtists an optimisation
// rather than something a page's correctness depends on, and it cannot hide anything:
// the union only ever adds rows the column already claimed.
func ReleaseGroupsForArtist(db *gorm.DB, artistMBID string) ([]models.CollectionReleaseGroup, error) {
	var groups []models.CollectionReleaseGroup
	if db == nil || artistMBID == "" {
		return groups, nil
	}

	linked, err := ReleaseGroupMBIDsForArtist(db, artistMBID)
	if err != nil {
		return nil, err
	}

	q := db.Where("artist_mb_id = ?", artistMBID)
	if len(linked) > 0 {
		q = q.Or("mb_id IN ?", linked)
	}
	err = q.Order("owned desc, first_release_date desc").Find(&groups).Error
	return groups, err
}

// ArtistsByReleaseGroup maps release-group MBID -> credited artist MBIDs, in credit
// order. One query for a whole page, so the collection overview does not ask per row.
func ArtistsByReleaseGroup(db *gorm.DB, releaseGroupMBIDs []string) (map[string][]string, error) {
	out := map[string][]string{}
	if db == nil || len(releaseGroupMBIDs) == 0 {
		return out, nil
	}

	var links []models.CollectionReleaseGroupArtist
	if err := db.Where("release_group_mb_id IN ?", releaseGroupMBIDs).
		Order("release_group_mb_id, position").Find(&links).Error; err != nil {
		return nil, err
	}
	for _, l := range links {
		out[l.ReleaseGroupMBID] = append(out[l.ReleaseGroupMBID], l.ArtistMBID)
	}
	return out, nil
}

// BackfillReleaseGroupArtists creates a link for every release-group that has none,
// from the single ArtistMBID column those rows already carry. It runs at startup and
// is a no-op once every row is linked.
//
// Without it, upgrading would empty every artist page until the next rebuild: the
// pages read the link table, and rows written before it existed have no links. The
// backfill cannot recover the *second* artist of an existing collaboration — nothing
// stored it — but the next Rebuild does, from the cached release.
func BackfillReleaseGroupArtists(db *gorm.DB) error {
	if db == nil {
		return nil
	}

	var groups []models.CollectionReleaseGroup
	if err := db.Where("artist_mb_id <> ''").
		Where("mb_id NOT IN (?)", db.Model(&models.CollectionReleaseGroupArtist{}).Select("release_group_mb_id")).
		Find(&groups).Error; err != nil {
		return err
	}
	if len(groups) == 0 {
		return nil
	}

	links := make([]models.CollectionReleaseGroupArtist, 0, len(groups))
	for _, rg := range groups {
		links = append(links, models.CollectionReleaseGroupArtist{
			ReleaseGroupMBID: rg.MBID,
			ArtistMBID:       rg.ArtistMBID,
			Position:         0,
		})
	}
	if err := db.CreateInBatches(links, 200).Error; err != nil {
		return err
	}
	logger.Log.Infof("linked %d existing release-groups to their credited artist", len(links))
	return nil
}

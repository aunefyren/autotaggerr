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
// Links are **additive for every writer but one**. Writers know different amounts:
// Rebuild reads the release-group's full MusicBrainz artist credit, while a
// discography sync only knows that the artist it is syncing is credited somehow. If a
// partial writer could remove links, syncing artist two would delete artist one's
// claim on the same album — which is the bug this table exists to fix, reintroduced
// from the other end.
//
// The exception is Rebuild, and only for a release-group it just re-derived from a
// cached release. It is the one caller holding the authoritative credit, so it is the
// one caller that can tell an artist who is *no longer* credited from an artist it
// merely does not know about. Without that, an upstream artist migration was
// permanent in the other direction: the new credit was added, the old one stayed, and
// the album kept appearing on an artist's page that MusicBrainz had moved it off.
// See pruneReleaseGroupArtists.

// creditChanges accumulates what a pass altered about the credit graph, so an
// upstream artist migration stops being invisible.
//
// A merge or a deletion leaves a musicbrainz_migrations row; a re-credit leaves
// nothing. The release keeps its ID, the release-group keeps its ID, and Rebuild
// simply files the album under someone else on its next pass — correct, silent, and
// impossible to notice until an album is not where it was. Nothing here is keyed on a
// credit, so this needs no migration row and no approval: it needs a number on the run
// that did it.
//
// It is an accumulator passed *into* the writers rather than a value returned by them
// because the two events happen in different places (the release-group upsert and the
// link prune) and only Rebuild wants the total. A nil pointer is the ordinary case —
// the manager mirrors write credits too, and what a mirror does to its own catalog is
// not an upstream change.
type creditChanges struct {
	// regrouped counts release-groups whose primary credit moved to a *different*
	// artist. Filling in a blank credit is not a move.
	regrouped int
	// unlinked counts credit links dropped because MusicBrainz no longer names the
	// artist on that release-group.
	unlinked int
}

// regroup records a primary credit moving between artists. Nil-safe: writers that do
// not report take no branch at the call site.
func (c *creditChanges) regroup() {
	if c != nil {
		c.regrouped++
	}
}

// unlink records n credit links dropped.
func (c *creditChanges) unlink(n int) {
	if c != nil {
		c.unlinked += n
	}
}

// total is what the run reports as one number.
func (c *creditChanges) total() int {
	if c == nil {
		return 0
	}
	return c.regrouped + c.unlinked
}

// creditSource names which authority a link is being written by. It decides which
// provenance flag the write sets, and therefore whose claim survives a prune.
type creditSource int

const (
	// creditFromDisk is collection.Rebuild: it read the release-group's real credit.
	creditFromDisk creditSource = iota
	// creditFromCatalog is a manager mirror or a discography sync: it knows its own
	// artist is credited, and nothing about anyone else's position.
	creditFromCatalog
)

// linkReleaseGroupArtists records that each of artistMBIDs is credited on a
// release-group, in credit order, and stamps the calling authority's provenance flag
// on each link (including ones that already existed — a link written before the flags
// or by the other authority gains this one's claim without losing theirs). Existing
// links keep their position unless this caller knows better (see authoritative).
// Best-effort: the collection view is derived data, so a failure to link is logged,
// never propagated into a scan.
func linkReleaseGroupArtists(db *gorm.DB, releaseGroupMBID string, artistMBIDs []string, authoritative bool, source creditSource) {
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
			updates := map[string]any{}
			// Only a caller that read the real artist credit may renumber, so a
			// single-artist writer cannot demote a collaborator to primary.
			if authoritative && link.Position != position {
				updates["position"] = position
			}
			if source == creditFromDisk && !link.FromDisk {
				updates["from_disk"] = true
			}
			if source == creditFromCatalog && !link.FromCatalog {
				updates["from_catalog"] = true
			}
			if len(updates) > 0 {
				if err := db.Model(&link).Updates(updates).Error; err != nil {
					logger.Log.Warnf("failed to update release-group artist link %s/%s: %s", releaseGroupMBID, artistMBID, err.Error())
				}
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
			FromDisk:         source == creditFromDisk,
			FromCatalog:      source == creditFromCatalog,
		}).Error; err != nil {
			logger.Log.Warnf("failed to link release-group %s to artist %s: %s", releaseGroupMBID, artistMBID, err.Error())
		}
	}
}

// pruneReleaseGroupArtists drops links from a release-group to artists the
// authoritative credit no longer names. Only Rebuild may call it, and only for a group
// it just re-derived from a cached release — `credited` must be that group's full
// MusicBrainz credit, or this removes claims that are simply unknown to the caller.
//
// MusicBrainz migrates a release-group between artists (a soundtrack moving from
// "Various Artists" to its composers) without any signal that the old credit is gone.
// Adding the new link is enough to put the album on the right artist's page; it is not
// enough to take it off the wrong one, because ReleaseGroupsForArtist unions the link
// table and a stale row is indistinguishable from a collaborator.
//
// The manager's claim is never collateral. A link a mirror or discography sync also
// wrote keeps the row and merely loses its disk flag — Lidarr saying an album is this
// artist's is a separate authority's answer, and MusicBrainz re-crediting the group
// does not overrule it. What actually gets deleted is a link no manager ever wrote for
// a group whose files are on disk right now, which is what a completed upstream
// migration looks like.
func pruneReleaseGroupArtists(db *gorm.DB, releaseGroupMBID string, credited []string) int {
	if db == nil || releaseGroupMBID == "" || len(credited) == 0 {
		return 0
	}

	keep := make(map[string]bool, len(credited))
	for _, artistMBID := range credited {
		keep[artistMBID] = true
	}

	var links []models.CollectionReleaseGroupArtist
	if err := db.Where("release_group_mb_id = ?", releaseGroupMBID).Find(&links).Error; err != nil {
		logger.Log.Warnf("failed to read release-group artist links for %s: %s", releaseGroupMBID, err.Error())
		return 0
	}

	removed := 0
	for _, link := range links {
		if keep[link.ArtistMBID] {
			continue
		}
		if link.FromCatalog {
			// A manager still claims this artist. Give up only the disk half.
			if link.FromDisk {
				if err := db.Model(&link).Update("from_disk", false).Error; err != nil {
					logger.Log.Warnf("failed to clear the disk claim on %s/%s: %s", releaseGroupMBID, link.ArtistMBID, err.Error())
				}
			}
			continue
		}
		if err := db.Delete(&models.CollectionReleaseGroupArtist{}, "id = ?", link.ID).Error; err != nil {
			logger.Log.Warnf("failed to unlink release-group %s from artist %s: %s", releaseGroupMBID, link.ArtistMBID, err.Error())
			continue
		}
		logger.Log.Infof("unlinked release-group %s from artist %s: MusicBrainz no longer credits them", releaseGroupMBID, link.ArtistMBID)
		removed++
	}
	return removed
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

// CreditedArtists returns every artist a release-group belongs to, falling back to
// its primary credit when the link map has nothing (an unlinked row, or a caller that
// did not load them). It lives here, next to the link table it reads, because the API
// and the manager-desire reconciliation both have to answer "whose album is this?"
// and two answers to that question is what made collaborations flip between artists.
func CreditedArtists(rg models.CollectionReleaseGroup, credits map[string][]string) []string {
	if linked := credits[rg.MBID]; len(linked) > 0 {
		return linked
	}
	if rg.ArtistMBID == "" {
		return nil
	}
	return []string{rg.ArtistMBID}
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

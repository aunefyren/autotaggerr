// Package migration applies MusicBrainz identity changes to Autotaggerr's own data.
//
// Detection lives in modules (it happens on the fetch path, under the rate limiter);
// this package is the other half — deciding whether a detected change may be applied
// yet, and rewriting the rows if so.
//
// The split matters because applying a migration is not a field update. Every table
// that stores an MBID has a unique index on it, so a merge whose target row already
// exists — the *common* case, since you probably own files under both IDs — has to
// merge two rows and dedupe, not update one. And the tables divide into two kinds
// that must be treated differently:
//
//   - Derived state (ownership counts, which release-group is owned) is rebuilt from
//     disk by collection.Rebuild after every scan and sync. It needs no careful
//     merging; it needs the stale row gone so Rebuild can recompute cleanly.
//   - Authored state (desires, monitoring, follow types) exists nowhere else. Losing
//     it to a merge would be the one genuinely unrecoverable outcome here, so the
//     merge rules below always union rather than pick a winner.
package migration

import (
	"errors"
	"fmt"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Policy decides which detected migrations are applied automatically and which are
// held for a human. It mirrors the autotaggerr_migration_review_* config keys.
//
// The fields are phrased as "review this" rather than "auto-apply this" so that the
// zero value means apply — a config.json written before this feature existed decodes
// to all-false, and that must keep the app moving rather than silently filling a
// queue nobody knows to look at.
type Policy struct {
	ReviewReleases  bool
	ReviewArtists   bool
	ReviewPinned    bool
	ReviewDeletions bool
}

// PolicyFromConfig reads the policy out of the process config.
func PolicyFromConfig(cfg models.ConfigStruct) Policy {
	return Policy{
		ReviewReleases:  cfg.AutotaggerrMigrationReviewReleases,
		ReviewArtists:   cfg.AutotaggerrMigrationReviewArtists,
		ReviewPinned:    cfg.AutotaggerrMigrationReviewPinned,
		ReviewDeletions: cfg.AutotaggerrMigrationReviewDeletions,
	}
}

// heldForReview reports whether this migration must wait for approval. The pinned
// rule is an override, not another category: a migration that would rewrite a manual
// correlation is held whatever its entity type, because the thing being second-
// guessed is a decision a person made by hand.
func (p Policy) heldForReview(m models.MusicbrainzMigration) bool {
	if m.TouchesPinned && p.ReviewPinned {
		return true
	}
	if m.Kind == models.MigrationKindDeleted {
		return p.ReviewDeletions
	}
	switch m.EntityType {
	case models.MigrationEntityArtist:
		return p.ReviewArtists
	default:
		return p.ReviewReleases
	}
}

// Result summarises a processing run, for the caller's event payload.
type Result struct {
	Applied   int      `json:"applied"`
	Pending   int      `json:"pending"`
	Failed    int      `json:"failed"`
	Files     int      `json:"files_remapped"`
	Unmatched int      `json:"files_unmatched"`
	Errors    []string `json:"errors,omitempty"`
}

// Add accumulates another run's result into this one. A run that drains the queue
// more than once (the identity sweep drains after releases and again after artists)
// would otherwise report only the last drain, hiding everything the first one did.
func (r *Result) Add(other Result) {
	r.Applied += other.Applied
	r.Pending += other.Pending
	r.Failed += other.Failed
	r.Files += other.Files
	r.Unmatched += other.Unmatched
	r.Errors = append(r.Errors, other.Errors...)
}

// ProcessPending measures every pending migration, applies the ones policy allows,
// and leaves the rest queued. Returns what happened, for the Activity event.
//
// Measuring first is what lets a pending row describe itself in the review UI ("12
// files, 1 desire") without re-querying, and is also how TouchesPinned is known —
// detection runs in modules, which has no view of library_items.
func ProcessPending(db *gorm.DB, policy Policy) (Result, error) {
	res := Result{}
	if db == nil {
		return res, errors.New("no database configured")
	}

	var pending []models.MusicbrainzMigration
	if err := db.Where("status = ?", models.MigrationStatusPending).Find(&pending).Error; err != nil {
		return res, err
	}

	for _, m := range pending {
		if err := measure(db, &m); err != nil {
			logger.Log.Warnf("failed to measure migration %s: %s", m.OldMBID, err.Error())
			continue
		}

		if policy.heldForReview(m) {
			res.Pending++
			continue
		}

		applied, err := apply(db, &m)
		if err != nil {
			res.Failed++
			res.Errors = append(res.Errors, fmt.Sprintf("%s %s: %s", m.EntityType, m.OldMBID, err.Error()))
			continue
		}
		res.Applied++
		res.Files += applied.files
		res.Unmatched += applied.unmatched
	}

	return res, nil
}

// ApplyByID applies one migration on demand — the approve button. Policy is not
// consulted: an explicit approval *is* the decision the policy was deferring to.
func ApplyByID(db *gorm.DB, id uuid.UUID) (models.MusicbrainzMigration, error) {
	var m models.MusicbrainzMigration
	if err := db.First(&m, "id = ?", id).Error; err != nil {
		return m, err
	}
	if m.Status == models.MigrationStatusApplied {
		return m, errors.New("migration has already been applied")
	}
	if err := measure(db, &m); err != nil {
		return m, err
	}
	if _, err := apply(db, &m); err != nil {
		return m, err
	}
	return m, nil
}

// Dismiss marks a migration as deliberately not applied. The row is kept rather than
// deleted, because deleting it would let the next fetch of the same old ID re-detect
// the identical move and re-queue it, which is exactly the nagging the user just
// declined.
func Dismiss(db *gorm.DB, id uuid.UUID) (models.MusicbrainzMigration, error) {
	var m models.MusicbrainzMigration
	if err := db.First(&m, "id = ?", id).Error; err != nil {
		return m, err
	}
	if m.Status == models.MigrationStatusApplied {
		return m, errors.New("migration has already been applied")
	}
	m.Status = models.MigrationStatusDismissed
	return m, db.Save(&m).Error
}

// List returns migrations, newest first, optionally filtered by status.
func List(db *gorm.DB, status string, limit int) ([]models.MusicbrainzMigration, error) {
	q := db.Order("detected_at desc")
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	var rows []models.MusicbrainzMigration
	err := q.Find(&rows).Error
	return rows, err
}

// PendingCount is the badge number for the UI.
func PendingCount(db *gorm.DB) (int64, error) {
	var n int64
	err := db.Model(&models.MusicbrainzMigration{}).
		Where("status = ?", models.MigrationStatusPending).Count(&n).Error
	return n, err
}

// measure fills in the impact snapshot and whether a pinned correlation is involved.
func measure(db *gorm.DB, m *models.MusicbrainzMigration) error {
	files, pinned, err := affectedFiles(db, *m)
	if err != nil {
		return err
	}
	desires, err := affectedDesires(db, *m)
	if err != nil {
		return err
	}

	m.AffectedFiles = files
	m.AffectedDesires = desires
	m.TouchesPinned = pinned
	if m.Name == "" {
		m.Name = describe(db, *m)
	}
	return db.Save(m).Error
}

// affectedFiles counts the indexed files a migration would touch, and whether any of
// them is a manual attachment.
func affectedFiles(db *gorm.DB, m models.MusicbrainzMigration) (int, bool, error) {
	if m.EntityType != models.MigrationEntityRelease {
		// An artist merge rewrites collection rows, not file correlations: files are
		// keyed by release, and the release is unaffected by its artist merging.
		return 0, false, nil
	}

	var items []models.LibraryItem
	if err := db.Where("mb_release_id = ?", m.OldMBID).Find(&items).Error; err != nil {
		return 0, false, err
	}
	pinned := false
	for _, item := range items {
		if item.Pinned {
			pinned = true
			break
		}
	}
	return len(items), pinned, nil
}

// affectedDesires counts authored wants that reference the old ID.
func affectedDesires(db *gorm.DB, m models.MusicbrainzMigration) (int, error) {
	var n int64
	var err error
	switch m.EntityType {
	case models.MigrationEntityArtist:
		err = db.Model(&models.CollectionDesire{}).Where("artist_mb_id = ?", m.OldMBID).Count(&n).Error
	default:
		err = db.Model(&models.CollectionDesire{}).Where("release_mb_id = ?", m.OldMBID).Count(&n).Error
	}
	return int(n), err
}

// describe names the entity for the review UI, from whatever row already knows it.
func describe(db *gorm.DB, m models.MusicbrainzMigration) string {
	switch m.EntityType {
	case models.MigrationEntityArtist:
		var artist models.CollectionArtist
		if err := db.Where("mb_id = ?", m.OldMBID).First(&artist).Error; err == nil {
			return artist.Name
		}
	default:
		var release models.CollectionRelease
		if err := db.Where("mb_id = ?", m.OldMBID).First(&release).Error; err == nil {
			return release.Title
		}
		if cached, ok := modules.CachedRelease(m.OldMBID); ok {
			return cached.Title
		}
	}
	return ""
}

// applyCounts is what one application actually changed.
type applyCounts struct {
	files     int
	unmatched int
}

// apply performs a migration in a single transaction and records the outcome on the
// row. All-or-nothing is the point: a half-remapped merge leaves some tables keyed
// on the old ID and some on the new, which is worse than not having started.
func apply(db *gorm.DB, m *models.MusicbrainzMigration) (applyCounts, error) {
	counts := applyCounts{}

	err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		switch {
		case m.Kind == models.MigrationKindDeleted:
			counts, err = applyDeletion(tx, *m)
		case m.EntityType == models.MigrationEntityArtist:
			counts, err = applyArtistRedirect(tx, *m)
		default:
			counts, err = applyReleaseRedirect(tx, *m)
		}
		return err
	})

	now := time.Now()
	if err != nil {
		m.Status = models.MigrationStatusFailed
		m.Error = err.Error()
		if saveErr := db.Save(m).Error; saveErr != nil {
			logger.Log.Warnf("failed to record migration failure for %s: %s", m.OldMBID, saveErr.Error())
		}
		return counts, err
	}

	m.Status = models.MigrationStatusApplied
	m.Error = ""
	m.AppliedAt = &now
	if err := db.Save(m).Error; err != nil {
		return counts, err
	}

	// The cache is keyed by MBID too, and the old key is now dead weight: left in
	// place it expires and gets re-fetched on every drift sync, spending rate limit
	// to re-learn a redirect that has already been dealt with.
	switch m.EntityType {
	case models.MigrationEntityRelease:
		modules.DropCachedRelease(m.OldMBID)
	case models.MigrationEntityArtist:
		// Two entries are keyed on an artist MBID, not one: who they are, and what
		// they released. Both describe an ID the app has just stopped believing in.
		modules.MusicbrainzForgetEntity(models.MBEntityArtist, m.OldMBID)
		modules.MusicbrainzForgetEntity(models.MBEntityDiscography, m.OldMBID)
	}

	logger.Log.Infof("applied MusicBrainz %s migration %s -> %s (%d files)",
		m.EntityType, m.OldMBID, m.NewMBID, counts.files)
	return counts, nil
}

// applyReleaseRedirect repoints everything keyed on a merged release.
func applyReleaseRedirect(tx *gorm.DB, m models.MusicbrainzMigration) (applyCounts, error) {
	counts := applyCounts{}
	if m.NewMBID == "" {
		return counts, errors.New("redirect has no target MBID")
	}

	// Files. Pinned items are remapped along with the rest: a merge renames an
	// entity, it does not substitute a different one, so the release the user chose
	// by hand *is* the surviving one. Leaving the pin on a dead ID would quietly
	// break the very file they took the trouble to identify.
	//
	// ProcessedVersion is cleared at the same time, which is the deliberate part.
	// Track and recording MBIDs are scoped to the release they came from, so a merge
	// leaves those two columns pointing into a release that no longer exists — and
	// nothing in this transaction can derive the replacements without fetching the
	// new release and re-matching every track, under the rate limiter, in the middle
	// of a sync. Blanking the version instead trips the existing skip-unchanged
	// escape hatch: the next scan re-correlates exactly these files and writes the
	// correct track IDs, using machinery that already exists for the case where the
	// app's behaviour changed underneath a file.
	res := tx.Model(&models.LibraryItem{}).
		Where("mb_release_id = ?", m.OldMBID).
		Updates(map[string]any{
			"mb_release_id":     m.NewMBID,
			"processed_version": "",
		})
	if res.Error != nil {
		return counts, res.Error
	}
	counts.files = int(res.RowsAffected)

	// Owned editions: derived state, so a collision is resolved by dropping the
	// stale row and letting collection.Rebuild recount from the files above.
	var target models.CollectionRelease
	targetExists := tx.Where("mb_id = ?", m.NewMBID).First(&target).Error == nil
	if targetExists {
		if err := tx.Where("mb_id = ?", m.OldMBID).Delete(&models.CollectionRelease{}).Error; err != nil {
			return counts, err
		}
	} else if err := tx.Model(&models.CollectionRelease{}).
		Where("mb_id = ?", m.OldMBID).
		Update("mb_id", m.NewMBID).Error; err != nil {
		return counts, err
	}

	// Authored intent. A desire naming the old edition now names the surviving one.
	if err := tx.Model(&models.CollectionDesire{}).
		Where("release_mb_id = ?", m.OldMBID).
		Update("release_mb_id", m.NewMBID).Error; err != nil {
		return counts, err
	}

	return counts, dedupeDesires(tx)
}

// applyArtistRedirect merges two artists into one.
//
// Every field here is unioned rather than overwritten. Monitoring and follow types
// are the only record of what the user asked for, and a merge is not an occasion to
// quietly stop following someone: if either side was monitored, the survivor is.
func applyArtistRedirect(tx *gorm.DB, m models.MusicbrainzMigration) (applyCounts, error) {
	counts := applyCounts{}
	if m.NewMBID == "" {
		return counts, errors.New("redirect has no target MBID")
	}

	var source models.CollectionArtist
	haveSource := tx.Where("mb_id = ?", m.OldMBID).First(&source).Error == nil

	var target models.CollectionArtist
	haveTarget := tx.Where("mb_id = ?", m.NewMBID).First(&target).Error == nil

	switch {
	case haveSource && haveTarget:
		target.Monitored = target.Monitored || source.Monitored
		target.FollowSecondary = target.FollowSecondary || source.FollowSecondary
		target.FollowTypes = unionCSV(target.FollowTypes, source.FollowTypes)
		// A manually added artist outranks a library-derived one: it records that
		// someone wanted this artist before owning any of them, which rebuilding
		// from disk cannot reconstruct.
		if source.Origin == models.CollectionOriginManual {
			target.Origin = models.CollectionOriginManual
		}
		if target.Name == "" {
			target.Name = source.Name
		}
		if err := tx.Save(&target).Error; err != nil {
			return counts, err
		}
		if err := tx.Where("mb_id = ?", m.OldMBID).Delete(&models.CollectionArtist{}).Error; err != nil {
			return counts, err
		}
	case haveSource:
		if err := tx.Model(&models.CollectionArtist{}).
			Where("mb_id = ?", m.OldMBID).
			Update("mb_id", m.NewMBID).Error; err != nil {
			return counts, err
		}
	}

	// Everything that points at an artist by MBID.
	for _, ref := range []struct {
		model  any
		column string
	}{
		{&models.CollectionReleaseGroup{}, "artist_mb_id"},
		{&models.CollectionRelease{}, "artist_mb_id"},
		{&models.CollectionDesire{}, "artist_mb_id"},
	} {
		if err := tx.Model(ref.model).
			Where(ref.column+" = ?", m.OldMBID).
			Update(ref.column, m.NewMBID).Error; err != nil {
			return counts, err
		}
	}

	if err := remapCreditLinks(tx, m.OldMBID, m.NewMBID); err != nil {
		return counts, err
	}
	return counts, dedupeDesires(tx)
}

// remapCreditLinks moves an artist's release-group credits onto the surviving MBID.
//
// This is the one table where a blind UPDATE fails rather than merely being wrong:
// collection_release_group_artists has a composite unique index on (release_group,
// artist), so a collaboration credited to both sides of a merge — precisely what a
// merge means — would violate it. Rows are therefore moved one at a time, and a row
// that would collide is dropped in favour of the existing one, keeping the earlier
// (more prominent) credit position of the two.
func remapCreditLinks(tx *gorm.DB, oldMBID, newMBID string) error {
	var links []models.CollectionReleaseGroupArtist
	if err := tx.Where("artist_mb_id = ?", oldMBID).Find(&links).Error; err != nil {
		return err
	}

	for _, link := range links {
		var existing models.CollectionReleaseGroupArtist
		err := tx.Where("release_group_mb_id = ? AND artist_mb_id = ?", link.ReleaseGroupMBID, newMBID).
			First(&existing).Error
		if err == nil {
			if link.Position < existing.Position {
				existing.Position = link.Position
				if err := tx.Save(&existing).Error; err != nil {
					return err
				}
			}
			if err := tx.Delete(&models.CollectionReleaseGroupArtist{}, "id = ?", link.ID).Error; err != nil {
				return err
			}
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := tx.Model(&models.CollectionReleaseGroupArtist{}).
			Where("id = ?", link.ID).
			Update("artist_mb_id", newMBID).Error; err != nil {
			return err
		}
	}
	return nil
}

// applyDeletion handles an entity that no longer exists upstream.
//
// Nothing is destroyed. The files stay indexed and keep the MB IDs they had — a dead
// ID is still the best available record of what the file was thought to be, and it
// is what makes the deletion diagnosable afterwards. What changes is the status:
// "unmatched" is the state that already means "this file needs identifying", so
// these files land in the same queue as a file that never matched, instead of in the
// error bucket next to genuine failures like an unreadable disk.
//
// Desires are never touched. A release disappearing upstream is exactly the moment
// the user's stated want becomes the only surviving record of what they were after.
func applyDeletion(tx *gorm.DB, m models.MusicbrainzMigration) (applyCounts, error) {
	counts := applyCounts{}

	if m.EntityType == models.MigrationEntityArtist {
		// An artist deletion does not orphan any file: files are keyed by release,
		// and MusicBrainz re-credits the releases rather than deleting them. Only the
		// artist's own collection row goes.
		if err := tx.Where("mb_id = ?", m.OldMBID).Delete(&models.CollectionArtist{}).Error; err != nil {
			return counts, err
		}
		return counts, nil
	}

	res := tx.Model(&models.LibraryItem{}).
		Where("mb_release_id = ?", m.OldMBID).
		Updates(map[string]any{
			"status": models.LibraryItemStatusUnmatched,
			"error":  "release no longer exists on MusicBrainz — re-identify this file",
		})
	if res.Error != nil {
		return counts, res.Error
	}
	counts.unmatched = int(res.RowsAffected)

	// The owned-edition row is derived from files that no longer resolve, so it
	// would otherwise keep the release counting towards a complete album.
	if err := tx.Where("mb_id = ?", m.OldMBID).Delete(&models.CollectionRelease{}).Error; err != nil {
		return counts, err
	}
	return counts, nil
}

// dedupeDesires collapses desires that have become identical through a remap.
//
// Wanting an album under two IDs that turn out to be the same album is one want, and
// leaving both would show the user a duplicate row they cannot tell apart. Recording
// selections are unioned rather than one row winning: each was a real choice, and the
// union is the only merge that cannot silently drop a track someone asked for.
func dedupeDesires(tx *gorm.DB) error {
	var all []models.CollectionDesire
	if err := tx.Order("created_at asc").Find(&all).Error; err != nil {
		return err
	}

	type key struct{ artist, group, release string }
	seen := map[key]*models.CollectionDesire{}

	for i := range all {
		d := all[i]
		k := key{d.ArtistMBID, d.ReleaseGroupMBID, d.ReleaseMBID}
		keeper, ok := seen[k]
		if !ok {
			kept := d
			seen[k] = &kept
			continue
		}

		merged := unionStrings(keeper.RecordingMBIDs, d.RecordingMBIDs)
		// An empty recording set means "the whole thing", which subsumes any track
		// selection — unioning it with a partial set must not narrow it back down.
		if len(keeper.RecordingMBIDs) == 0 || len(d.RecordingMBIDs) == 0 {
			merged = nil
		}
		keeper.RecordingMBIDs = merged
		if err := tx.Save(keeper).Error; err != nil {
			return err
		}
		if err := tx.Delete(&models.CollectionDesire{}, "id = ?", d.ID).Error; err != nil {
			return err
		}
	}
	return nil
}

// RelinkRelease follows a release that has changed release-group upstream.
//
// This is deliberately *not* a migration row. A release payload naming a different
// release-group than the one on record can mean the two groups were merged, or that
// this single release was moved to another group — and the payload cannot tell them
// apart. Re-pointing only the release in hand is correct under both readings, and
// collection.Rebuild recomputes the group's ownership from there. Remapping the group
// globally would be right for a merge and destructive for a move.
func RelinkRelease(db *gorm.DB, releaseMBID, releaseGroupMBID string) (bool, error) {
	if db == nil || releaseMBID == "" || releaseGroupMBID == "" {
		return false, nil
	}

	var row models.CollectionRelease
	if err := db.Where("mb_id = ?", releaseMBID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if row.ReleaseGroupMBID == releaseGroupMBID {
		return false, nil
	}

	logger.Log.Infof("release %s moved from release-group %s to %s", releaseMBID, row.ReleaseGroupMBID, releaseGroupMBID)
	return true, db.Model(&models.CollectionRelease{}).
		Where("mb_id = ?", releaseMBID).
		Update("release_group_mb_id", releaseGroupMBID).Error
}

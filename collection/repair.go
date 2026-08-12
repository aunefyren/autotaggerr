package collection

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// Repairing albums whose MusicBrainz ID has stopped resolving.
//
// The discovery is in the metadata refresh: a release-group whose ID answers 404 at
// MusicBrainz, recorded as a `release_group` deletion (see
// docs/mb-migration.md#groups-that-resolve-nowhere). What that record does *not* say is
// whether the album is gone or merely mis-keyed, and the difference decides everything:
// retiring a mis-keyed album destroys a row the manager is about to correct.
//
// The manager can tell them apart, and it is the only thing that can. Measured against
// a live Lidarr, refreshing an artist re-keyed their dead album IDs to the live
// MusicBrainz ones — `Heatstroke` moved from an ID that 404s to
// f71cd67f-bf59-48e6-a89f-359a26e7e977, which resolves — and dropped the handful that
// genuinely no longer exist anywhere. So the repair is not a guess Autotaggerr makes;
// it is a question it asks.
//
// This is emphatically better than the two alternatives considered. Searching
// MusicBrainz by title and taking the best hit would be inferring an answer the manager
// can state: measured on the same rows, the correct matches scored 78–90, below any
// confidence floor worth setting, with titles differing by Unicode punctuation and
// case. Deleting the album from the manager would destroy exactly the rows a refresh
// repairs.
//
// # Why Autotaggerr has to ask
//
// Lidarr refreshes artists on its own schedule, but throttled per artist. On the
// instance this was measured against, the scheduled pass ran and left two artists
// holding dead IDs hours later. An artist whose catalog is otherwise stable can hold
// one indefinitely. Autotaggerr is what noticed, so Autotaggerr is what asks.

// repairCooldown is how long a refresh of one artist suppresses the next.
//
// A refresh that did not fix a row will not fix it on the retry either — the album is
// genuinely absent upstream — so the value only has to be long enough that a run does
// not re-ask on every pass. It is deliberately far shorter than "never": a metadata
// service that has not caught up today may have next week, which is precisely how these
// rows come to be repairable in the first place.
const repairCooldown = 7 * 24 * time.Hour

// RepairStats is what one repair pass did, for the caller's event.
type RepairStats struct {
	// Candidates is release-groups with a confirmed deletion that a manager still
	// lists — the rows a repair could act on.
	Candidates int `json:"candidates"`
	// Artists is how many artists were actually refreshed, which is the unit of work:
	// one refresh covers every dead album of that artist at once.
	Artists int `json:"artists_refreshed"`
	// Repaired is candidates the manager stopped listing afterwards. Those rows are now
	// retirable without ambiguity; this pass does not retire them.
	Repaired int      `json:"repaired"`
	Skipped  int      `json:"skipped_in_cooldown"`
	Failures []string `json:"failures,omitempty"`
}

// RepairGhostReleaseGroups asks each manager to refresh the artists holding albums
// whose MusicBrainz ID no longer resolves, then re-mirrors those artists.
//
// The sequence is the point and the order is not negotiable: refresh, wait for the
// command to finish, re-sync. Re-syncing before the refresh lands mirrors the same
// stale catalog back and the pass concludes nothing changed; retiring before either
// deletes the row that was about to be corrected.
func RepairGhostReleaseGroups(db *gorm.DB) (RepairStats, error) {
	var stats RepairStats
	if db == nil {
		return stats, nil
	}

	ghosts, err := GhostReleaseGroups(db)
	if err != nil {
		return stats, err
	}
	stats.Candidates = len(ghosts)
	if len(ghosts) == 0 {
		return stats, nil
	}

	// One refresh per artist, not per album. A single artist held eight dead IDs on the
	// instance this was built against; asking eight times would be eight times the load
	// on the manager's metadata service for one answer.
	artists, err := artistsHoldingGhosts(db, ghosts)
	if err != nil {
		return stats, err
	}

	var managers []models.Manager
	if err := db.Where("type = ? AND enabled = ?", models.ManagerTypeLidarr, true).
		Find(&managers).Error; err != nil {
		return stats, err
	}

	for _, m := range managers {
		if m.LidarrSkipArtistRefresh {
			logger.Log.Debugf("manager %q has artist refresh turned off; not repairing through it", m.Name)
			continue
		}
		if strings.TrimSpace(m.LidarrBaseURL) == "" || strings.TrimSpace(m.LidarrAPIKey) == "" {
			continue
		}
		cookie := m.LidarrHeaderCookie
		client := modules.NewLidarrClient(m.LidarrBaseURL, m.LidarrAPIKey, &cookie)

		lidarrArtists, err := client.GetArtists()
		if err != nil {
			stats.Failures = append(stats.Failures, fmt.Sprintf("%s: %s", m.Name, err.Error()))
			continue
		}
		byMBID := map[string]models.LidarrArtist{}
		for _, la := range lidarrArtists {
			if la.ForeignArtistID != "" {
				byMBID[la.ForeignArtistID] = la
			}
		}

		for _, artistMBID := range artists {
			la, ok := byMBID[artistMBID]
			if !ok {
				continue
			}
			if inCooldown(db, artistMBID) {
				stats.Skipped++
				continue
			}
			if err := refreshAndResync(db, client, la, artistMBID); err != nil {
				stats.Failures = append(stats.Failures, fmt.Sprintf("%s: %s", la.Name, err.Error()))
				// The attempt is still stamped: a manager that errors on every pass must
				// not turn into a refresh on every pass.
			}
			markRepairAttempted(db, artistMBID, ghosts)
			stats.Artists++
		}
	}

	// Recount rather than infer. A refresh can repair an album, remove it, or leave it
	// exactly as it was, and only the catalog state after the re-sync says which.
	remaining, err := GhostReleaseGroups(db)
	if err != nil {
		return stats, err
	}
	still := map[string]bool{}
	for _, id := range remaining {
		still[id] = true
	}
	for _, id := range ghosts {
		if !still[id] {
			stats.Repaired++
		}
	}
	if stats.Repaired > 0 {
		logger.Log.Infof("manager refresh resolved %d album(s) whose MusicBrainz ID no longer resolved", stats.Repaired)
	}
	return stats, nil
}

// refreshAndResync performs the ordered part: ask, wait, re-mirror.
func refreshAndResync(db *gorm.DB, client *modules.LidarrClient, la models.LidarrArtist, artistMBID string) error {
	logger.Log.Infof("asking Lidarr to refresh %q, which holds albums whose MusicBrainz IDs do not resolve", la.Name)

	commandID, err := client.RefreshArtist(la.ID)
	if err != nil {
		return fmt.Errorf("requesting refresh: %w", err)
	}

	finished, err := client.WaitForCommand(commandID)
	if err != nil {
		return fmt.Errorf("waiting for refresh: %w", err)
	}
	if !finished {
		// Not an error, and deliberately not followed by a re-sync: mirroring a catalog
		// mid-refresh records a half-updated view as though it were settled. The next
		// pass reads the finished result.
		return nil
	}

	// Scoped to the artist just refreshed, and with the cached Lidarr responses dropped
	// — the whole point is to read what the refresh changed, and an hour-old cached
	// album list is exactly what must not be believed here.
	if _, err := SyncLidarrWith(db, SyncOptions{ArtistMBID: artistMBID, IgnoreCache: true}); err != nil {
		return fmt.Errorf("re-syncing after refresh: %w", err)
	}
	return nil
}

// artistsHoldingGhosts maps the ghost release-groups back to the artists to refresh,
// deduplicated and ordered so a pass is reproducible.
func artistsHoldingGhosts(db *gorm.DB, ghosts []string) ([]string, error) {
	var rows []models.CollectionReleaseGroup
	if err := db.Select("artist_mb_id").Where("mb_id IN ?", ghosts).Find(&rows).Error; err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.ArtistMBID != "" && !seen[r.ArtistMBID] {
			seen[r.ArtistMBID] = true
			out = append(out, r.ArtistMBID)
		}
	}
	sort.Strings(out)
	return out, nil
}

// inCooldown reports whether this artist's ghosts were all attempted recently. Keyed on
// the migration rows rather than on the artist, because that is where the attempt is
// already recorded and it survives the release-group row being retired.
func inCooldown(db *gorm.DB, artistMBID string) bool {
	var oldest *time.Time
	var rows []models.MusicbrainzMigration
	err := db.Joins("JOIN collection_release_groups ON collection_release_groups.mb_id = musicbrainz_migrations.old_mb_id").
		Where("musicbrainz_migrations.entity_type = ?", models.MigrationEntityReleaseGroup).
		Where("collection_release_groups.artist_mb_id = ?", artistMBID).
		Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return false
	}
	for _, r := range rows {
		if r.RepairAttemptedAt == nil {
			// One never-attempted ghost is reason enough to refresh the artist: the
			// refresh covers all of them at once.
			return false
		}
		if oldest == nil || r.RepairAttemptedAt.Before(*oldest) {
			oldest = r.RepairAttemptedAt
		}
	}
	return oldest != nil && time.Since(*oldest) < repairCooldown
}

// markRepairAttempted stamps this artist's ghost rows so a failed or ineffective
// refresh is not repeated on the next pass. Recorded whatever the outcome — the stamp
// means "asked", not "worked".
func markRepairAttempted(db *gorm.DB, artistMBID string, ghosts []string) {
	now := time.Now()
	var ids []string
	if err := db.Model(&models.CollectionReleaseGroup{}).
		Where("artist_mb_id = ? AND mb_id IN ?", artistMBID, ghosts).
		Pluck("mb_id", &ids).Error; err != nil {
		logger.Log.Warnf("failed to list ghost albums for %s: %s", artistMBID, err.Error())
		return
	}
	if len(ids) == 0 {
		return
	}
	if err := db.Model(&models.MusicbrainzMigration{}).
		Where("entity_type = ? AND old_mb_id IN ?", models.MigrationEntityReleaseGroup, ids).
		Update("repair_attempted_at", now).Error; err != nil {
		logger.Log.Warnf("failed to record repair attempt for %s: %s", artistMBID, err.Error())
	}
}

package collection

import (
	"errors"
	"fmt"
	"strings"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Detaching a manager is a change of authority, not a loss of data.
//
// It is only possible because the manager's selections are already Autotaggerr's own
// rows: reconcileManagerDesires records what Lidarr monitors as desire rows carrying
// source = manager, and the correlations live in library_items. So handing authority
// back means re-labelling what is already stored, not re-deriving anything.
//
// Three things change, and deliberately nothing else:
//
//   - ManagedBy is held at native, which is what makes SyncLidarr and
//     reconcileManagerDesires skip the artist — the manager stops being asked about it
//     and stops maintaining its rows.
//   - The manager's wants become manual, so they survive. They must: those passes only
//     ever prune rows they own, and a manager row on an artist no manager governs is
//     exactly what reconcileManagerDesires deletes on its next run.
//   - Following is switched off.
//
// The last one is the non-obvious one. Following is *stored* under a manager but does
// not govern (see FollowGoverns), so a Lidarr-managed artist can be carrying a stale
// Monitored flag set before it was ever managed. Detaching makes following govern
// again, so leaving that flag alone would turn a detach into "and also auto-want the
// entire back catalogue" — an effect the user never asked for, from a control they
// cannot see on the page. Following is offered right there afterwards if they want it.
//
// What detach does *not* do is invent follow settings from what the manager wanted.
// Lidarr monitors per album, not by rule, so there is no rule to recover: any
// follow-types guess would be fabricating intent that was never expressed.

// DetachResult reports what a detach changed. WantsKept is the headline — it is the
// count that says the manager's decisions survived it.
type DetachResult struct {
	Artist models.CollectionArtist `json:"artist"`
	// WantsKept is how many mirrored wants became the user's own.
	WantsKept int `json:"wants_kept"`
	// FollowCleared reports that a stale follow flag was switched off, so the caller
	// can say so rather than leaving the user to notice the toggle moved.
	FollowCleared bool `json:"follow_cleared"`
}

// ErrNotManaged is returned when there is no authority to take back.
var ErrNotManaged = errors.New("this artist is not managed by a manager")

// Detachable reports whether the detach verb applies to an artist: a manager governs
// it now, and it has not already been detached.
//
// ManagedByUnknown is deliberately excluded. It does not mean "a manager owns this",
// it means the library's manager could not be resolved — and the fix for that is to
// reassign a manager on the Libraries page, not to claim the artist natively and
// paper over a misconfiguration. The stranded-want case that state produces is
// handled where it is created instead: DetachManagerArtists runs *before* the manager
// row goes away, so the artist never reaches unknown in the first place.
func Detachable(artist models.CollectionArtist) bool {
	if artist.ManagerDetached {
		return false
	}
	return artist.ManagedBy == models.ManagedByLidarr || artist.ManagedBy == models.ManagedByMixed
}

// DetachArtist takes authority over an artist back from its library's manager,
// keeping what the manager decided. Idempotent: an artist already detached is
// returned unchanged rather than treated as an error, so a double-click cannot
// re-clear a follow flag the user has since switched back on.
func DetachArtist(db *gorm.DB, artistMBID string) (DetachResult, error) {
	artistMBID = strings.TrimSpace(artistMBID)
	if artistMBID == "" {
		return DetachResult{}, errors.New("an artist MusicBrainz ID is required")
	}

	var artist models.CollectionArtist
	if err := db.Where("mb_id = ?", artistMBID).First(&artist).Error; err != nil {
		return DetachResult{}, err
	}
	if artist.ManagerDetached {
		return DetachResult{Artist: artist}, nil
	}
	if !Detachable(artist) {
		return DetachResult{}, ErrNotManaged
	}

	result := DetachResult{FollowCleared: artist.Monitored}
	err := db.Transaction(func(tx *gorm.DB) error {
		// The wants first: until they are re-labelled they belong to a manager that
		// is about to stop governing the artist, and a failure after the artist row
		// moved would leave them to be pruned as orphans by the next sync.
		res := tx.Model(&models.CollectionDesire{}).
			Where("artist_mb_id = ? AND source = ?", artistMBID, models.DesireSourceManager).
			Update("source", models.DesireSourceManual)
		if res.Error != nil {
			return res.Error
		}
		result.WantsKept = int(res.RowsAffected)

		return tx.Model(&models.CollectionArtist{}).Where("mb_id = ?", artistMBID).
			Updates(map[string]any{
				"manager_detached": true,
				"managed_by":       models.ManagedByAutotaggerr,
				"monitored":        false,
			}).Error
	})
	if err != nil {
		return DetachResult{}, err
	}

	if err := db.Where("mb_id = ?", artistMBID).First(&result.Artist).Error; err != nil {
		return DetachResult{}, err
	}
	logger.Log.Infof("detached artist %s from its manager, keeping %d want(s)", artistMBID, result.WantsKept)
	return result, nil
}

// ReattachArtist gives the artist back to whatever manages its libraries, by clearing
// the override and re-deriving provenance immediately — the next Rebuild would do the
// same, but a control whose effect only shows up after the next scan is not one a user
// can tell worked.
//
// It is not a perfect inverse, and that is the safe direction: wants that detach made
// manual stay manual. Re-labelling them back would hand rows the user has since edited
// to a pass that may prune or re-point them. reconcileManagerDesires already treats a
// hand-authored want as a veto, so the manager simply leaves those albums alone.
func ReattachArtist(db *gorm.DB, artistMBID string) (models.CollectionArtist, error) {
	artistMBID = strings.TrimSpace(artistMBID)
	if artistMBID == "" {
		return models.CollectionArtist{}, errors.New("an artist MusicBrainz ID is required")
	}

	var artist models.CollectionArtist
	if err := db.Where("mb_id = ?", artistMBID).First(&artist).Error; err != nil {
		return artist, err
	}
	if !artist.ManagerDetached {
		return artist, nil
	}

	managedBy, err := deriveArtistManager(db, artistMBID)
	if err != nil {
		return artist, err
	}
	if err := db.Model(&models.CollectionArtist{}).Where("mb_id = ?", artistMBID).
		Updates(map[string]any{"manager_detached": false, "managed_by": managedBy}).Error; err != nil {
		return artist, err
	}

	if err := db.Where("mb_id = ?", artistMBID).First(&artist).Error; err != nil {
		return artist, err
	}
	logger.Log.Infof("reattached artist %s to its manager (%s)", artistMBID, managedBy)
	return artist, nil
}

// DetachManagerArtists detaches every artist a manager currently governs. It runs when
// that manager is about to be deleted, and it is what keeps deletion from stranding the
// manager's decisions.
//
// Without it, deleting a manager leaves its libraries pointing at a row that is gone,
// so the artists fall to ManagedByUnknown while their mirrored wants keep a `manager`
// provenance naming an authority that no longer exists. Nothing reconciles those:
// SyncLidarr returns early when no enabled Lidarr manager is configured, and that early
// return is the right behaviour — a reconcile with zero managers cannot tell "Lidarr
// unmonitored this album" from "Lidarr is gone", and would delete the decisions rather
// than keep them. So the fix belongs here, before the information is lost.
//
// The Manager row itself is another matter, and the answer is that draining it changes
// nothing about it. A manager is configuration owned by *libraries* — a base URL and a
// credential — not a container of artists. Deleting one because its last artist walked
// away would throw away what the user configured, and it would be wrong on its own
// terms: its libraries are still its, so the next file to appear in one is managed by
// it again. An empty manager is idle, not obsolete.
func DetachManagerArtists(db *gorm.DB, managerID uuid.UUID) (int, error) {
	var libraries []models.Library
	if err := db.Where("manager_id = ?", managerID).Find(&libraries).Error; err != nil {
		return 0, err
	}
	if len(libraries) == 0 {
		return 0, nil
	}
	mine := make(map[uuid.UUID]bool, len(libraries))
	for _, l := range libraries {
		mine[l.ID] = true
	}

	artists, err := artistsInLibraries(db, mine)
	if err != nil {
		return 0, err
	}

	detached := 0
	for _, mbID := range artists {
		var artist models.CollectionArtist
		if err := db.Where("mb_id = ?", mbID).First(&artist).Error; err != nil {
			continue
		}
		if !Detachable(artist) {
			continue
		}
		if _, err := DetachArtist(db, mbID); err != nil {
			// Logged and skipped, like the rest of the per-row writes here: one artist
			// that cannot be detached must not abandon the others, whose wants would
			// then be the ones left stranded.
			logger.Log.Warnf("failed to detach artist %s from manager %s: %s", mbID, managerID, err.Error())
			continue
		}
		detached++
	}
	return detached, nil
}

// artistsInLibraries lists the MusicBrainz artists credited on the albums whose files
// live in the given libraries, sorted so the caller's work is deterministic.
func artistsInLibraries(db *gorm.DB, libraries map[uuid.UUID]bool) ([]string, error) {
	rows, err := ownedItemRows(db)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []string{}
	for _, r := range rows {
		if !libraries[r.LibraryID] {
			continue
		}
		release, ok := modules.CachedRelease(r.MBReleaseID)
		if !ok {
			continue
		}
		for _, credit := range albumArtistCredit(release) {
			if credit.Artist.ID == "" || seen[credit.Artist.ID] {
				continue
			}
			seen[credit.Artist.ID] = true
			out = append(out, credit.Artist.ID)
		}
	}
	return out, nil
}

// deriveArtistManager answers "who would manage this artist if nothing were overridden"
// — the same question Rebuild answers for every artist at once, asked for one.
//
// An artist whose files are nowhere gets the native answer rather than unknown: no
// library governs it, so the native manager is the only thing that could, which is what
// AddArtist already says about an artist added before any of its files exist.
func deriveArtistManager(db *gorm.DB, artistMBID string) (string, error) {
	libraryManager, err := libraryManagerTypes(db)
	if err != nil {
		return "", err
	}
	rows, err := ownedItemRows(db)
	if err != nil {
		return "", err
	}

	mgrs := map[string]bool{}
	for _, r := range rows {
		release, ok := modules.CachedRelease(r.MBReleaseID)
		if !ok {
			continue
		}
		for _, credit := range albumArtistCredit(release) {
			if credit.Artist.ID != artistMBID {
				continue
			}
			mt := libraryManager[r.LibraryID]
			if mt == "" {
				// The item points at a library that no longer exists.
				mt = models.ManagedByUnknown
			}
			mgrs[mt] = true
			break
		}
	}
	if len(mgrs) == 0 {
		return models.ManagedByAutotaggerr, nil
	}
	return managedByLabel(mgrs), nil
}

// itemRow is the part of a correlated file that provenance is derived from: which
// release it is, and which library it sits in.
type itemRow struct {
	MBReleaseID string
	LibraryID   uuid.UUID
}

// ownedItemRows lists every correlated file in the index.
//
// Correlated, not *successfully processed*: the disk view answers "what is on disk",
// and a file is on disk whether or not the last attempt to tag it worked. This used
// to require status = ok, which meant any failure — MusicBrainz unreachable, metaflac
// refusing to write, a permission error — took the file out of the collection. A
// scan interrupted by an outage therefore emptied whole albums, which then reported
// `not_indexed` against a manager that could see the files perfectly well.
//
// Unmatched is the one status still excluded, and for the opposite reason: it is not
// a failed attempt at all, it is the manager saying it does not know this file. There
// is no identity to aggregate against, only a stale one left over from before, and
// counting a file the manager has disowned would put the album back in the collection
// on the strength of an answer that has been withdrawn.
func ownedItemRows(db *gorm.DB) ([]itemRow, error) {
	var rows []itemRow
	err := db.Model(&models.LibraryItem{}).
		Where("status <> ? AND mb_release_id <> ''", models.LibraryItemStatusUnmatched).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to read the library index: %w", err)
	}
	return rows, nil
}

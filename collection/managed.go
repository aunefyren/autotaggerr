package collection

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
)

// Managed and present-only artists.
//
// Every artist an owned album credits gets a collection row, so a collaboration brings
// its second artist in too. That is right for ownership, which is what the row is for,
// and wrong for "who are my artists": a library with a few hundred albums has as many
// co-credits again, most of them names the owner would not recognise. Lidarr answers
// the question by making an artist something you *add*. Here the rows already exist,
// so the answer is a rule over them instead: an artist is **managed** when something
// the user did, or the album they own, says so, and **present-only** otherwise.
//
// The rule is derived on read, never stored. Its inputs — wants, follow settings, the
// quality override, the manager's listing — change outside Rebuild, and a stored flag
// would need every one of those writers to remember to re-derive it. The one stored
// input is the user's own override (models.CollectionArtist.ManageOverride).

// Why an artist is managed. Empty means present-only.
const (
	ManagedAdded       = "added"        // the user chose to manage them
	ManagedManual      = "manual"       // added by hand from search
	ManagedFollowed    = "followed"     // followed
	ManagedWanted      = "wanted"       // something of theirs is wanted
	ManagedLidarr      = "lidarr"       // an enabled Lidarr lists them
	ManagedAlbumArtist = "album_artist" // the first-credited artist of an album you own
	ManagedSettings    = "settings"     // their follow or quality settings were changed
)

// ManagedFacts are the inputs to ManagedReason that do not live on the artist row.
type ManagedFacts struct {
	HasDesire    bool
	LidarrListed bool
	AlbumArtist  bool
}

// ManagedReason is the single definition of a managed artist: it returns why one is,
// or "" for an artist present only because files credit them. Earlier reasons win, so
// the reason shown is the most deliberate one.
func ManagedReason(a models.CollectionArtist, f ManagedFacts) string {
	switch a.ManageOverride {
	case models.ManageOn:
		return ManagedAdded
	case models.ManageOff:
		return ""
	}
	switch {
	case a.Origin == models.CollectionOriginManual:
		return ManagedManual
	case a.Monitored:
		return ManagedFollowed
	case f.HasDesire:
		return ManagedWanted
	case f.LidarrListed:
		return ManagedLidarr
	case f.AlbumArtist:
		return ManagedAlbumArtist
	case customised(a):
		return ManagedSettings
	}
	return ""
}

// customised reports whether anyone changed this artist's settings from the defaults.
// Setting something up for an artist is as much a statement as following them.
func customised(a models.CollectionArtist) bool {
	types := strings.TrimSpace(a.FollowTypes)
	return (types != "" && types != models.DefaultFollowTypes) ||
		a.FollowSecondary || a.FollowFromYear != 0 || len(a.FollowLibraryIDs) > 0 ||
		a.QualityProfileID != nil || a.ManagerDetached
}

// isAlbumArtistOf reports whether being the first-credited artist of an owned album
// with these secondary types makes someone an album artist. A compilation does not:
// its credit is whoever compiled it, which is not a statement about whose music it
// holds. Various Artists is excluded by the callers, by MBID.
func isAlbumArtistOf(secondaryCSV string) bool {
	for _, t := range splitTypes(secondaryCSV) {
		if strings.EqualFold(t, "Compilation") {
			return false
		}
	}
	return true
}

// managedInputs is everything ManagedReason needs beyond the artist row, for the
// artists named (or every artist, when mbids is nil).
type managedInputs struct {
	desire, lidarr, albumArtist map[string]bool
}

func (in managedInputs) facts(mbid string) ManagedFacts {
	return ManagedFacts{HasDesire: in.desire[mbid], LidarrListed: in.lidarr[mbid], AlbumArtist: in.albumArtist[mbid]}
}

func readManagedInputs(db *gorm.DB, mbids []string) (managedInputs, error) {
	in := managedInputs{desire: map[string]bool{}, lidarr: map[string]bool{}, albumArtist: map[string]bool{}}
	narrow := func(q *gorm.DB, column string) *gorm.DB {
		if mbids != nil {
			return q.Where(column+" IN ?", mbids)
		}
		return q
	}

	var desired []string
	if err := narrow(db.Model(&models.CollectionDesire{}), "artist_mb_id").
		Distinct().Pluck("artist_mb_id", &desired).Error; err != nil {
		return in, fmt.Errorf("read wanted artists: %w", err)
	}
	for _, id := range desired {
		in.desire[id] = true
	}

	// The manager's own listing, not ManagedBy: ManagedBy says which library an
	// artist's files sit in, so every co-credit on an album in a Lidarr library reads
	// "lidarr". A link exists only for an artist Lidarr actually listed. A disabled or
	// deleted manager's links linger, so the join keeps them out.
	var listed []string
	if err := narrow(db.Table("manager_links").
		Joins("JOIN managers ON managers.id = manager_links.manager_id").
		Where("manager_links.entity_type = ? AND managers.enabled = ? AND managers.type = ?",
			models.ManagerLinkArtist, true, models.ManagerTypeLidarr), "manager_links.mb_id").
		Distinct().Pluck("manager_links.mb_id", &listed).Error; err != nil {
		return in, fmt.Errorf("read Lidarr's artists: %w", err)
	}
	for _, id := range listed {
		in.lidarr[id] = true
	}

	// The primary-credit column, not the link table's position: catalog writers link
	// at position 0 whichever artist they sync, and only Rebuild writes artist_mb_id
	// from the album's real credit.
	var owned []struct {
		ArtistMBID     string `gorm:"column:artist_mb_id"`
		SecondaryTypes string `gorm:"column:secondary_types"`
	}
	if err := narrow(db.Model(&models.CollectionReleaseGroup{}).
		Where("owned = ? AND artist_mb_id <> ''", true), "artist_mb_id").
		Select("artist_mb_id", "secondary_types").Scan(&owned).Error; err != nil {
		return in, fmt.Errorf("read album artists: %w", err)
	}
	for _, rg := range owned {
		if !models.IsVariousArtists(rg.ArtistMBID) && isAlbumArtistOf(rg.SecondaryTypes) {
			in.albumArtist[rg.ArtistMBID] = true
		}
	}
	return in, nil
}

// ManagedArtists returns every managed artist's MBID with the reason. An artist not in
// the map is present-only. Three queries however large the collection.
func ManagedArtists(db *gorm.DB) (map[string]string, error) {
	in, err := readManagedInputs(db, nil)
	if err != nil {
		return nil, err
	}
	var artists []models.CollectionArtist
	if err := db.Find(&artists).Error; err != nil {
		return nil, err
	}
	out := make(map[string]string, len(artists))
	for _, a := range artists {
		if r := ManagedReason(a, in.facts(a.MBID)); r != "" {
			out[a.MBID] = r
		}
	}
	return out, nil
}

// ManagedReasonFor is ManagedReason for one artist row, reading its facts.
func ManagedReasonFor(db *gorm.DB, a models.CollectionArtist) (string, error) {
	in, err := readManagedInputs(db, []string{a.MBID})
	if err != nil {
		return "", err
	}
	return ManagedReason(a, in.facts(a.MBID)), nil
}

// ErrManagedByIntent refuses to stop managing an artist the user still follows or
// wants something from: their wanted albums would stay on the missing list under an
// artist the Managed view no longer shows.
var ErrManagedByIntent = errors.New("this artist is followed or has albums picked — unfollow them and clear the picks first")

// SetManaged records the user's answer to "is this one of my artists?". Managing an
// artist queues their discography read, which present-only artists are skipped by.
func SetManaged(db *gorm.DB, mbid string, on bool) (models.CollectionArtist, error) {
	var a models.CollectionArtist
	if err := db.Where("mb_id = ?", mbid).First(&a).Error; err != nil {
		return a, err
	}
	value := models.ManageOn
	if !on {
		if a.Monitored {
			return a, ErrManagedByIntent
		}
		var manual int64
		if err := db.Model(&models.CollectionDesire{}).
			Where("artist_mb_id = ? AND source = ?", mbid, models.DesireSourceManual).
			Count(&manual).Error; err != nil {
			return a, err
		}
		if manual > 0 {
			return a, ErrManagedByIntent
		}
		value = models.ManageOff
	}
	if err := db.Model(&a).Update("manage_override", value).Error; err != nil {
		return a, err
	}
	a.ManageOverride = value
	if on {
		readNewArtistCatalogue()
	}
	return a, nil
}

// followCleanupMigration names the one-time UnfollowPolicyCoCredits pass in
// data_migrations.
const followCleanupMigration = "unfollow_policy_cocredits"

// UnfollowPolicyCoCredits stops following the artists the new-artist policy followed
// only because a collaboration credited them, once per install.
//
// Until managed artists existed, the policy (applyNewArtistPolicy) followed every
// artist Rebuild created, and Rebuild creates one for every artist an album credits.
// With the policy on, every co-credit was followed and wanted their discography. The
// policy now follows album artists only; this pass takes back what it did before.
//
// Which follows the policy made is not recorded, so the pass takes the shape it left:
// an artist found in files (not added by hand), followed, with no override, no album
// of their own, no pick by hand and no Lidarr listing. The follow settings the policy
// wrote go back to the defaults too, since they are the policy's and not the user's
// — and left in place they would make the artist managed through ManagedSettings.
// Every artist unfollowed is listed on a follow_cleanup activity, so the change is
// visible and can be undone by hand.
//
// Called from main after the data migrations, not from database.ApplyDataMigrations:
// the database package cannot import collection.
func UnfollowPolicyCoCredits(db *gorm.DB) error {
	var done models.DataMigration
	if err := db.Where("name = ?", followCleanupMigration).Limit(1).Find(&done).Error; err != nil {
		return err
	}
	if done.Name != "" {
		return nil
	}

	var unfollowed []models.CollectionArtist
	err := db.Transaction(func(tx *gorm.DB) error {
		var followed []models.CollectionArtist
		if err := tx.Where("monitored = ? AND origin = ? AND (manage_override = '' OR manage_override IS NULL)",
			true, models.CollectionOriginLibrary).Find(&followed).Error; err != nil {
			return err
		}
		if len(followed) > 0 {
			ids := make([]string, len(followed))
			for i, a := range followed {
				ids[i] = a.MBID
			}
			in, err := readManagedInputs(tx, ids)
			if err != nil {
				return err
			}
			var picked []string
			if err := tx.Model(&models.CollectionDesire{}).
				Where("artist_mb_id IN ? AND source = ?", ids, models.DesireSourceManual).
				Distinct().Pluck("artist_mb_id", &picked).Error; err != nil {
				return err
			}
			hasPick := map[string]bool{}
			for _, id := range picked {
				hasPick[id] = true
			}
			for _, a := range followed {
				if in.albumArtist[a.MBID] || in.lidarr[a.MBID] || hasPick[a.MBID] {
					continue
				}
				if err := tx.Model(&models.CollectionArtist{}).Where("mb_id = ?", a.MBID).
					Updates(map[string]any{
						"monitored": false, "follow_types": "", "follow_secondary": false, "follow_from_year": 0,
					}).Error; err != nil {
					return err
				}
				unfollowed = append(unfollowed, a)
			}
		}
		return tx.Create(&models.DataMigration{Name: followCleanupMigration, AppliedAt: time.Now()}).Error
	})
	if err != nil {
		return err
	}
	if len(unfollowed) == 0 {
		return nil
	}

	logger.Log.Infof("unfollowed %d artist(s) the new-artist policy followed only because a collaboration credited them", len(unfollowed))
	ev := events.Begin(db, models.EventTypeFollowCleanup, "Unfollowed co-credited artists")
	items := make([]models.EventItem, len(unfollowed))
	for i, a := range unfollowed {
		items[i] = models.EventItem{
			Path: a.MBID, Kind: models.EventItemKindEntity,
			Status: models.EventItemStatusUnfollowed, Phase: models.EventItemPhaseArtists,
		}
	}
	events.AddItems(db, ev, items)
	events.Finish(db, ev, models.EventStatusOK,
		fmt.Sprintf("%d artist(s) unfollowed", len(unfollowed)),
		map[string]any{"unfollowed": len(unfollowed)})
	return nil
}

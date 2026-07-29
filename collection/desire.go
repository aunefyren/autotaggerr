package collection

import (
	"errors"
	"strings"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// AddArtist puts an artist into the collection before any of their files are owned,
// which Rebuild alone can never do — it only ever materialises artists from files on
// disk. The row is marked manual so it is not mistaken for a stale library artist.
//
// Adding is idempotent: an artist already present (however they got there) is
// returned unchanged, so re-adding never resets monitoring or provenance.
func AddArtist(db *gorm.DB, mbID, name string) (models.CollectionArtist, error) {
	mbID = strings.TrimSpace(mbID)
	if mbID == "" {
		return models.CollectionArtist{}, errors.New("an artist MusicBrainz ID is required")
	}

	var existing models.CollectionArtist
	if err := db.Where("mb_id = ?", mbID).First(&existing).Error; err == nil {
		return existing, nil
	}

	artist := models.CollectionArtist{
		MBID: mbID,
		Name: strings.TrimSpace(name),
		// A manually added artist owns nothing yet, so no library governs it; the
		// native manager is the only thing that could.
		ManagedBy: models.ManagedByAutotaggerr,
		Origin:    models.CollectionOriginManual,
	}
	if err := db.Create(&artist).Error; err != nil {
		return artist, err
	}
	return artist, nil
}

// OwnedReleases lists the editions of a release-group that files are owned of,
// best-owned first. Empty is the normal case for an album you do not have.
func OwnedReleases(db *gorm.DB, releaseGroupMBID string) ([]models.CollectionRelease, error) {
	var out []models.CollectionRelease
	err := db.Where("release_group_mb_id = ?", releaseGroupMBID).
		Order("owned_tracks desc, date").Find(&out).Error
	return out, err
}

// OwnedReleaseCounts returns, per release-group, how many distinct editions are
// owned. Loaded in one query for a whole artist so the discography list does not
// issue a query per row.
func OwnedReleaseCounts(db *gorm.DB, artistMBID string) (map[string]int, error) {
	var rows []models.CollectionRelease
	if err := db.Where("artist_mb_id = ?", artistMBID).Find(&rows).Error; err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, r := range rows {
		counts[r.ReleaseGroupMBID]++
	}
	return counts, nil
}

// DesireInput describes something the user asked for. The release-group metadata is
// carried so the desired album can be displayed even when nothing else in the
// collection knows about it — the caller already has it from the discography list,
// so passing it costs nothing and avoids a MusicBrainz round-trip.
type DesireInput struct {
	ArtistMBID       string
	ReleaseGroupMBID string
	// ReleaseMBID empty means "any release of the group will do" — the default and
	// most common case. Set it to demand one specific edition.
	ReleaseMBID string

	// RecordingMBIDs narrows the want to specific songs. Empty means the whole
	// album or edition. Recordings, not release-scoped track IDs, so a song want
	// survives without a release having been chosen.
	RecordingMBIDs []string

	Title            string
	PrimaryType      string
	SecondaryTypes   string
	FirstReleaseDate string
}

// SetDesire records an explicit want, idempotently.
//
// It deliberately bypasses the follow-type filter: that filter exists so *automatic*
// discography discovery does not bury the missing list under singles, live albums
// and compilations. An explicit request is not a guess, so filtering it out would
// mean silently refusing to keep something the user just asked for.
//
// Besides the desire row it upserts the release-group's catalog state, so a desired
// album appears in the collection even when no file and no manager mentions it.
func SetDesire(db *gorm.DB, in DesireInput) (models.CollectionDesire, error) {
	in.ArtistMBID = strings.TrimSpace(in.ArtistMBID)
	in.ReleaseGroupMBID = strings.TrimSpace(in.ReleaseGroupMBID)
	in.ReleaseMBID = strings.TrimSpace(in.ReleaseMBID)

	var desire models.CollectionDesire
	if in.ArtistMBID == "" || in.ReleaseGroupMBID == "" {
		return desire, errors.New("an artist and release-group MusicBrainz ID are required")
	}

	upsertReleaseGroup(db, rgWrite{
		mbID: in.ReleaseGroupMBID, artistMBID: in.ArtistMBID, title: in.Title,
		primary: in.PrimaryType, secondary: in.SecondaryTypes, date: in.FirstReleaseDate,
		catalog: &catalogState{},
	})

	// Within one release-group a desire is *either* "any release will do" *or* a set
	// of specific editions — never both, because holding both is a contradiction the
	// UI cannot render and the user cannot have meant. Setting one clears the other.
	if in.ReleaseMBID == "" {
		if err := db.Where("release_group_mb_id = ? AND release_mb_id <> ''", in.ReleaseGroupMBID).
			Delete(&models.CollectionDesire{}).Error; err != nil {
			return desire, err
		}
	} else {
		if err := db.Where("release_group_mb_id = ? AND release_mb_id = ''", in.ReleaseGroupMBID).
			Delete(&models.CollectionDesire{}).Error; err != nil {
			return desire, err
		}
	}

	// (release-group, release) identifies a desire: asking twice for the same thing
	// is the same want, not two. Re-asking updates the track selection.
	err := db.Where("release_group_mb_id = ? AND release_mb_id = ?", in.ReleaseGroupMBID, in.ReleaseMBID).
		First(&desire).Error
	if err == nil {
		// Save, not Update: the recordings column goes through GORM's json
		// serializer, and a column-level Update writes the raw Go slice instead.
		desire.RecordingMBIDs = in.RecordingMBIDs
		if err := db.Save(&desire).Error; err != nil {
			return desire, err
		}
		return desire, nil
	}

	desire = models.CollectionDesire{
		ArtistMBID:       in.ArtistMBID,
		ReleaseGroupMBID: in.ReleaseGroupMBID,
		ReleaseMBID:      in.ReleaseMBID,
		RecordingMBIDs:   in.RecordingMBIDs,
	}
	if err := db.Create(&desire).Error; err != nil {
		return desire, err
	}
	return desire, nil
}

// ClearDesire removes a want. Nothing owned is touched: dropping a desire says "I no
// longer want the parts I do not have", never "delete what I have".
func ClearDesire(db *gorm.DB, releaseGroupMBID, releaseMBID string) error {
	return db.Where("release_group_mb_id = ? AND release_mb_id = ?",
		strings.TrimSpace(releaseGroupMBID), strings.TrimSpace(releaseMBID)).
		Delete(&models.CollectionDesire{}).Error
}

// DesiresForArtist lists an artist's explicit wants, so the UI can show which
// release-groups (and which specific editions) were asked for.
func DesiresForArtist(db *gorm.DB, artistMBID string) ([]models.CollectionDesire, error) {
	var out []models.CollectionDesire
	err := db.Where("artist_mb_id = ?", strings.TrimSpace(artistMBID)).
		Order("release_group_mb_id").Find(&out).Error
	return out, err
}

// ReleaseGroupEditions lists every release of a release-group from MusicBrainz, for
// choosing a specific edition to desire. This is catalog data, not ownership — the
// owned-editions view arrives with M6 pass C.
func ReleaseGroupEditions(releaseGroupMBID string) ([]models.MusicBrainzReleaseSearchResult, error) {
	return modules.GetMusicBrainzReleaseGroupReleases(releaseGroupMBID)
}

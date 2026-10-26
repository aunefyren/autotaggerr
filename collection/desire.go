package collection

import (
	"errors"
	"strings"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/metadata"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
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
	if err := requireNativeLibrary(db); err != nil {
		return models.CollectionArtist{}, err
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
	// The *Add artist* half of the create hook. Their release-groups do not exist
	// yet — the discography sync creates those, and notifies from upsertReleaseGroup
	// when it does — so this warms the portrait and backdrop only.
	warmArtistArtwork(mbID)
	readNewArtistCatalogue()
	return artist, nil
}

// ErrAllLibrariesManaged: every library is one Lidarr decides for, so an artist added
// by hand could be wanted but never fulfilled — Import and Downloads cannot write into
// a Lidarr library, and Lidarr will not act on a want it never saw.
var ErrAllLibrariesManaged = errors.New("every library is managed by Lidarr — add the artist in Lidarr; their files bring them here")

// requireNativeLibrary refuses a hand-added artist when libraries are configured and
// none of them is one Autotaggerr decides for. An install with no library yet is
// still being set up, and is let through.
func requireNativeLibrary(db *gorm.DB) error {
	idx, err := LoadLibraryIndex(db)
	if err != nil {
		return err
	}
	if len(idx.libraries) == 0 || idx.HasNativeLibrary() {
		return nil
	}
	return ErrAllLibrariesManaged
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
// owned. Loaded in one query for a whole page so the discography list does not issue
// a query per row.
//
// Keyed by release-group, not by artist: an edition of a collaboration is stored
// under its primary credit, so counting per artist reported zero owned editions on
// the other credited artist's page while the release itself was plainly owned.
func OwnedReleaseCounts(db *gorm.DB, releaseGroupMBIDs []string) (map[string]int, error) {
	counts := map[string]int{}
	if len(releaseGroupMBIDs) == 0 {
		return counts, nil
	}
	var rows []models.CollectionRelease
	if err := db.Where("release_group_mb_id IN ?", releaseGroupMBIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
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

	// LibraryID is the library the want is for, resolved by WantTarget; uuid.Nil
	// is a want tied to no library (see models.CollectionDesire.LibraryID).
	LibraryID uuid.UUID

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

	// The wants this one is weighed against: the album's wants for the same library.
	// A want for the native library and Lidarr's selection for its own are two
	// answers, and neither may clear or update the other.
	var artist models.CollectionArtist
	if err := db.Where("mb_id = ?", in.ArtistMBID).Limit(1).Find(&artist).Error; err != nil {
		return desire, err
	}
	var rows []models.CollectionDesire
	if err := db.Where("release_group_mb_id = ?", in.ReleaseGroupMBID).Order("created_at").Find(&rows).Error; err != nil {
		return desire, err
	}
	same := make([]models.CollectionDesire, 0, len(rows))
	for _, d := range rows {
		if sameWantScope(d, artist, in.LibraryID) {
			same = append(same, d)
		}
	}

	// Within one release-group a desire is *either* "any release will do" *or* a set
	// of specific editions — never both, because holding both is a contradiction the
	// UI cannot render and the user cannot have meant. Setting one clears the other.
	kept := same[:0]
	for _, d := range same {
		if (d.ReleaseMBID == "") != (in.ReleaseMBID == "") {
			if err := db.Delete(&models.CollectionDesire{}, "id = ?", d.ID).Error; err != nil {
				return desire, err
			}
			continue
		}
		kept = append(kept, d)
	}

	// (release-group, release, library) identifies a desire: asking twice for the
	// same thing is the same want, not two. Re-asking updates the track selection.
	for _, d := range kept {
		if d.ReleaseMBID != in.ReleaseMBID {
			continue
		}
		desire = d
		// Save, not Update: the recordings column goes through GORM's json
		// serializer, and a column-level Update writes the raw Go slice instead.
		desire.RecordingMBIDs = in.RecordingMBIDs
		// A hand re-assert makes the want manual: the reconciliation passes must stop
		// managing it as a derived row (they would otherwise re-point or prune the
		// edition the user pinned).
		desire.Source = models.DesireSourceManual
		// Edited in a library, it is that library's from now on: a want tied to no
		// library that someone narrows while looking at one library meant that one.
		desire.LibraryID = in.LibraryID
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
		LibraryID:        in.LibraryID,
		Source:           models.DesireSourceManual,
	}
	if err := db.Create(&desire).Error; err != nil {
		return desire, err
	}
	return desire, nil
}

// sameWantScope reports whether an existing want answers the same question as a new
// one for the given library. For no library, that is the wants tied to no library.
// For a native library it is the wants shown there: that library's own, and — on an
// artist no manager governs — the ones tied to none, which every native library shows.
func sameWantScope(d models.CollectionDesire, artist models.CollectionArtist, library uuid.UUID) bool {
	if library == uuid.Nil {
		return d.LibraryID == uuid.Nil
	}
	return DesireInScope(d, artist, library, AuthorityNative)
}

// BackfillDesireSources gives provenance to rows written before CollectionDesire had
// any. It runs at startup and is a no-op once every row carries a source.
//
// The old schema said only "auto or not", in a boolean column AutoMigrate leaves in
// place rather than dropping. Where that column still exists its true rows become
// auto; everything else becomes manual, which is both the old meaning of auto=false
// and the safe reading of an unknown row — a reconciliation pass may rewrite its own
// rows, so mislabelling a hand-pinned want as derived would let it be re-pointed or
// pruned, while mislabelling a derived one as manual merely leaves it in place.
func BackfillDesireSources(db *gorm.DB) error {
	if db == nil {
		return nil
	}

	if db.Migrator().HasColumn(&models.CollectionDesire{}, "auto") {
		// Raw column reference: the field is gone from the model, but the data is not.
		if err := db.Model(&models.CollectionDesire{}).
			Where("(source IS NULL OR source = '') AND auto = ?", true).
			Update("source", models.DesireSourceAuto).Error; err != nil {
			return err
		}
	}

	res := db.Model(&models.CollectionDesire{}).
		Where("source IS NULL OR source = ''").
		Update("source", models.DesireSourceManual)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		logger.Log.Infof("gave %d existing want(s) a provenance", res.RowsAffected)
	}
	return nil
}

// ClearDesire removes a want, in every library. Nothing owned is touched: dropping a
// desire says "I no longer want the parts I do not have", never "delete what I have".
func ClearDesire(db *gorm.DB, releaseGroupMBID, releaseMBID string) error {
	return db.Where("release_group_mb_id = ? AND release_mb_id = ?",
		strings.TrimSpace(releaseGroupMBID), strings.TrimSpace(releaseMBID)).
		Delete(&models.CollectionDesire{}).Error
}

// ClearDesireIn removes the wants one native library shows for an album edition —
// that library's own, and the ones tied to no library when it shows those (see
// sameWantScope). Another library's want for the same edition is left alone.
func ClearDesireIn(db *gorm.DB, artistMBID, releaseGroupMBID, releaseMBID string, library uuid.UUID) error {
	if library == uuid.Nil {
		return ClearDesire(db, releaseGroupMBID, releaseMBID)
	}
	var artist models.CollectionArtist
	if err := db.Where("mb_id = ?", strings.TrimSpace(artistMBID)).Limit(1).Find(&artist).Error; err != nil {
		return err
	}
	var rows []models.CollectionDesire
	if err := db.Where("release_group_mb_id = ? AND release_mb_id = ?",
		strings.TrimSpace(releaseGroupMBID), strings.TrimSpace(releaseMBID)).Find(&rows).Error; err != nil {
		return err
	}
	var ids []uuid.UUID
	for _, d := range rows {
		if sameWantScope(d, artist, library) {
			ids = append(ids, d.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return db.Delete(&models.CollectionDesire{}, "id IN ?", ids).Error
}

// DesiresForArtist lists the explicit wants shown on an artist's page: those recorded
// against the artist, plus any recorded against a release-group they are credited on.
//
// The second half matters for collaborations. A desire stores the artist whose page it
// was created from, so wanting a collaboration from one artist's page would otherwise
// leave the other artist's page showing the same album as un-wanted — and offering to
// want it again.
func DesiresForArtist(db *gorm.DB, artistMBID string, releaseGroupMBIDs []string) ([]models.CollectionDesire, error) {
	var out []models.CollectionDesire
	q := db.Where("artist_mb_id = ?", strings.TrimSpace(artistMBID))
	if len(releaseGroupMBIDs) > 0 {
		q = q.Or("release_group_mb_id IN ?", releaseGroupMBIDs)
	}
	err := q.Order("release_group_mb_id").Find(&out).Error
	return out, err
}

// ReleaseGroupEditions lists every release of a release-group from MusicBrainz, for
// choosing a specific edition to desire. This is catalog data, not ownership — the
// owned-editions view arrives with M6 pass C.
func ReleaseGroupEditions(meta metadata.MetadataSource, releaseGroupMBID string) ([]models.MusicBrainzReleaseSearchResult, error) {
	return meta.GetReleaseGroupReleases(releaseGroupMBID)
}

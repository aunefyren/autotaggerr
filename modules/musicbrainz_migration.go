package modules

import (
	"errors"
	"fmt"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
)

// Upstream identity changes, as MusicBrainz reports them.
//
// The service does not tell a client "this ID moved" in any explicit way. It does
// two things, and each needs recognising for what it is:
//
//   - A merged MBID still resolves. MusicBrainz follows its own redirect internally
//     and answers 200 with the *surviving* entity, whose payload `id` is the new
//     MBID. Nothing about the HTTP exchange says a merge happened — the only signal
//     is that the id we asked for is not the id we got back. Miss it, and the app
//     keeps storing an ID the service no longer serves while holding data that is
//     perfectly correct, which is why this failure is silent and long-lived.
//   - A deleted MBID answers 404 (or 410). That is indistinguishable from a typo or
//     a bad correlation at the HTTP layer, but distinguishable from the failures
//     that matter operationally — a timeout, a 503 — and those must not be treated
//     as "this release is gone".

// ErrEntityGone reports that MusicBrainz no longer has an entity at all. It is a
// sentinel so callers can separate "this ID is dead" from "the lookup failed",
// which previously both arrived as an opaque error string: a scan marked the file
// as errored either way, and a drift sync retried the dead ID on every run forever.
var ErrEntityGone = errors.New("entity no longer exists on MusicBrainz")

// GoneError wraps ErrEntityGone with the entity it refers to, so a handler can act
// on the specific ID without parsing a message.
type GoneError struct {
	EntityType string
	MBID       string
	Status     int
	Detail     string
}

func (e *GoneError) Error() string {
	return fmt.Sprintf("%s %q no longer exists on MusicBrainz (HTTP %d): %s",
		e.EntityType, e.MBID, e.Status, e.Detail)
}

func (e *GoneError) Unwrap() error { return ErrEntityGone }

// newGoneError builds the sentinel-wrapping error for a 404/410 response.
func newGoneError(entityType, mbID string, status int, detail string) error {
	return &GoneError{EntityType: entityType, MBID: mbID, Status: status, Detail: detail}
}

// GoneEntity returns the entity type and MBID of a gone error, or false for any
// other error. Callers use it instead of type-asserting at every call site.
func GoneEntity(err error) (string, string, bool) {
	var gone *GoneError
	if errors.As(err, &gone) {
		return gone.EntityType, gone.MBID, true
	}
	return "", "", false
}

// RecordRedirect persists a detected merge as a pending migration, unless the same
// move is already on record.
//
// Detection is deliberately separate from application. This runs on the hot fetch
// path — inside a scan, under the rate limiter, possibly on a worker goroutine — so
// all it does is write one row. Deciding what a merge means for the collection, and
// rewriting rows accordingly, is the migration package's job and happens at a run
// boundary where a transaction and a Rebuild are affordable.
//
// Re-detection is the normal case, not an edge case: every subsequent fetch of the
// old ID sees the same redirect until the migration is applied. The unique index on
// (entity_type, old_mb_id) makes the repeat a no-op.
func RecordRedirect(entityType, oldMBID, newMBID, name string) {
	if cacheDB == nil || oldMBID == "" || newMBID == "" || oldMBID == newMBID {
		return
	}

	var existing models.MusicbrainzMigration
	err := cacheDB.Where("entity_type = ? AND old_mb_id = ?", entityType, oldMBID).First(&existing).Error
	if err == nil {
		return
	}

	row := models.MusicbrainzMigration{
		EntityType: entityType,
		OldMBID:    oldMBID,
		NewMBID:    newMBID,
		Kind:       models.MigrationKindRedirect,
		Status:     models.MigrationStatusPending,
		Name:       name,
		DetectedAt: time.Now(),
	}
	if err := cacheDB.Create(&row).Error; err != nil {
		// A concurrent worker inserting the same redirect loses the unique-index
		// race; that is the row already existing, which is the desired state.
		logger.Log.Debugf("could not record %s redirect %s -> %s: %s", entityType, oldMBID, newMBID, err.Error())
		return
	}
	logger.Log.Infof("MusicBrainz %s %s was merged into %s", entityType, oldMBID, newMBID)
}

// RecordDeletion persists a 404/410 as a pending migration. Same split as
// RecordRedirect: notice it here, act on it later.
func RecordDeletion(entityType, mbID string) {
	if cacheDB == nil || mbID == "" {
		return
	}

	var existing models.MusicbrainzMigration
	if err := cacheDB.Where("entity_type = ? AND old_mb_id = ?", entityType, mbID).First(&existing).Error; err == nil {
		return
	}

	row := models.MusicbrainzMigration{
		EntityType: entityType,
		OldMBID:    mbID,
		Kind:       models.MigrationKindDeleted,
		Status:     models.MigrationStatusPending,
		DetectedAt: time.Now(),
	}
	if err := cacheDB.Create(&row).Error; err != nil {
		logger.Log.Debugf("could not record %s deletion %s: %s", entityType, mbID, err.Error())
		return
	}
	logger.Log.Infof("MusicBrainz %s %s no longer exists upstream", entityType, mbID)
}

// DropCachedRelease removes a release from both the in-memory and persistent cache.
// Used when an ID turns out to be dead or superseded: leaving it cached means the
// entry expires and is re-fetched on every drift sync from then on, spending rate
// limit to re-learn the same 404.
func DropCachedRelease(mbID string) {
	musicbrainzReleaseCacheMu.Lock()
	delete(musicbrainzReleaseCache, mbID)
	musicbrainzReleaseCacheMu.Unlock()

	if cacheDB != nil {
		if err := cacheDB.Delete(&models.MusicbrainzReleaseCache{}, "mb_id = ?", mbID).Error; err != nil {
			logger.Log.Warnf("failed to drop MusicBrainz cache row %s: %s", mbID, err.Error())
		}
		return
	}
	markCacheDirty(cacheNameMusicbrainz)
}

// VerifyArtistIdentity re-reads an artist from MusicBrainz with the lookup cache
// bypassed, purely to learn whether the ID still resolves to the same entity.
//
// Artist redirects have no other systematic way of being found. Releases are re-read
// constantly — the drift sync walks every cached release on its TTL — but nothing
// re-reads an artist unless someone asks, so an artist merged upstream can sit
// undetected for as long as nobody opens their page. This is what the discography
// sync and the manual sweep call to close that gap.
//
// Errors are returned rather than logged so a sweep can distinguish "gone" (which is
// a result) from a transport failure (which is not).
func VerifyArtistIdentity(artistMBID string) error {
	if artistMBID == "" {
		return nil
	}

	// The cache is the thing being bypassed: a cached hit would answer the request
	// without ever asking MusicBrainz, which is exactly the question here. Dropping
	// the entry outright (rather than ignoring it) also removes the stale fallback,
	// so a transport failure surfaces as an error instead of being papered over with
	// the very copy this call exists to re-verify.
	MusicbrainzForgetEntity(models.MBEntityArtist, artistMBID)

	_, err := GetMusicBrainzArtist(artistMBID)
	return err
}

// CachedReleaseGroupID returns the release-group MBID a cached release belongs to,
// which is how a release moving between groups is noticed without an extra request.
func CachedReleaseGroupID(releaseMBID string) (string, bool) {
	release, ok := CachedRelease(releaseMBID)
	if !ok {
		return "", false
	}
	return release.ReleaseGroup.ID, release.ReleaseGroup.ID != ""
}

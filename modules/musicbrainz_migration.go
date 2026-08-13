package modules

import (
	"errors"
	"fmt"
	"net/http"
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

// ErrTransient reports the other half of that distinction: the lookup failed for a
// reason that says nothing about the entity. The service was unreachable, throttled,
// or answered with a server error, and the same request a minute later may well
// succeed.
//
// It exists because "failed" and "does not exist" were being recorded identically.
// A file whose release could not be fetched had its correlation discarded and left
// the disk view, so an outage mid-scan made whole albums read as mismatched against
// the manager — the deletion path at least records a migration, while an outage left
// nothing behind but an error string. Callers that decide what to *persist* about a
// file need to tell the two apart; callers that just want data can keep treating any
// error as "no data".
var ErrTransient = errors.New("MusicBrainz lookup failed transiently")

// TransientError carries the message its call site already wrote, wrapping both
// ErrTransient and the underlying cause so errors.Is finds either. A struct rather
// than the entity-bearing shape of GoneError because nothing acts on *which* entity
// failed transiently — a gone ID has a migration to record against it, an outage has
// only a retry — and because the generic fetch helpers do not always have an MBID to
// name.
type TransientError struct {
	Msg   string
	Cause error
}

func (e *TransientError) Error() string { return e.Msg }

// Unwrap returns both edges: the sentinel callers branch on, and the cause, so a
// handler can still ask whether it was a timeout underneath.
func (e *TransientError) Unwrap() []error {
	if e.Cause == nil {
		return []error{ErrTransient}
	}
	return []error{ErrTransient, e.Cause}
}

// newTransientError builds the sentinel-wrapping error. A nil cause is fine — an
// HTTP status is a complete explanation on its own.
func newTransientError(cause error, format string, args ...any) error {
	return &TransientError{Msg: fmt.Sprintf(format, args...), Cause: cause}
}

// transientStatus reports whether an HTTP status means "ask again later" rather than
// anything about the entity. Any 5xx is the server's own problem by definition, and
// 429 is the rate limiter asking for exactly that. A 4xx other than 429 is not here
// on purpose: a 400 or a 401 is a request this client will keep getting wrong, and
// quietly retrying it forever would hide a misconfiguration.
func transientStatus(status int) bool {
	return status >= 500 || status == http.StatusTooManyRequests
}

// StatusError is a non-transient HTTP status from MusicBrainz that the call site was
// not able to classify on its own.
//
// The generic fetch helper cannot decide what a 404 means, because that depends on the
// endpoint: on a lookup it is the entity being gone, but on a *browse* it can equally
// be the filter entity being unknown, so the two need different handling and only the
// caller knows which it made. Carrying the status lets a caller branch and upgrade the
// error to a GoneError once it has the evidence, instead of parsing the message.
type StatusError struct {
	Status int
	Detail string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("MusicBrainz returned HTTP %d: %s", e.Status, e.Detail)
}

// HTTPStatus returns the status a StatusError carries, or 0 for any other error.
func HTTPStatus(err error) int {
	var status *StatusError
	if errors.As(err, &status) {
		return status.Status
	}
	return 0
}

// notFoundStatus reports whether a status means the thing asked for is not there, as
// opposed to the request being wrong or the service being unwell. Kept beside
// transientStatus because the two together are the whole classification.
func notFoundStatus(status int) bool {
	return status == http.StatusNotFound || status == http.StatusGone
}

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
		reopenIfClosedExternally(existing)
		return
	}

	row := models.MusicbrainzMigration{
		EntityType: entityType,
		OldMBID:    oldMBID,
		NewMBID:    newMBID,
		Kind:       models.MigrationKindRedirect,
		Status:     models.MigrationStatusPending,
		Source:     models.DataSourceTypeMusicBrainz,
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
		reopenIfClosedExternally(existing)
		return
	}

	row := models.MusicbrainzMigration{
		EntityType: entityType,
		OldMBID:    mbID,
		Kind:       models.MigrationKindDeleted,
		Status:     models.MigrationStatusPending,
		Source:     models.DataSourceTypeMusicBrainz,
		DetectedAt: time.Now(),
	}
	if err := cacheDB.Create(&row).Error; err != nil {
		logger.Log.Debugf("could not record %s deletion %s: %s", entityType, mbID, err.Error())
		return
	}
	logger.Log.Infof("MusicBrainz %s %s no longer exists upstream", entityType, mbID)
}

// reopenIfClosedExternally puts a row back in the queue when the same change is seen
// again after being closed as resolved-by-something-else.
//
// A row is closed externally when nothing in the collection was keyed on the old ID
// any more, and closing it drops the cached entity — so nothing should ever ask for
// that ID again. Seeing the redirect a second time means something does point at it
// after all (a file re-indexed, an album re-mirrored under the old key), and the
// judgement that there was nothing to migrate no longer holds.
//
// Deliberately narrow. An applied row must not re-open — the remap happened, and the
// old ID resolving is exactly what a merge means — and a dismissed one must not, since
// re-raising a change the user declined is the nagging they declined.
func reopenIfClosedExternally(existing models.MusicbrainzMigration) {
	if existing.Resolution != models.MigrationResolutionExternal {
		return
	}
	err := cacheDB.Model(&models.MusicbrainzMigration{}).
		Where("id = ?", existing.ID).
		Updates(map[string]any{
			"status":            models.MigrationStatusPending,
			"resolution":        "",
			"resolution_detail": "",
			"resolved_at":       nil,
		}).Error
	if err != nil {
		logger.Log.Warnf("failed to re-open migration %s: %s", existing.OldMBID, err.Error())
		return
	}
	logger.Log.Infof("%s %s is referenced again; its identity change is back in the review queue",
		existing.EntityType, existing.OldMBID)
}

// DropCachedRelease removes a release from both the in-memory and persistent cache.
// Used when an ID turns out to be dead or superseded: leaving it cached means the
// entry expires and is re-fetched on every drift sync from then on, spending rate
// limit to re-learn the same 404.
func DropCachedRelease(mbID string) {
	musicbrainzReleaseCacheMu.Lock()
	delete(musicbrainzReleaseCache, mbID)
	musicbrainzReleaseCacheMu.Unlock()

	if cacheDB == nil {
		return
	}
	if err := cacheDB.Delete(&models.MusicbrainzReleaseCache{}, "mb_id = ?", mbID).Error; err != nil {
		logger.Log.Warnf("failed to drop MusicBrainz cache row %s: %s", mbID, err.Error())
	}
}

// There was a VerifyArtistIdentity here: it forgot an artist's cached lookup and
// re-read it over the network, purely to learn whether the ID still resolved to the
// same entity. It existed because nothing re-read an artist on a schedule, so a merge
// could sit undetected until somebody opened the page.
//
// That premise is gone. CollectionScope puts every artist in the refresh pass, so
// each is re-read on its TTL, and redirects are recorded on the HTTP path by whatever
// fetch happens to see one — no dedicated re-read required. Its only caller was the
// discography sync, where it turned a follow toggle into a rate-limited request and a
// discarded stale fallback. A deliberate re-read is still available: it is the forced
// pass, which expires rather than forgets (see mirror.refreshOne).

// CachedReleaseGroupID returns the release-group MBID a cached release belongs to,
// which is how a release moving between groups is noticed without an extra request.
func CachedReleaseGroupID(releaseMBID string) (string, bool) {
	release, ok := CachedRelease(releaseMBID)
	if !ok {
		return "", false
	}
	return release.ReleaseGroup.ID, release.ReleaseGroup.ID != ""
}

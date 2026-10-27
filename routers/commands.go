package routers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/components"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/migration"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/aunefyren/autotaggerr/process"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// The write half of the query layer: every action the MCP server offers, as a method
// both the REST handler and the tool call, so the guards a handler applies cannot be
// skipped by asking through the other door (see docs/mcp.md).

// apiError is a command's failure together with the HTTP status that describes it.
// Its message is written for whoever asked, REST client or assistant alike.
type apiError struct {
	status  int
	message string
}

func (e *apiError) Error() string { return e.message }

func failWith(status int, message string) error { return &apiError{status: status, message: message} }

// respondErr writes a command's error: its own status, or 500 for an error that
// carries none.
func respondErr(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	var ae *apiError
	if errors.As(err, &ae) {
		status = ae.status
	}
	c.JSON(status, gin.H{"error": err.Error()})
}

var errProcessorUnavailable = failWith(http.StatusServiceUnavailable, "processor unavailable")

// verbTarget is the scope a verb runs at: one library, one artist, or — neither set —
// everything.
type verbTarget struct {
	LibraryID  *uuid.UUID
	ArtistMBID string
	// In narrows an artist target to one library: the artist page read in a library
	// acts on that library's files only (docs/collection.md). Nil is every library.
	In *uuid.UUID
}

// artistScopeErr turns a failure to resolve an artist scope into the API's answer.
func artistScopeErr(err error, what string) error {
	if errors.Is(err, process.ErrNothingToProcess) || errors.Is(err, process.ErrNothingInLibrary) {
		return failWith(http.StatusConflict, err.Error())
	}
	logger.Log.Errorf("failed to resolve artist %s scope: %s", what, err.Error())
	return failWith(http.StatusInternalServerError, "failed to resolve what to "+what)
}

// verbResult is what queueing a verb reports. A queued verb has no event yet — the
// event is opened when the worker reaches the job — so it is followed through the
// job queue and the Activity feed, not by an ID returned here.
type verbResult struct {
	Status  string
	Library string
	Artist  string
	// Files is how many indexed files Tag files will rewrite.
	Files int
	// Folders is how many artist folders Process will walk, at artist scope.
	Folders any
}

func (a *API) targetLibrary(id uuid.UUID) (models.Library, error) {
	var lib models.Library
	if err := a.DB.First(&lib, "id = ?", id).Error; err != nil {
		return lib, failWith(http.StatusNotFound, "library not found")
	}
	return lib, nil
}

func (a *API) targetArtist(mbid string) (models.CollectionArtist, error) {
	var artist models.CollectionArtist
	if err := a.DB.Where("mb_id = ?", strings.TrimSpace(mbid)).First(&artist).Error; err != nil {
		return artist, failWith(http.StatusNotFound, "artist not found")
	}
	return artist, nil
}

// queueProcess queues the full pipeline. At artist scope the folders are resolved
// first, so "this artist has no files yet" is an answer rather than a run that
// reports zero files a minute later.
func (a *API) queueProcess(t verbTarget) (verbResult, error) {
	if a.Scan == nil {
		return verbResult{}, errProcessorUnavailable
	}
	switch {
	case t.LibraryID != nil:
		lib, err := a.targetLibrary(*t.LibraryID)
		if err != nil {
			return verbResult{}, err
		}
		if err := a.Scan.RunLibrary(lib.ID); err != nil {
			logger.Log.Error("failed to queue library processing. error: " + err.Error())
			return verbResult{}, failWith(http.StatusInternalServerError, "failed to queue the run")
		}
		return verbResult{Status: "processing queued", Library: lib.Name}, nil
	case t.ArtistMBID != "":
		artist, err := a.targetArtist(t.ArtistMBID)
		if err != nil {
			return verbResult{}, err
		}
		scope, err := a.Scan.ArtistScopeIn(artist.MBID, t.In)
		if err != nil {
			return verbResult{}, artistScopeErr(err, "process")
		}
		a.Scan.Run(scope)
		return verbResult{Status: "processing queued", Artist: artist.Name, Folders: scope.Detail["folders"]}, nil
	}
	// No busy check: overlapping requests queue rather than 409, and an identical run
	// already queued or running is collapsed by the runner's dedup.
	a.Scan.RunAll()
	return verbResult{Status: "processing queued"}, nil
}

// queueRefresh queues a metadata refresh. Writes no files. force ignores the cache,
// and is offered at artist scope only (see refreshArtist).
func (a *API) queueRefresh(t verbTarget, force bool) (verbResult, error) {
	if a.Scan == nil {
		return verbResult{}, errProcessorUnavailable
	}
	switch {
	case t.LibraryID != nil:
		lib, err := a.targetLibrary(*t.LibraryID)
		if err != nil {
			return verbResult{}, err
		}
		a.Scan.RefreshLibrary(lib.ID)
		return verbResult{Status: "refresh queued", Library: lib.Name}, nil
	case t.ArtistMBID != "":
		artist, err := a.targetArtist(t.ArtistMBID)
		if err != nil {
			return verbResult{}, err
		}
		a.Scan.RefreshArtist(artist.MBID, force)
		return verbResult{Status: "refresh queued", Artist: artist.Name}, nil
	}
	a.Scan.SyncDrift()
	return verbResult{Status: "refresh queued"}, nil
}

// queueTag queues Tag files. Refused when there is nothing to tag — a "0 files
// tagged" event would look like the action silently failed — but never for a running
// job: it queues behind whatever is ahead of it. Counted with the scope the runner
// selects with (models.TaggableItems), so the refusal cannot disagree with the work.
func (a *API) queueTag(t verbTarget) (verbResult, error) {
	if a.Scan == nil {
		return verbResult{}, errProcessorUnavailable
	}
	switch {
	case t.LibraryID != nil:
		lib, err := a.targetLibrary(*t.LibraryID)
		if err != nil {
			return verbResult{}, err
		}
		var count int64
		a.DB.Model(&models.LibraryItem{}).
			Where("library_id = ?", lib.ID).Scopes(models.TaggableItems).Count(&count)
		if count == 0 {
			return verbResult{}, failWith(http.StatusConflict, "no indexed files in this library — process it first")
		}
		a.Scan.RetagLibrary(lib.ID)
		return verbResult{Status: "tagging queued", Library: lib.Name, Files: int(count)}, nil
	case t.ArtistMBID != "":
		artist, err := a.targetArtist(t.ArtistMBID)
		if err != nil {
			return verbResult{}, err
		}
		items, err := collection.ArtistItemIDsIn(a.DB, artist.MBID, t.In)
		if err != nil {
			logger.Log.Error("failed to load artist items. error: " + err.Error())
			return verbResult{}, failWith(http.StatusInternalServerError, "failed to resolve what to tag")
		}
		if len(items) == 0 {
			if t.In != nil {
				return verbResult{}, failWith(http.StatusConflict, "no indexed files for this artist in that library — process them first")
			}
			return verbResult{}, failWith(http.StatusConflict, "no indexed files for this artist — process them first")
		}
		a.Scan.RetagArtistIn(artist.MBID, t.In)
		return verbResult{Status: "tagging queued", Artist: artist.Name, Files: len(items)}, nil
	}
	var count int64
	a.DB.Model(&models.LibraryItem{}).
		Joins("JOIN libraries ON libraries.id = library_items.library_id").
		Where("libraries.enabled = ?", true).
		Scopes(models.TaggableItems).
		Count(&count)
	if count == 0 {
		return verbResult{}, failWith(http.StatusConflict, "no indexed files — process a library first")
	}
	a.Scan.RetagAll()
	return verbResult{Status: "tagging queued", Files: int(count)}, nil
}

// scanResult is what Scan reports: either the stats of the inline pass, or — when the
// disk is walked first — that it was queued.
type scanResult struct {
	Queued bool
	Artist string
	Stats  collection.RebuildStats
}

// runScan is the Scan verb: re-derive the collection from the index, inline (a
// database pass), or — walkDisk — queued behind a walk of the folders, which finds
// files that moved or arrived. No audio file is written either way. There is no
// library scope: the collection is per artist, not per folder.
func (a *API) runScan(t verbTarget, walkDisk bool) (scanResult, error) {
	if t.LibraryID != nil {
		return scanResult{}, failWith(http.StatusBadRequest, "scan runs for the whole collection or one artist, not a library")
	}
	var artist models.CollectionArtist
	if t.ArtistMBID != "" {
		if a.Scan == nil {
			return scanResult{}, errProcessorUnavailable
		}
		var err error
		if artist, err = a.targetArtist(t.ArtistMBID); err != nil {
			return scanResult{}, err
		}
	}

	if walkDisk {
		if a.Scan == nil {
			return scanResult{}, errProcessorUnavailable
		}
		var err error
		if artist.MBID != "" {
			err = a.Scan.DiscoverArtistIn(artist.MBID, t.In)
		} else {
			err = a.Scan.DiscoverAll()
		}
		if err != nil {
			if errors.Is(err, process.ErrNothingToProcess) || errors.Is(err, process.ErrNothingInLibrary) {
				if artist.MBID == "" {
					return scanResult{}, failWith(http.StatusConflict, "no enabled libraries to walk")
				}
				return scanResult{}, failWith(http.StatusConflict, err.Error())
			}
			logger.Log.Error("failed to queue a disk-walking scan. error: " + err.Error())
			if artist.MBID != "" {
				return scanResult{}, failWith(http.StatusInternalServerError, "failed to resolve what to scan")
			}
			return scanResult{}, failWith(http.StatusInternalServerError, "failed to queue the scan")
		}
		return scanResult{Queued: true, Artist: artist.Name}, nil
	}

	if artist.MBID != "" {
		stats, err := collection.RecordScan(a.DB, "Collection scan for "+artist.Name,
			collection.RebuildScope{ArtistMBID: artist.MBID},
			map[string]any{"artist": artist.Name, "artist_mb_id": artist.MBID})
		if err != nil {
			logger.Log.Error("failed to scan artist. error: " + err.Error())
			return scanResult{}, failWith(http.StatusInternalServerError, "failed to scan this artist")
		}
		return scanResult{Artist: artist.Name, Stats: stats}, nil
	}
	stats, err := collection.RecordScan(a.DB, "Collection scan", collection.RebuildScope{}, nil)
	if err != nil {
		return scanResult{}, failWith(http.StatusInternalServerError, "failed to scan the library")
	}
	return scanResult{Stats: stats}, nil
}

// followUpdate is a change to how an artist is followed; nil leaves a field alone.
type followUpdate struct {
	Monitored       *bool
	FollowTypes     *string
	FollowSecondary *bool
	FollowFromYear  *int
	// LibraryID is where following wants into when it is switched on (see
	// collection.WantTarget); nil is wherever an edit from the overview goes.
	LibraryID *uuid.UUID
}

// updateFollowSettings saves follow settings and re-syncs, so the missing list matches
// them immediately. It returns the artist as saved and, when followed, how many albums
// the sync now wants. The sync reads MusicBrainz.
func (a *API) updateFollowSettings(mbid string, in followUpdate) (models.CollectionArtist, int, error) {
	// A cutoff outside the range MusicBrainz dates can occupy is a typo, and storing it
	// would quietly want nothing (or everything) forever. Zero stays valid: it is how
	// the cutoff is cleared.
	if in.FollowFromYear != nil {
		if year := *in.FollowFromYear; !validFollowYear(year) {
			return models.CollectionArtist{}, 0, failWith(http.StatusBadRequest,
				fmt.Sprintf("follow_from_year must be 0 (no cutoff) or between %d and %d", minFollowFromYear, maxFollowFromYear))
		}
	}

	artist, err := a.targetArtist(mbid)
	if err != nil {
		return artist, 0, err
	}

	updates := map[string]any{}
	if in.Monitored != nil {
		// Following says where it wants into, and may want into several native
		// libraries with the one set of settings. Switched on or off in a library, that
		// library joins or leaves the set — required when Lidarr governs the artist,
		// since following Lidarr's own library is not Autotaggerr's to do. Switched on
		// or off in the overview of an artist no manager governs, it is every native
		// library, as it always was.
		target, err := a.wantTarget(artist.MBID, in.LibraryID)
		if err != nil && (*in.Monitored || in.LibraryID != nil) {
			return artist, 0, err
		}
		idx, err := collection.LoadLibraryIndex(a.DB)
		if err != nil {
			return artist, 0, failWith(http.StatusInternalServerError, "failed to load the libraries")
		}
		on, libraries := idx.FollowLibrariesAfter(artist, target, *in.Monitored)
		updates["monitored"] = on
		updates["follow_library_ids"] = libraries
	}
	if in.FollowTypes != nil {
		updates["follow_types"] = strings.TrimSpace(*in.FollowTypes)
	}
	if in.FollowSecondary != nil {
		updates["follow_secondary"] = *in.FollowSecondary
	}
	if in.FollowFromYear != nil {
		updates["follow_from_year"] = *in.FollowFromYear
	}
	if len(updates) > 0 {
		if err := a.DB.Model(&artist).Updates(updates).Error; err != nil {
			return artist, 0, failWith(http.StatusInternalServerError, "failed to update the artist")
		}
	}

	// Re-read so the sync uses the settings just saved.
	if err := a.DB.Where("mb_id = ?", artist.MBID).First(&artist).Error; err != nil {
		return artist, 0, failWith(http.StatusInternalServerError, "failed to reload the artist")
	}

	wanted := 0
	if artist.Monitored {
		n, err := collection.SyncArtist(a.DB, a.meta(), artist.MBID)
		if err != nil {
			return artist, 0, failWith(http.StatusBadGateway, "failed to sync discography: "+err.Error())
		}
		wanted = n
	} else if groups, _, err := a.meta().GetArtistReleaseGroups(artist.MBID); err == nil {
		// Not following, but the shape still decides what "their albums" are, so the
		// catalogue count follows the settings. Best-effort: a failed read leaves the
		// old count until the next discography pass.
		if err := collection.RecordCatalogue(a.DB, artist, groups); err != nil {
			logger.Log.Warnf("failed to record the catalogue of %s: %s", artist.MBID, err.Error())
		}
	}
	a.DB.Where("mb_id = ?", artist.MBID).First(&artist)
	return artist, wanted, nil
}

// addDesire records an explicit want for a library, behind the manager gate: in
// Lidarr's library what is wanted and which edition is Lidarr's call, mirrored into
// the collection by the sync, so a want is refused there and written for a native
// library instead (see collection.WantTarget). It fails closed if the artist's
// manager cannot be resolved.
func (a *API) addDesire(library *uuid.UUID, in collection.DesireInput) (models.CollectionDesire, error) {
	target, err := a.wantTarget(in.ArtistMBID, library)
	if err != nil {
		return models.CollectionDesire{}, err
	}
	in.LibraryID = target
	desire, err := collection.SetDesire(a.DB, in)
	if err != nil {
		return desire, failWith(http.StatusBadRequest, err.Error())
	}
	return desire, nil
}

// filesIdentityEditable refuses a manual identity when any file in the set is
// Lidarr's to identify: it lives in a Lidarr-managed library and its artist has not
// been detached (itemsIdentityEditable). There the release and track are Lidarr's to
// decide, so the honest answer is to refuse and point at Lidarr. It fails closed if a
// manager cannot be resolved, and one Lidarr-governed file locks the whole action — a
// bulk attach is one album, and splitting it would be a worse surprise than rejecting
// it.
//
// target is the release the files are being attached to. A file with no identity yet
// has no edition to read its artist from, so for those files the artist is read from
// the target instead: attaching an unidentified file in a Lidarr library to a detached
// artist's release is allowed, since detaching took every one of that artist's
// libraries native. A file that already has an identity is judged by it — re-pointing
// a file Lidarr identified as someone else's is still Lidarr's call.
//
// The target is fetched only when an unidentified file needs it, so every other
// refusal stays as cheap as it was.
func (a *API) filesIdentityEditable(items []models.LibraryItem, target *releaseLoader) error {
	editable, err := a.itemsIdentityEditable(items)
	if err != nil {
		logger.Log.Warnf("attach gate: failed to resolve who identifies these files: %s", err.Error())
		return failWith(http.StatusInternalServerError, "failed to resolve the library manager")
	}
	targetDetached := false
	// With no artist detached no target can pass, so the refusal costs no fetch.
	anyDetached, err := collection.AnyArtistDetached(a.DB)
	if err != nil {
		return failWith(http.StatusInternalServerError, "failed to resolve the library manager")
	}
	for _, item := range items {
		if editable[item.ID] {
			continue
		}
		if item.MBReleaseID == "" && anyDetached {
			if !targetDetached {
				release, err := target.load()
				if err != nil {
					return err
				}
				if targetDetached, err = collection.ReleaseOfDetachedArtist(a.DB, release); err != nil {
					return failWith(http.StatusInternalServerError, "failed to resolve the release's artist")
				}
			}
			if targetDetached {
				continue
			}
			return failWith(http.StatusConflict, "these files are managed by Lidarr — set them in Lidarr, or attach them to a release of an artist detached from Lidarr")
		}
		return failWith(http.StatusConflict, "these files are managed by Lidarr — change the release in Lidarr, not here")
	}
	return nil
}

// releaseLoader fetches an attach target once, on first use — the gate may need it,
// and the attach always does after.
type releaseLoader struct {
	a       *API
	id      string
	release models.MusicBrainzReleaseResponse
	err     error
	done    bool
}

func (a *API) releaseLoader(id string) *releaseLoader { return &releaseLoader{a: a, id: id} }

func (l *releaseLoader) load() (models.MusicBrainzReleaseResponse, error) {
	if !l.done {
		l.done = true
		l.release, l.err = l.a.meta().GetRelease(l.id)
		if l.err != nil {
			logger.Log.Errorf("attach: failed to load release %s: %s", l.id, l.err.Error())
			l.err = failWith(http.StatusBadGateway, "could not load that release from MusicBrainz")
		}
	}
	return l.release, l.err
}

// tagOutcome is what an identity change that writes tags straight away reports. The
// identity is saved even when the write fails — it is a real decision worth keeping —
// and Warning says so.
type tagOutcome struct {
	TagsWritten int
	Warning     string
}

func (a *API) libraryItem(id uuid.UUID) (models.LibraryItem, error) {
	var item models.LibraryItem
	if err := a.DB.First(&item, "id = ?", id).Error; err != nil {
		return item, failWith(http.StatusNotFound, "library item not found")
	}
	return item, nil
}

// attachFile pins a file to a release and track, validated against the real release
// rather than trusted — a wrong ID would otherwise be written into the file's tags —
// then writes its tags, so the file stops being unmatched without waiting for a run.
func (a *API) attachFile(itemID uuid.UUID, releaseID, trackID string) (tagOutcome, error) {
	releaseID, trackID = strings.TrimSpace(releaseID), strings.TrimSpace(trackID)
	if releaseID == "" || trackID == "" {
		return tagOutcome{}, failWith(http.StatusBadRequest, "mb_release_id and mb_release_track_id are required")
	}
	item, err := a.libraryItem(itemID)
	if err != nil {
		return tagOutcome{}, err
	}
	target := a.releaseLoader(releaseID)
	if err := a.filesIdentityEditable([]models.LibraryItem{item}, target); err != nil {
		return tagOutcome{}, err
	}
	release, err := target.load()
	if err != nil {
		return tagOutcome{}, err
	}
	track, found := modules.FindReleaseTrack(release, trackID)
	if !found {
		return tagOutcome{}, failWith(http.StatusBadRequest, "that track is not part of that release")
	}
	if err := a.saveCorrelation(item.ID, release.ID, track); err != nil {
		return tagOutcome{}, failWith(http.StatusInternalServerError, "failed to save the correlation")
	}

	if a.Scan == nil {
		return tagOutcome{Warning: "Attached, but nothing is available to write tags; the next run will."}, nil
	}
	written, err := a.Scan.RetagItem(item.ID)
	if err != nil {
		logger.Log.Warnf("attach: correlation saved but tagging failed for %s: %s", item.Path, err.Error())
		return tagOutcome{Warning: "Attached, but writing tags failed: " + err.Error()}, nil
	}
	return tagOutcome{TagsWritten: written}, nil
}

// releasePin removes a manual pin, handing the file back to automatic resolution.
// The already-written tags are left alone — they are the correct tags for whatever it
// was attached to, and rewriting them is the next run's business. Releasing is also
// how a held pin is settled the other way: the hold and the hash go with the pin. The
// file keeps the status the hold recorded, so the next run resolves it.
func (a *API) releasePin(itemID uuid.UUID) error {
	item, err := a.libraryItem(itemID)
	if err != nil {
		return err
	}
	if err := a.DB.Model(&models.LibraryItem{}).Where("id = ?", item.ID).Updates(map[string]any{
		"pinned":            false,
		"pin_held":          false,
		"pinned_audio_hash": "",
	}).Error; err != nil {
		return failWith(http.StatusInternalServerError, "failed to remove the pin")
	}
	return nil
}

// confirmHeldPin settles a held pin by keeping it: the hand-chosen identity is
// confirmed for the audio the file holds now, and written straight away. Only a held
// pin can be confirmed; anything else has nothing to confirm.
func (a *API) confirmHeldPin(itemID uuid.UUID) (tagOutcome, error) {
	item, err := a.libraryItem(itemID)
	if err != nil {
		return tagOutcome{}, err
	}
	if !item.Pinned || !item.PinHeld {
		return tagOutcome{}, failWith(http.StatusConflict, "this file has no held pin to confirm")
	}
	audioHash, err := components.PinAudioHash(item.Path)
	if err != nil {
		return tagOutcome{}, failWith(http.StatusInternalServerError, "could not read the file's audio: "+err.Error())
	}
	if err := a.DB.Model(&models.LibraryItem{}).Where("id = ?", item.ID).Updates(map[string]any{
		"pinned_audio_hash":    audioHash,
		"pin_held":             false,
		"status":               models.LibraryItemStatusOK,
		"error":                "",
		"last_error_at":        nil,
		"last_error_transient": false,
	}).Error; err != nil {
		return tagOutcome{}, failWith(http.StatusInternalServerError, "failed to confirm the pin")
	}

	if a.Scan == nil {
		return tagOutcome{Warning: "Pin kept, but nothing is available to write tags; the next run will."}, nil
	}
	written, err := a.Scan.RetagItem(item.ID)
	if err != nil {
		logger.Log.Warnf("confirm pin: pin kept but tagging failed for %s: %s", item.Path, err.Error())
		return tagOutcome{Warning: "Pin kept, but writing tags failed: " + err.Error()}, nil
	}
	return tagOutcome{TagsWritten: written}, nil
}

// approveOutcome is an approval's result: either the migration as applied, or — for
// an album that only the manager can settle — the manager refresh that was queued.
type approveOutcome struct {
	Migration models.MusicbrainzMigration
	// Queued is set when approving asked the manager to re-read an artist instead of
	// applying anything; the fields below describe that.
	Queued     bool
	ArtistMBID string
	ArtistName string
	Marked     int64
	Message    string
}

// approveMigrationByID applies one held migration. Approving is itself the decision
// the review policy was waiting for, so the policy is not re-consulted.
//
// One case applies nothing: an album whose retirement is blocked only because the
// manager still lists it. There, approving asks the manager to re-read the artist and
// the migration settles itself afterwards (Runner.RepairArtistAlbums).
func (a *API) approveMigrationByID(id uuid.UUID) (approveOutcome, error) {
	if out, ok := a.approveViaManagerRefresh(id); ok {
		return out, nil
	}

	row, err := migration.ApplyByID(a.DB, id)
	if err != nil {
		logger.Log.Errorf("failed to apply migration %s: %s", id, err.Error())
		// A refusal to decide twice is not an event. The row is settled, nothing was
		// attempted, and recording it would put a feed entry in front of anyone who
		// double-clicks — reporting their second press as a failure of the first.
		if row.Status != models.MigrationStatusApplied && row.Status != models.MigrationStatusResolved {
			a.recordMigrationDecision(row, models.EventItemStatusError, err.Error())
		}
		return approveOutcome{}, failWith(http.StatusBadRequest, err.Error())
	}

	// Approving is a decision, and a decision that leaves no trace is indistinguishable
	// afterwards from the nightly pass having applied it — which is exactly the question
	// the feed exists to answer. Recorded after the fact so a failure is recorded too.
	if row.Status == models.MigrationStatusResolved {
		a.recordMigrationDecision(row, models.EventItemStatusResolved, row.ResolutionDetail)
	} else {
		a.recordMigrationDecision(row, models.EventItemStatusMigrated, "")
	}

	// Applying rewrites which releases the collection is keyed on, so the derived
	// ownership view is stale until it is rebuilt. Doing it here means a consistent
	// collection immediately after approving.
	a.rebuildAfterMigration()
	return approveOutcome{Migration: row}, nil
}

// approveViaManagerRefresh handles the one approval that is a question rather than an
// application, reporting whether it took it. The manager's refresh is waited on for
// minutes, so it is queued; the queue's dedup makes a second approval harmless.
func (a *API) approveViaManagerRefresh(id uuid.UUID) (approveOutcome, bool) {
	if a.Scan == nil {
		return approveOutcome{}, false
	}
	var row models.MusicbrainzMigration
	if err := a.DB.First(&row, "id = ?", id).Error; err != nil {
		return approveOutcome{}, false
	}
	review := migration.NewReview(a.DB, row)
	if !review.NeedsManagerRefresh || review.ArtistMBID == "" {
		return approveOutcome{}, false
	}

	// Marked before queueing, not after: the job may start on the worker goroutine
	// before this returns, and a mark written afterwards could land on a row the job
	// has already settled. Every open row of the artist's is marked, because the
	// refresh answers for all of them — see migration.MarkRepairQueued.
	marked, err := migration.MarkRepairQueued(a.DB, review.ArtistMBID)
	if err != nil {
		// The mark is how the table shows work in flight, not how the work happens.
		logger.Log.Warnf("failed to mark migrations as repairing for %s: %s", review.ArtistMBID, err.Error())
	}
	a.Scan.RepairArtistAlbums(review.ArtistMBID)

	who := review.ArtistName
	if who == "" {
		who = "the artist"
	}
	return approveOutcome{
		Migration: row, Queued: true, ArtistMBID: review.ArtistMBID, ArtistName: review.ArtistName, Marked: marked,
		Message: "Asking the manager to re-read " + who + ". If it corrects the album's " +
			"identifier this entry resolves itself; if it stops listing the album, the " +
			"album is removed from the collection. Follow it in Activity.",
	}, true
}

// dismissMigrationByID records that a detected change was deliberately not applied.
func (a *API) dismissMigrationByID(id uuid.UUID) (models.MusicbrainzMigration, error) {
	row, err := migration.Dismiss(a.DB, id)
	if err != nil {
		logger.Log.Errorf("failed to dismiss migration %s: %s", id, err.Error())
		return row, failWith(http.StatusBadRequest, err.Error())
	}
	a.recordMigrationDecision(row, models.EventItemStatusDismissed, "")
	return row, nil
}

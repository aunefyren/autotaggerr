package process

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/aunefyren/autotaggerr/utilities"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Following Lidarr.
//
// Lidarr changes files on its own schedule — an upgrade deletes an album's files and
// imports better ones under new trackfile IDs, often new paths — and nothing told
// Autotaggerr. Its cached Lidarr answers stayed an hour behind, the new files went
// `unmatched`, the album dropped out of the collection, and the repair was a manual
// scoped Lidarr sync and disk-walking Scan, followed by a Process to actually tag them.
//
// Lidarr already records every one of those changes in its history. This pass reads
// it on a short schedule, and for each artist it names drops their cached Lidarr
// answers and queues a Process over their folders — which is the repair above done by
// the verb that also tags. It is the cron job that queues the run, the same way the
// weekly schedule queues one; no verb triggers another.
//
// Polling rather than a webhook, because polling needs nothing configured on Lidarr's
// side and works when Lidarr cannot reach Autotaggerr. The history is the same either
// way; only who notices first differs.

// lidarrFollowSettle is how old a history record has to be before it is acted on. An
// upgrade is several records over seconds to minutes — grab, import per file, delete
// per replaced file, perhaps a rename — and processing the artist halfway through
// would see half an album. Records newer than this wait for the next pass, which is
// what makes the cursor the lower bound of the *next* window rather than "now". It also
// absorbs ordinary clock drift between the two hosts. A var so tests need not wait.
var lidarrFollowSettle = 2 * time.Minute

// Why an artist Lidarr named could not have a run queued. Their cached answers are
// dropped regardless, so the next run that reaches them asks Lidarr fresh.
const (
	followSkipNoArtist = "Lidarr's history did not name a MusicBrainz artist"
	followSkipNoFolder = "no folder of theirs was found in a library this manager governs — the next full processing run picks them up"
)

// followedArtist is one artist a history window named, with the albums it named for
// them — which is what InvalidateArtistCaches needs to drop the album-keyed
// caches as well as the artist-keyed one.
type followedArtist struct {
	lidarrID int64
	mbid     string
	name     string
	// folder is the artist folder's name as Lidarr stores it — the last segment of
	// its path, which is the only part comparable across Lidarr's root and ours.
	folder  string
	albums  map[int64]bool
	records int
}

// FollowLidarr reads every enabled Lidarr manager's history since it was last read
// and acts on the artists whose files changed. It runs on the cron goroutine rather
// than the job queue: it is a few requests and a few rows, and queued behind an
// hours-long run it would only learn of changes later. The work it starts goes on the
// queue like any other Process.
//
// An overlapping tick is skipped rather than waited for; the next one covers the same
// window, because the cursor only moves when a pass completes.
func (r *Runner) FollowLidarr() {
	if r.db == nil || !r.followMu.TryLock() {
		return
	}
	defer r.followMu.Unlock()

	var managers []models.Manager
	if err := r.db.Where("type = ? AND enabled = ?", models.ManagerTypeLidarr, true).Find(&managers).Error; err != nil {
		logger.Log.Warnf("failed to load managers to follow Lidarr: %s", err.Error())
		return
	}
	upTo := time.Now().Add(-lidarrFollowSettle)
	for _, m := range managers {
		if m.LidarrBaseURL == "" || m.LidarrAPIKey == "" {
			continue
		}
		r.followManager(m, upTo)
	}
}

// followManager follows one manager's history from its cursor up to upTo.
//
// A failed read records no event and leaves the cursor where it was: the health check
// already reports an unreachable Lidarr, a row every few minutes saying so again would
// bury everything else in the feed, and the window is simply read again next time.
func (r *Runner) followManager(m models.Manager, upTo time.Time) {
	if m.LidarrHistoryCursor == nil {
		// First sight of this manager. Start from now rather than replaying its whole
		// history: everything before this moment is what the scheduled runs have been
		// covering all along.
		logger.Log.Infof("following Lidarr manager %q from now on", m.Name)
		r.saveFollowCursor(m.ID, upTo)
		return
	}
	from := *m.LidarrHistoryCursor
	if !upTo.After(from) {
		return
	}

	cookie := m.LidarrHeaderCookie
	client := modules.NewLidarrClient(m.LidarrBaseURL, m.LidarrAPIKey, &cookie)
	records, err := client.GetHistorySince(from)
	if err != nil {
		logger.Log.Warnf("failed to read Lidarr history for manager %q: %s", m.Name, err.Error())
		return
	}

	changed := artistsWithMovedFiles(records, from, upTo)
	if len(changed) == 0 {
		r.saveFollowCursor(m.ID, upTo)
		return
	}

	ev := events.Begin(r.db, models.EventTypeLidarrFollow, "Follow Lidarr: "+m.Name)
	items := make([]models.EventItem, 0, len(changed))
	queued, skipped := 0, 0
	for _, a := range changed {
		albumIDs := make([]int64, 0, len(a.albums))
		for id := range a.albums {
			albumIDs = append(albumIDs, id)
		}
		client.InvalidateArtistCaches(a.lidarrID, albumIDs)

		item := models.EventItem{Path: a.mbid, Kind: models.EventItemKindEntity, Status: models.EventItemStatusQueued}
		if reason := r.queueFollowedArtist(m, a); reason != "" {
			item.Status = models.EventItemStatusSkipped
			item.Error = reason
			if a.mbid == "" {
				item.Path = a.name
			}
			skipped++
		} else {
			queued++
		}
		items = append(items, item)
	}
	r.saveFollowCursor(m.ID, upTo)

	summary := fmt.Sprintf("%s with file changes in Lidarr · %d queued for processing",
		plural(len(changed), "artist", "artists"), queued)
	if skipped > 0 {
		summary += fmt.Sprintf(" · %d skipped", skipped)
	}
	ev.Stats = []models.EventStat{
		{Label: "Artists changed", Value: len(changed), Kind: models.EventStatNotable},
		{Label: "Processing queued", Value: queued, Kind: models.EventStatNotable, Filter: models.EventItemStatusQueued},
		{Label: "Skipped", Value: skipped, Kind: models.EventStatMuted, Filter: models.EventItemStatusSkipped},
	}
	events.Finish(r.db, ev, models.EventStatusOK, summary, map[string]any{
		"manager": m.Name,
		"from":    from,
		"to":      upTo,
	})
	events.AddItems(r.db, ev, items)
}

// queueFollowedArtist queues a Process over the artist's folders and returns "" — or,
// when there is nothing to scope a run to, the reason why.
//
// The collection is asked first, because it knows every folder the artist's files sit
// in. An artist it cannot answer for — new to Lidarr, so not in the collection, or in
// it with nothing indexed yet — is looked for on disk instead, by the folder name
// Lidarr files them under, in each library this manager governs. That is the case the
// collection can never cover by itself: an artist's first import is precisely the
// one with nothing indexed. The Process that runs over the folder is what then brings
// them into the collection.
func (r *Runner) queueFollowedArtist(m models.Manager, a followedArtist) string {
	if a.mbid == "" {
		return followSkipNoArtist
	}
	scope, err := r.ArtistScope(a.mbid)
	switch {
	case err == nil:
		r.Run(scope)
		return ""
	case errors.Is(err, gorm.ErrRecordNotFound), errors.Is(err, ErrNothingToProcess):
	default:
		return err.Error()
	}

	found := r.lidarrArtistFolders(m, a.folder)
	if len(found) == 0 {
		return followSkipNoFolder
	}
	r.Run(buildScope("Processing "+a.name,
		map[string]any{"artist": a.name, "artist_mb_id": a.mbid}, found))
	return ""
}

// lidarrArtistFolders finds an artist folder by name under every enabled library
// assigned to this manager. Only the name is compared: Lidarr's stored path sits under
// its own root (often a container mount), so the leading part says nothing about
// where the folder is here — the same reason trackfile matching compares by position.
func (r *Runner) lidarrArtistFolders(m models.Manager, folder string) []collection.ArtistTarget {
	if folder == "" || folder == "." || folder == string(filepath.Separator) {
		return nil
	}
	var libraries []models.Library
	if err := r.db.Where("manager_id = ? AND enabled = ?", m.ID, true).Order("name").Find(&libraries).Error; err != nil {
		logger.Log.Warnf("failed to load libraries for manager %q: %s", m.Name, err.Error())
		return nil
	}
	var found []collection.ArtistTarget
	for _, library := range libraries {
		candidate := filepath.Join(library.Path, folder)
		if fi, err := os.Stat(candidate); err == nil && fi.IsDir() {
			found = append(found, collection.ArtistTarget{Library: library, Path: candidate})
		}
	}
	return found
}

// artistsWithMovedFiles groups the records in (from, upTo] that moved files by the
// artist they name, sorted by name so the event lists them in a stable order.
func artistsWithMovedFiles(records []models.LidarrHistoryRecord, from, upTo time.Time) []followedArtist {
	byID := map[int64]*followedArtist{}
	for _, rec := range records {
		if !rec.Date.After(from) || rec.Date.After(upTo) || !rec.EventType.MovesFiles() || rec.ArtistID == 0 {
			continue
		}
		a := byID[rec.ArtistID]
		if a == nil {
			a = &followedArtist{lidarrID: rec.ArtistID, albums: map[int64]bool{}}
			byID[rec.ArtistID] = a
		}
		if rec.Artist != nil {
			a.mbid = rec.Artist.ForeignArtistID
			a.name = rec.Artist.Name
			a.folder = utilities.BaseOfPathAny(rec.Artist.Path)
		}
		if rec.AlbumID != 0 {
			a.albums[rec.AlbumID] = true
		}
		a.records++
	}

	out := make([]followedArtist, 0, len(byID))
	for _, a := range byID {
		if a.name == "" {
			a.name = fmt.Sprintf("Lidarr artist %d", a.lidarrID)
		}
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// saveFollowCursor records how far a manager's history has been acted on. A column
// update rather than a Save, so it cannot write back a manager row someone edited in
// the meantime.
func (r *Runner) saveFollowCursor(managerID uuid.UUID, at time.Time) {
	if err := r.db.Model(&models.Manager{}).Where("id = ?", managerID).Update("lidarr_history_cursor", at).Error; err != nil {
		logger.Log.Warnf("failed to save the Lidarr history cursor: %s", err.Error())
	}
}

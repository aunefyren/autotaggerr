package process

import (
	"fmt"
	"time"

	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
)

// Discographies: every artist's catalogue, read so coverage is measured against what
// an artist released rather than what happens to be stored for them. See
// collection/catalogue.go and docs/collection.md#every-artist-has-a-catalogue.

// PhaseDiscographies is the discography job's stage.
const PhaseDiscographies = "discographies"

// catalogueMaxAge is how long a read discography is trusted before the scheduled pass
// reads it again. Long, because a discography grows by an album every year or two and
// the read spends the MusicBrainz budget; an artist anyone follows is re-read by the
// metadata refresh anyway.
var catalogueMaxAge = 30 * 24 * time.Hour

// ReadCatalogues queues a read of every artist whose discography has not been read, or
// was read longer ago than catalogueMaxAge. Called at startup, after the scheduled
// metadata refresh, and whenever an artist is new to the collection — which is the
// collection.CatalogueReader hook; it returns at once.
//
// One job, deduped on its key, so a scan that discovers forty artists queues one read
// rather than forty. An artist created while the job is already running is not in its
// list; the next trigger reaches them.
func (r *Runner) ReadCatalogues() {
	if r.db == nil {
		return
	}
	r.enqueue(job{jobDiscography, "discographies", "Read discographies", func() {
		var mbids []string
		cutoff := time.Now().Add(-catalogueMaxAge)
		if err := r.db.Model(&models.CollectionArtist{}).
			Where("catalogue_read_at IS NULL OR catalogue_read_at < ?", cutoff).
			Order("name").Pluck("mb_id", &mbids).Error; err != nil {
			logger.Log.Warnf("failed to list artists to read discographies for: %s", err.Error())
			return
		}
		r.readDiscographies("Read discographies", mbids)
	}})
}

// FollowArtists queues the discography sync of artists whose follow settings were
// just set, so what they want is listed. The settings are written by the caller; this
// is only the reading, which spends a MusicBrainz request (or a page of them) per artist
// and so cannot answer inline.
func (r *Runner) FollowArtists(mbids []string) {
	if len(mbids) == 0 {
		return
	}
	title := fmt.Sprintf("Follow %s", plural(len(mbids), "artist", "artists"))
	r.enqueue(job{jobDiscography, "follow:" + uuid.NewString(), title, func() { r.readDiscographies(title, mbids) }})
}

// readDiscographies reads each artist's discography. An artist Autotaggerr follows
// is synced — its wanted albums stored — which records the catalogue on the way; any
// other only has the catalogue recorded, because reading is not wanting. An artist a
// manager governs is never synced here: its catalogue block is the manager's.
//
// Nothing to read records nothing, like the Lidarr follower.
func (r *Runner) readDiscographies(title string, mbids []string) {
	if len(mbids) == 0 {
		return
	}
	event := events.Begin(r.db, models.EventTypeDiscographyRead, title)
	r.progTotal.Store(int64(len(mbids)))
	r.setPhase(PhaseDiscographies)
	stopProgress := events.StartProgress(r.db, event, r.progressSnapshot)
	ctx := r.jobContext()

	var items []models.EventItem
	read, synced, failed, stopped := 0, 0, 0, false
	for _, mbid := range mbids {
		if ctx.Err() != nil {
			stopped = true
			break
		}
		var artist models.CollectionArtist
		if err := r.db.Where("mb_id = ?", mbid).First(&artist).Error; err != nil {
			r.progDone.Add(1)
			continue // removed since the job was queued
		}
		r.setCurrent(artist.Name)
		item := models.EventItem{Path: mbid, Kind: models.EventItemKindEntity, Status: models.EventItemStatusRefreshed}

		var err error
		if artist.Monitored && collection.FollowGoverns(artist) {
			_, err = collection.SyncArtist(r.db, r.meta, mbid)
			synced++
		} else {
			var groups []models.MusicBrainzArtistReleaseGroup
			if groups, _, err = r.meta.GetArtistReleaseGroups(mbid); err == nil {
				err = collection.RecordCatalogue(r.db, artist, groups)
			}
		}
		if err != nil {
			item.Status, item.Error = models.EventItemStatusError, err.Error()
			failed++
			logger.Log.Warnf("could not read the discography of %s: %s", artist.Name, err.Error())
		} else {
			read++
		}
		items = append(items, item)
		r.progDone.Add(1)
	}
	stopProgress()

	summary := fmt.Sprintf("%s read", plural(read, "discography", "discographies"))
	if synced > 0 {
		summary += fmt.Sprintf(" · %d followed artists synced", synced)
	}
	if failed > 0 {
		summary += fmt.Sprintf(" · %d failed", failed)
	}
	if stopped {
		summary = "stopped early — " + summary
	}
	status := models.EventStatusOK
	if failed > 0 {
		status = models.EventStatusError
	}
	event.Stats = []models.EventStat{
		{Label: "Discographies read", Value: read, Kind: models.EventStatNotable, Filter: models.EventItemStatusRefreshed},
		{Label: "Failed", Value: failed, Kind: models.EventStatBad, Filter: models.EventItemStatusError},
	}
	if len(items) > r.detailRetention {
		items = items[:r.detailRetention]
	}
	events.Finish(r.db, event, status, summary, map[string]any{"artists": len(mbids), "read": read, "synced": synced, "errors": failed})
	events.AddItems(r.db, event, items)
	events.Prune(r.db, r.eventRetention)
}

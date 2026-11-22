package process

import (
	"errors"
	"fmt"
	"time"

	"github.com/aunefyren/autotaggerr/components"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrListeningBusy is a sync asked for while another is reading.
var ErrListeningBusy = errors.New("listening history is already being read")

// SyncListening reads every enabled listening account's new plays (docs/listening.md).
// Like FollowLidarr it runs on the cron goroutine rather than the job queue: it writes
// no file and costs a few requests per account, and queued behind an hours-long run
// the overlay would only learn of plays later. An overlapping tick is skipped; the
// next one reads the same window, because a cursor only moves when its account's pass
// completes.
func (r *Runner) SyncListening() {
	if err := r.SyncListeningFor(nil); err != nil && !errors.Is(err, ErrListeningBusy) {
		logger.Log.Warnf("failed to read listening history: %s", err.Error())
	}
}

// SyncListeningFor reads one user's accounts, or every user's with a nil user. It
// returns ErrListeningBusy when a pass is already running.
func (r *Runner) SyncListeningFor(user *uuid.UUID) error {
	if r.db == nil {
		return nil
	}
	if !r.listenMu.TryLock() {
		return ErrListeningBusy
	}
	defer r.listenMu.Unlock()

	q := r.db.Where("enabled = ?", true)
	if user != nil {
		q = q.Where("user_id = ?", *user)
	}
	var accounts []models.ListeningAccount
	if err := q.Order("created_at").Find(&accounts).Error; err != nil {
		return err
	}
	if len(accounts) == 0 {
		return nil
	}
	s := &listeningSync{db: r.db}
	for i := range accounts {
		s.account(&accounts[i])
	}
	return nil
}

// listeningSync is one pass. The path index is built once, on the first Plex
// account, and shared by the rest.
type listeningSync struct {
	db      *gorm.DB
	index   *modules.TrailingPathIndex
	servers map[string]*modules.PlexClient
}

// account reads one account from its cursor and records the outcome on the row. A
// failure leaves the cursor where it was and records no event: the plays stored
// before it are kept (storing is idempotent), and the next pass reads the window
// again.
func (s *listeningSync) account(a *models.ListeningAccount) {
	since := time.Time{}
	if a.Cursor != nil {
		since = *a.Cursor
	}
	var newest time.Time
	var err error
	switch a.Source {
	case models.ListeningSourceListenBrainz:
		newest, err = s.listenBrainz(a, since)
	case models.ListeningSourcePlex:
		newest, err = s.plex(a, since)
	default:
		err = fmt.Errorf("unknown listening source %q", a.Source)
	}
	now := time.Now()
	updates := map[string]any{"last_synced_at": now, "last_error": ""}
	if err != nil {
		updates["last_error"] = err.Error()
		logger.Log.Warnf("failed to read listening history for account %s (%s): %s", a.ID, a.Source, err.Error())
	} else if newest.After(since) {
		updates["cursor"] = newest
	}
	if err := s.db.Model(&models.ListeningAccount{}).Where("id = ?", a.ID).Updates(updates).Error; err != nil {
		logger.Log.Warnf("failed to record the listening sync of account %s: %s", a.ID, err.Error())
	}
}

// store inserts plays, skipping ones already stored for the account, and their
// artists.
func (s *listeningSync) store(listens []models.Listen, artists [][]modules.ListenBrainzArtist) error {
	if len(listens) == 0 {
		return nil
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		for i := range listens {
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&listens[i])
			if res.Error != nil {
				return res.Error
			}
			// A play already stored keeps the artists it was stored with.
			if res.RowsAffected == 0 || listens[i].ID == 0 || artists == nil {
				continue
			}
			seen := map[string]bool{}
			var rows []models.ListenArtist
			for _, a := range artists[i] {
				if a.MBID == "" || seen[a.MBID] {
					continue
				}
				seen[a.MBID] = true
				rows = append(rows, models.ListenArtist{ListenID: listens[i].ID, ArtistMBID: a.MBID, ArtistName: a.Name})
			}
			if len(rows) > 0 {
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (s *listeningSync) listenBrainz(a *models.ListeningAccount, since time.Time) (time.Time, error) {
	client := modules.NewListenBrainzClient(a.Username, a.Token)
	if client.Username == "" {
		return since, errors.New("no ListenBrainz user name")
	}
	return client.ListensSince(since, func(page []modules.ListenBrainzListen) error {
		listens := make([]models.Listen, len(page))
		artists := make([][]modules.ListenBrainzArtist, len(page))
		for i, l := range page {
			listens[i] = models.Listen{
				UserID: a.UserID, AccountID: a.ID, Source: models.ListeningSourceListenBrainz,
				ListenedAt: l.ListenedAt, ExternalKey: l.ExternalKey(),
				RecordingMBID: l.RecordingMBID, ReleaseMBID: l.ReleaseMBID,
				Title: l.Title, Artist: l.Artist, Album: l.Album,
			}
			artists[i] = l.Artists
		}
		return s.store(listens, artists)
	})
}

func (s *listeningSync) plex(a *models.ListeningAccount, since time.Time) (time.Time, error) {
	if a.MediaServerID == nil || a.PlexAccountID == "" {
		return since, errors.New("no Plex server or account chosen")
	}
	if s.servers == nil {
		s.servers = components.MediaServerClients(s.db)
	}
	client, ok := s.servers[a.MediaServerID.String()]
	if !ok {
		return since, errors.New("the Plex server was removed or disabled")
	}
	if s.index == nil {
		var items []models.LibraryItem
		if err := s.db.Model(&models.LibraryItem{}).Scopes(models.InDiskView).
			Select("id, path, mb_release_id, mb_recording_id").Find(&items).Error; err != nil {
			return since, err
		}
		s.index = modules.NewTrailingPathIndex(items)
	}
	return client.History(a.PlexAccountID, since, func(page []modules.PlexPlay) error {
		listens := make([]models.Listen, 0, len(page))
		for _, p := range page {
			l := models.Listen{
				UserID: a.UserID, AccountID: a.ID, Source: models.ListeningSourcePlex,
				ListenedAt: p.ViewedAt, ExternalKey: p.HistoryKey,
				Title: p.Title, Artist: p.Artist, Album: p.Album,
			}
			file, err := client.TrackFile(p.RatingKey)
			if err != nil {
				return err
			}
			if item, ok := s.index.Lookup(file); ok {
				l.Path, l.RecordingMBID, l.ReleaseMBID = item.Path, item.MBRecordingID, item.MBReleaseID
			}
			listens = append(listens, l)
		}
		return s.store(listens, nil)
	})
}

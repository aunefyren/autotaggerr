package main

import (
	"math/rand"
	"sort"
	"strconv"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Demo listening history: a year of plays for admin, so the graph's listening overlay
// (docs/listening.md) has something to show. Stored under a ListenBrainz account that
// is switched off, so the sync never asks the real ListenBrainz about a made-up user.
const (
	listenDays     = 400
	listenMaxPlays = 30000
)

// seedListens plays a few favourite albums often and the rest of the library rarely:
// on most days one to three sessions, each a whole album or a shuffle across the
// favourites, track after track. Deterministic for a given seed.
func seedListens(db *gorm.DB, specs []fileSpec, seed int64) (int, error) {
	var admin models.User
	if err := db.Where("username = ?", "admin").First(&admin).Error; err != nil {
		return 0, err
	}
	account := models.ListeningAccount{UserID: admin.ID, Source: models.ListeningSourceListenBrainz, Username: "demo"}
	if err := db.Create(&account).Error; err != nil {
		return 0, err
	}

	// Albums in a stable order, each its tracks in order.
	byRelease := map[string][]fileSpec{}
	for _, s := range specs {
		if s.release != nil {
			byRelease[s.release.ID] = append(byRelease[s.release.ID], s)
		}
	}
	if len(byRelease) == 0 {
		return 0, nil
	}
	ids := make([]string, 0, len(byRelease))
	for id := range byRelease {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	r := rand.New(rand.NewSource(seed + 7))
	r.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
	favourites := ids[:max(1, min(len(ids), 3+len(ids)/12))]

	var listens []models.Listen
	add := func(at time.Time, s fileSpec) {
		listens = append(listens, models.Listen{
			UserID: admin.ID, AccountID: account.ID, Source: models.ListeningSourceListenBrainz,
			ListenedAt: at, ExternalKey: strconv.FormatInt(at.Unix(), 10) + ":" + strconv.Itoa(len(listens)),
			RecordingMBID: s.track.Recording.ID, ReleaseMBID: s.release.ID,
			Title: s.tags.Title, Artist: s.tags.Artist, Album: s.tags.Album,
		})
	}
	trackLength := func(s fileSpec) time.Duration {
		if s.lengthMS > 0 {
			return time.Duration(s.lengthMS) * time.Millisecond
		}
		return 3 * time.Minute
	}

	start := time.Now().AddDate(0, 0, -listenDays).Truncate(24 * time.Hour)
	for day := 0; day < listenDays && len(listens) < listenMaxPlays; day++ {
		if r.Float64() > 0.65 {
			continue
		}
		for s := 0; s < 1+r.Intn(3); s++ {
			at := start.AddDate(0, 0, day).Add(time.Duration(8+r.Intn(14)) * time.Hour).Add(time.Duration(r.Intn(60)) * time.Minute)
			switch {
			case r.Float64() < 0.5: // a favourite album, start to finish
				for _, t := range byRelease[favourites[r.Intn(len(favourites))]] {
					add(at, t)
					at = at.Add(trackLength(t))
				}
			case r.Float64() < 0.7: // a shuffle across the favourites
				for n := 5 + r.Intn(20); n > 0; n-- {
					album := byRelease[favourites[r.Intn(len(favourites))]]
					t := album[r.Intn(len(album))]
					add(at, t)
					at = at.Add(trackLength(t))
				}
			default: // something from deeper in the library
				album := byRelease[ids[r.Intn(len(ids))]]
				for _, t := range album[:min(len(album), 1+r.Intn(4))] {
					add(at, t)
					at = at.Add(trackLength(t))
				}
			}
		}
		// Now and then a scrobble nobody could identify.
		if r.Float64() < 0.05 {
			listens = append(listens, models.Listen{UserID: admin.ID, AccountID: account.ID, Source: models.ListeningSourceListenBrainz,
				ListenedAt: start.AddDate(0, 0, day).Add(23 * time.Hour), ExternalKey: uuid.NewString(), Title: "Radio stream"})
		}
	}
	if err := db.CreateInBatches(listens, 500).Error; err != nil {
		return 0, err
	}
	return len(listens), nil
}

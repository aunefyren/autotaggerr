package insights

import (
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// creditsVersion is what SyncCredits derives per release. Bump it when that changes,
// and every release is read again on the next pass. 2 added the genre.
const creditsVersion = 2

// SyncResult is what one SyncCredits pass changed.
type SyncResult struct {
	// Read is how many releases had their credits read (new, or re-fetched upstream).
	Read int
	// Dropped is how many releases no file is on any more.
	Dropped int
	// Uncached is how many releases on disk are not in the release cache, so their
	// credits are unknown until a metadata refresh caches them.
	Uncached int
}

// SyncCredits brings insight_credits up to date with the files on disk and the
// release cache: credits are read for every release a file is on that has none yet,
// or whose cache entry was fetched since they were read, and dropped for releases no
// file is on. A pass where nothing changed reads two short lists and writes nothing,
// which is what lets the Scan verb run it after every rebuild.
//
// It reads the cache only (modules.CachedReleaseEntry), never the network. A release
// that has left the cache keeps the credits it had.
func SyncCredits(db *gorm.DB) (SyncResult, error) {
	var res SyncResult
	var onDisk []string
	if err := db.Model(&models.LibraryItem{}).Scopes(models.InDiskView).
		Distinct("library_items.mb_release_id").Pluck("library_items.mb_release_id", &onDisk).Error; err != nil {
		return res, err
	}
	var synced []models.InsightCreditRelease
	if err := db.Find(&synced).Error; err != nil {
		return res, err
	}
	known := make(map[string]models.InsightCreditRelease, len(synced))
	for _, s := range synced {
		known[s.ReleaseMBID] = s
	}

	present := make(map[string]bool, len(onDisk))
	var stale []models.CachedMusicBrainzRelease
	for _, id := range onDisk {
		present[id] = true
		entry, ok := modules.CachedReleaseEntry(id)
		if !ok {
			if _, had := known[id]; !had {
				res.Uncached++
			}
			continue
		}
		if s, had := known[id]; had && s.CachedAt.Equal(entry.Timestamp) && s.Version == creditsVersion {
			continue
		}
		stale = append(stale, entry)
	}
	var gone []string
	for id := range known {
		if !present[id] {
			gone = append(gone, id)
		}
	}
	if len(stale) == 0 && len(gone) == 0 {
		return res, nil
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		for _, ids := range chunk(gone, 500) {
			if err := tx.Where("release_mb_id IN ?", ids).Delete(&models.InsightCredit{}).Error; err != nil {
				return err
			}
			if err := tx.Where("release_mb_id IN ?", ids).Delete(&models.InsightCreditRelease{}).Error; err != nil {
				return err
			}
		}
		for _, entry := range stale {
			id := entry.Release.ID
			if err := tx.Where("release_mb_id = ?", id).Delete(&models.InsightCredit{}).Error; err != nil {
				return err
			}
			if credits := ReleaseCredits(entry.Release); len(credits) > 0 {
				if err := tx.CreateInBatches(credits, 200).Error; err != nil {
					return err
				}
			}
			row := models.InsightCreditRelease{ReleaseMBID: id, CachedAt: entry.Timestamp, Genre: topGenre(entry.Release), Version: creditsVersion}
			if err := tx.Save(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return SyncResult{}, err
	}
	res.Read, res.Dropped = len(stale), len(gone)
	return res, nil
}

// topGenre is a release group's strongest genre by MusicBrainz vote count.
func topGenre(release models.MusicBrainzReleaseResponse) string {
	best, genre := 0, ""
	for _, g := range release.ReleaseGroup.Genres {
		if g.Count > best || (g.Count == best && g.Name < genre) {
			best, genre = g.Count, g.Name
		}
	}
	return genre
}

func chunk(ids []string, size int) [][]string {
	var out [][]string
	for len(ids) > size {
		out = append(out, ids[:size])
		ids = ids[size:]
	}
	if len(ids) > 0 {
		out = append(out, ids)
	}
	return out
}

// ReleaseCredits lists every artist credited on each recording of a release: the
// track's artist credit, and the release's, the recording's and its works'
// relationships grouped by modules.CreditGroup — the same relationships
// modules.TrackCredits writes to a file. Video tracks are left out, as everywhere
// else an audio library is counted.
func ReleaseCredits(release models.MusicBrainzReleaseResponse) []models.InsightCredit {
	type key struct{ recording, artist, role string }
	seen := map[key]bool{}
	var out []models.InsightCredit
	add := func(recording string, artist models.Artist, name, role string) {
		if artist.ID == "" || recording == "" || role == "" {
			return
		}
		k := key{recording, artist.ID, role}
		if seen[k] {
			return
		}
		seen[k] = true
		if name == "" {
			name = artist.Name
		}
		out = append(out, models.InsightCredit{ReleaseMBID: release.ID, RecordingMBID: recording, ArtistMBID: artist.ID, Role: role, ArtistName: name})
	}
	relate := func(recording string, relations []models.MusicBrainzRelation) {
		for _, r := range relations {
			if r.TargetType == "artist" && r.Artist != nil {
				// The artist's own name, not the credited-as name: one node per artist.
				add(recording, *r.Artist, r.Artist.Name, modules.CreditGroup(r.Type))
			}
		}
	}

	for _, medium := range release.Media {
		for _, track := range medium.Tracks {
			if track.IsVideo() {
				continue
			}
			recording := track.Recording.ID
			credit := track.ArtistCredit
			if len(credit) == 0 {
				credit = track.Recording.ArtistCredit
			}
			for _, c := range credit {
				add(recording, c.Artist, c.Artist.Name, models.InsightRoleCredit)
			}
			relate(recording, release.Relations)
			relate(recording, track.Recording.Relations)
			for _, r := range track.Recording.Relations {
				if r.TargetType == "work" && r.Type == "performance" && r.Work != nil {
					relate(recording, r.Work.Relations)
				}
			}
		}
	}
	return out
}

package collection

import (
	"net/url"
	"strings"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Links into the managers' own UIs. The mirror pass records which manager holds each
// artist and album (models.ManagerLink); this file writes those rows and turns them
// back into URLs.

// recordArtistLinks replaces one manager's artist links with what its artist list
// just said, for the artists the collection holds. Best-effort: a failed write costs
// a link, never the sync.
func recordArtistLinks(db *gorm.DB, managerID uuid.UUID, listed []models.LidarrArtist, inCollection map[string]bool) {
	rows := make([]models.ManagerLink, 0, len(listed))
	for _, la := range listed {
		if la.ForeignArtistID != "" && inCollection[la.ForeignArtistID] {
			rows = append(rows, models.ManagerLink{ManagerID: managerID, EntityType: models.ManagerLinkArtist,
				MBID: la.ForeignArtistID, ExternalID: la.ID})
		}
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("manager_id = ? AND entity_type = ?", managerID, models.ManagerLinkArtist).
			Delete(&models.ManagerLink{}).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		return tx.CreateInBatches(rows, 200).Error
	})
	if err != nil {
		logger.Log.Warnf("failed to record which artists a manager holds: %s", err.Error())
	}
}

// recordAlbumLinks replaces one manager's album links for one artist with the albums
// it just listed under them.
func recordAlbumLinks(db *gorm.DB, managerID uuid.UUID, artistMBID string, albums []models.LidarrAlbum) {
	rows := make([]models.ManagerLink, 0, len(albums))
	seen := map[string]bool{}
	for _, al := range albums {
		if al.ForeignAlbumID == "" || seen[al.ForeignAlbumID] {
			continue
		}
		seen[al.ForeignAlbumID] = true
		rows = append(rows, models.ManagerLink{ManagerID: managerID, EntityType: models.ManagerLinkReleaseGroup,
			MBID: al.ForeignAlbumID, ArtistMBID: artistMBID, ExternalID: al.ID})
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("manager_id = ? AND entity_type = ? AND artist_mb_id = ?",
			managerID, models.ManagerLinkReleaseGroup, artistMBID).Delete(&models.ManagerLink{}).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		// An album filed under another artist of this manager already has its row:
		// that listing is as true as this one, so the first one stays.
		var have []string
		if err := tx.Model(&models.ManagerLink{}).Where("manager_id = ? AND entity_type = ? AND mb_id IN ?",
			managerID, models.ManagerLinkReleaseGroup, keysOf(seen)).Pluck("mb_id", &have).Error; err != nil {
			return err
		}
		taken := map[string]bool{}
		for _, id := range have {
			taken[id] = true
		}
		fresh := rows[:0]
		for _, r := range rows {
			if !taken[r.MBID] {
				fresh = append(fresh, r)
			}
		}
		if len(fresh) == 0 {
			return nil
		}
		return tx.CreateInBatches(fresh, 200).Error
	})
	if err != nil {
		logger.Log.Warnf("failed to record which albums a manager holds for %s: %s", artistMBID, err.Error())
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ExternalLink is one place outside Autotaggerr that holds an entity: a manager's page
// for it, or a media server's.
type ExternalLink struct {
	// Kind is the service ("lidarr", "plex"), for the UI's label and icon.
	Kind string `json:"kind"`
	// Name is the manager or library it is in, to tell two of a kind apart.
	Name string `json:"name"`
	URL  string `json:"url"`
	// ManagerID is the manager a link leads into, so a page read in one library can
	// keep only its own manager's links. Not serialised: the page needs the link, not
	// the row behind it.
	ManagerID uuid.UUID `json:"-"`
}

// LinksInLibrary keeps the links into the manager of one library — the links that
// belong on a page read in that library. A library with no manager keeps none: a
// native library's view does not link into a Lidarr, even one that also holds the
// album. Nil keeps every link (the overview).
func LinksInLibrary(db *gorm.DB, links []ExternalLink, library *uuid.UUID) []ExternalLink {
	if library == nil || len(links) == 0 {
		return links
	}
	var lib models.Library
	if err := db.First(&lib, "id = ?", *library).Error; err != nil || lib.ManagerID == nil {
		return []ExternalLink{}
	}
	kept := make([]ExternalLink, 0, len(links))
	for _, link := range links {
		if link.ManagerID == *lib.ManagerID {
			kept = append(kept, link)
		}
	}
	return kept
}

// ManagerLinks lists the managers holding an artist or an album, as links into each
// manager's UI, ordered by manager name. Lidarr addresses both by MusicBrainz ID —
// /artist/<mbid>, /album/<mbid> — so the stored row only has to say which manager.
// Disabled managers and managers with no address are left out: a link that cannot
// open is worse than none.
func ManagerLinks(db *gorm.DB, entityType, mbid string) ([]ExternalLink, error) {
	links, err := ManagerLinksFor(db, entityType, []string{mbid})
	return links[mbid], err
}

// ManagerLinksFor is ManagerLinks for many entities of one type in one query — what a
// discography needs, where every row may be held by a manager. Entities no manager
// holds are absent from the map.
func ManagerLinksFor(db *gorm.DB, entityType string, mbids []string) (map[string][]ExternalLink, error) {
	out := map[string][]ExternalLink{}
	if len(mbids) == 0 {
		return out, nil
	}
	var rows []struct {
		MBID string
		models.Manager
	}
	if err := db.Table("managers").Select("manager_links.mb_id AS mb_id, managers.*").
		Joins("JOIN manager_links ON manager_links.manager_id = managers.id").
		Where("manager_links.entity_type = ? AND manager_links.mb_id IN ? AND managers.enabled = ?", entityType, mbids, true).
		Order("managers.name").Scan(&rows).Error; err != nil {
		return nil, err
	}
	segment := "artist"
	if entityType == models.ManagerLinkReleaseGroup {
		segment = "album"
	}
	for _, row := range rows {
		m := row.Manager
		base := strings.TrimSpace(m.LidarrWebURL)
		if base == "" {
			base = strings.TrimSpace(m.LidarrBaseURL)
		}
		if base == "" || m.Type != models.ManagerTypeLidarr {
			continue
		}
		out[row.MBID] = append(out[row.MBID], ExternalLink{Kind: models.ManagerTypeLidarr, Name: m.Name,
			URL: strings.TrimRight(base, "/") + "/" + segment + "/" + url.PathEscape(row.MBID), ManagerID: m.ID})
	}
	return out, nil
}

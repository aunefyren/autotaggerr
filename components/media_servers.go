package components

import (
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// MediaServersForLibrary is the media servers a library is refreshed on: its links to
// enabled servers, each client narrowed to the linked section. Read from the rows on
// every call, the way BuildForLibrary reads the manager — so a server added, edited or
// linked takes effect on the next run, not the next restart. A nil db, a library with
// no links, and a failed read all give no servers: a refresh is a follow-up to a write
// that already happened, never a reason for one to fail.
func MediaServersForLibrary(db *gorm.DB, library models.Library) modules.PlexTargets {
	if db == nil {
		return nil
	}
	var links []models.LibraryMediaServer
	if err := db.Where("library_id = ?", library.ID).Find(&links).Error; err != nil {
		logger.Log.Warnf("failed to read the media servers of library %q: %s", library.Name, err.Error())
		return nil
	}
	if len(links) == 0 {
		return nil
	}
	servers := MediaServerClients(db)
	var out modules.PlexTargets
	for _, link := range links {
		if server, ok := servers[link.MediaServerID.String()]; ok {
			out = append(out, server.InSection(link.SectionKey))
		}
	}
	return out
}

// MediaServerClients is a client per enabled media server, by row ID — what a refresh
// flush needs to send each queued album to the server it came from.
func MediaServerClients(db *gorm.DB) map[string]*modules.PlexClient {
	out := map[string]*modules.PlexClient{}
	if db == nil {
		return out
	}
	var rows []models.MediaServer
	if err := db.Where("enabled = ? AND type = ?", true, models.MediaServerTypePlex).Order("name").Find(&rows).Error; err != nil {
		logger.Log.Warnf("failed to read the media servers: %s", err.Error())
		return out
	}
	for _, row := range rows {
		out[row.ID.String()] = modules.NewPlexServer(row)
	}
	return out
}

package routers

import (
	"net/http"

	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// plexSectionsView is what the library form needs to offer a Plex section: whether
// Plex is configured at all, its music sections, and — when it could not be asked —
// why, so the form can still save "every section" rather than failing.
type plexSectionsView struct {
	Configured bool                 `json:"configured"`
	Sections   []models.PlexSection `json:"sections"`
	Error      string               `json:"error,omitempty"`
}

// plexSections lists the Plex server's music sections. A Plex that does not answer
// is reported in the body with a 200: the form is still usable, and "every section"
// needs no list to be chosen.
func (a *API) plexSections(c *gin.Context) {
	if a.Plex == nil {
		c.JSON(http.StatusOK, plexSectionsView{Sections: []models.PlexSection{}})
		return
	}
	sections, err := a.Plex.MusicSections()
	if err != nil {
		c.JSON(http.StatusOK, plexSectionsView{Configured: true, Sections: []models.PlexSection{}, Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, plexSectionsView{Configured: true, Sections: sections})
}

// albumPlexLinks answers where an album is in Plex: one link per library holding its
// files, to the album Plex lists those files in. Separate from the album detail
// because a cold answer asks Plex — a few requests per section — and the page should
// not wait on that; a warm one is the folder cache the refresh path filled.
//
// One file per library is enough: every file of an album folder resolves to the same
// Plex album, and that is what the cache is keyed by. Libraries with Plex turned off,
// files whose release is not cached, and albums Plex does not hold give no link.
func (a *API) albumPlexLinks(c *gin.Context) {
	links := []collection.ExternalLink{}
	if a.Plex == nil {
		c.JSON(http.StatusOK, links)
		return
	}
	releases, err := collection.ReleaseGroupReleaseMBIDs(a.DB, c.Param("rgid"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to find the album's editions"})
		return
	}
	if len(releases) == 0 {
		c.JSON(http.StatusOK, links)
		return
	}
	var items []models.LibraryItem
	if err := a.DB.Where("mb_release_id IN ?", releases).Scopes(models.InDiskView).
		Order("path").Find(&items).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load the album's files"})
		return
	}
	var libraries []models.Library
	if err := a.DB.Order("name").Find(&libraries).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load the libraries"})
		return
	}
	first := map[uuid.UUID]models.LibraryItem{}
	for _, item := range items {
		if _, ok := first[item.LibraryID]; !ok {
			first[item.LibraryID] = item
		}
	}
	seen := map[string]bool{}
	for _, library := range libraries {
		item, ok := first[library.ID]
		client := a.Plex.ForLibrary(library)
		if !ok || client == nil {
			continue
		}
		key, found, err := modules.PlexAlbumForItem(client, item.Path, library.Path, models.Correlation{
			MBReleaseID: item.MBReleaseID, MBReleaseTrackID: item.MBReleaseTrackID, MBRecordingID: item.MBRecordingID,
		})
		if err != nil {
			logger.Log.Warnf("failed to find %s in Plex: %s", item.Path, err.Error())
			continue
		}
		if !found || key == "" || seen[key] {
			continue
		}
		seen[key] = true
		webURL, err := client.AlbumWebURL(key)
		if err != nil {
			logger.Log.Warnf("failed to build a Plex link: %s", err.Error())
			continue
		}
		links = append(links, collection.ExternalLink{Kind: "plex", Name: library.Name, URL: webURL})
	}
	c.JSON(http.StatusOK, links)
}

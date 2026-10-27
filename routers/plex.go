package routers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/components"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Media servers: the Plex servers told to refresh an album after its files change.
// Rows, like managers and download clients — zero, one or several — linked to the
// libraries they scan on the library form. See docs/media-servers.md.

type mediaServerInput struct {
	Name    *string `json:"name"`
	Type    *string `json:"type"`
	Enabled *bool   `json:"enabled"`
	BaseURL *string `json:"base_url"`
	WebURL  *string `json:"web_url"`
	// Token is write-only: settable here, never returned (json:"-" on the model).
	// Absent keeps the stored one.
	Token *string `json:"token"`
}

func (in mediaServerInput) apply(row *models.MediaServer) {
	if in.Name != nil {
		row.Name = strings.TrimSpace(*in.Name)
	}
	if in.Type != nil {
		row.Type = *in.Type
	}
	if in.Enabled != nil {
		row.Enabled = *in.Enabled
	}
	if in.BaseURL != nil {
		row.BaseURL = strings.TrimRight(strings.TrimSpace(*in.BaseURL), "/")
	}
	if in.WebURL != nil {
		row.WebURL = strings.TrimRight(strings.TrimSpace(*in.WebURL), "/")
	}
	if in.Token != nil {
		row.Token = strings.TrimSpace(*in.Token)
	}
}

func validateMediaServer(row models.MediaServer) string {
	switch {
	case row.Name == "":
		return "name is required"
	case row.Type != models.MediaServerTypePlex:
		return fmt.Sprintf("type must be %q", models.MediaServerTypePlex)
	case !strings.HasPrefix(row.BaseURL, "http://") && !strings.HasPrefix(row.BaseURL, "https://"):
		return "base_url must be an http:// or https:// URL"
	case row.WebURL != "" && !strings.HasPrefix(row.WebURL, "http://") && !strings.HasPrefix(row.WebURL, "https://"):
		return "web_url must be an http:// or https:// URL, or empty"
	case row.Token == "":
		return "token is required"
	}
	return ""
}

// mediaServerView is a server as the API returns it: the row, whether a token is
// stored — never the token — and how many libraries it scans.
type mediaServerView struct {
	models.MediaServer
	TokenSet  bool `json:"token_set"`
	Libraries int  `json:"libraries"`
}

func (a *API) viewMediaServer(row models.MediaServer) mediaServerView {
	var libraries int64
	a.DB.Model(&models.LibraryMediaServer{}).Where("media_server_id = ?", row.ID).Count(&libraries)
	return mediaServerView{MediaServer: row, TokenSet: row.Token != "", Libraries: int(libraries)}
}

func (a *API) loadMediaServer(c *gin.Context) (models.MediaServer, bool) {
	var row models.MediaServer
	id, ok := a.idParam(c)
	if !ok {
		return row, false
	}
	if err := a.DB.First(&row, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return row, false
	}
	return row, true
}

func (a *API) listMediaServers(c *gin.Context) {
	var rows []models.MediaServer
	if err := a.DB.Order("name").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list media servers"})
		return
	}
	out := make([]mediaServerView, 0, len(rows))
	for _, row := range rows {
		out = append(out, a.viewMediaServer(row))
	}
	c.JSON(http.StatusOK, out)
}

func (a *API) getMediaServer(c *gin.Context) {
	if row, ok := a.loadMediaServer(c); ok {
		c.JSON(http.StatusOK, a.viewMediaServer(row))
	}
}

func (a *API) mediaServerNameTaken(row models.MediaServer) bool {
	var clash int64
	a.DB.Model(&models.MediaServer{}).Where("name = ? AND id <> ?", row.Name, row.ID).Count(&clash)
	return clash > 0
}

func (a *API) createMediaServer(c *gin.Context) {
	var in mediaServerInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	row := models.MediaServer{Enabled: true, Type: models.MediaServerTypePlex}
	in.apply(&row)
	if problem := validateMediaServer(row); problem != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": problem})
		return
	}
	if a.mediaServerNameTaken(row) {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("a media server named %q already exists", row.Name)})
		return
	}
	if err := a.DB.Create(&row).Error; err != nil {
		logger.Log.Errorf("failed to create media server: %s", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create failed"})
		return
	}
	c.JSON(http.StatusCreated, a.viewMediaServer(row))
}

func (a *API) updateMediaServer(c *gin.Context) {
	row, ok := a.loadMediaServer(c)
	if !ok {
		return
	}
	var in mediaServerInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	in.apply(&row)
	if problem := validateMediaServer(row); problem != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": problem})
		return
	}
	if a.mediaServerNameTaken(row) {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("a media server named %q already exists", row.Name)})
		return
	}
	if err := a.DB.Save(&row).Error; err != nil {
		logger.Log.Errorf("failed to update media server %s: %s", row.ID, err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update failed"})
		return
	}
	c.JSON(http.StatusOK, a.viewMediaServer(row))
}

// deleteMediaServer removes a server and its library links. Nothing on the server
// changes; what Autotaggerr cached about its albums ages out on its own.
func (a *API) deleteMediaServer(c *gin.Context) {
	id, ok := a.idParam(c)
	if !ok {
		return
	}
	var deleted int64
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("media_server_id = ?", id).Delete(&models.LibraryMediaServer{}).Error; err != nil {
			return err
		}
		res := tx.Where("id = ?", id).Delete(&models.MediaServer{})
		deleted = res.RowsAffected
		return res.Error
	})
	switch {
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete failed"})
	case deleted == 0:
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
	default:
		c.Status(http.StatusNoContent)
	}
}

// testMediaServer probes a server with the token a refresh uses. Like a manager's
// test it answers 200 either way — a refused token is a test that worked.
func (a *API) testMediaServer(c *gin.Context) {
	row, ok := a.loadMediaServer(c)
	if !ok {
		return
	}
	healthy, probeErr := modules.NewPlexServer(row).HealthCheck()
	out := gin.H{"healthy": healthy}
	if probeErr != nil {
		out["error"] = probeErr.Error()
		logger.Log.Warnf("media server %q failed its connection test: %s", row.Name, probeErr.Error())
	}
	c.JSON(http.StatusOK, out)
}

// mediaServerSectionsView is what the library form needs to offer one server's
// sections: its music sections, and — when it could not be asked — why, so the form
// can still save "every section" rather than failing.
type mediaServerSectionsView struct {
	Sections []models.PlexSection `json:"sections"`
	Error    string               `json:"error,omitempty"`
}

// mediaServerSections lists one server's music sections. A server that does not answer
// is reported in the body with a 200: the form is still usable, and "every section"
// needs no list to be chosen.
func (a *API) mediaServerSections(c *gin.Context) {
	row, ok := a.loadMediaServer(c)
	if !ok {
		return
	}
	sections, err := modules.NewPlexServer(row).MusicSections()
	if err != nil {
		c.JSON(http.StatusOK, mediaServerSectionsView{Sections: []models.PlexSection{}, Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, mediaServerSectionsView{Sections: sections})
}

// albumPlexLinks answers where an album is in Plex: one link per library and server
// holding its files, to the album that server lists those files in. Separate from the
// album detail because a cold answer asks each server — a few requests per section —
// and the page should not wait on that; a warm one is the folder cache the refresh
// path filled.
//
// One file per library is enough: every file of an album folder resolves to the same
// album on a server, and that is what the cache is keyed by. Libraries with no media
// server, files whose release is not cached, and albums a server does not hold give
// no link.
func (a *API) albumPlexLinks(c *gin.Context) {
	library, err := parseLibraryParam(c.Query("library"))
	if err != nil {
		respondErr(c, err)
		return
	}
	links, err := a.plexAlbumLinks(c.Param("rgid"), library)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, links)
}

// plexAlbumLinks is albumPlexLinks' answer, shared with the MCP get_album tool. Read in
// one library (only non-nil), only that library's servers are linked. The error is a failed
// database read; a server not answering costs a link, not the call.
func (a *API) plexAlbumLinks(releaseGroupMBID string, only *uuid.UUID) ([]collection.ExternalLink, error) {
	links := []collection.ExternalLink{}
	releases, err := collection.ReleaseGroupReleaseMBIDs(a.DB, releaseGroupMBID)
	if err != nil {
		return nil, errors.New("failed to find the album's editions")
	}
	if len(releases) == 0 {
		return links, nil
	}
	var items []models.LibraryItem
	if err := a.DB.Where("mb_release_id IN ?", releases).Scopes(models.InDiskView).
		Order("path").Find(&items).Error; err != nil {
		return nil, errors.New("failed to load the album's files")
	}
	var libraries []models.Library
	if err := a.DB.Order("name").Find(&libraries).Error; err != nil {
		return nil, errors.New("failed to load the libraries")
	}
	first := map[uuid.UUID]models.LibraryItem{}
	for _, item := range items {
		if _, ok := first[item.LibraryID]; !ok {
			first[item.LibraryID] = item
		}
	}
	// The link says which library and, once there is more than one, which server.
	var serverCount int64
	a.DB.Model(&models.MediaServer{}).Where("enabled = ?", true).Count(&serverCount)
	seen := map[string]bool{}
	for _, library := range libraries {
		item, ok := first[library.ID]
		if !ok || (only != nil && library.ID != *only) {
			continue
		}
		for _, client := range components.MediaServersForLibrary(a.DB, library) {
			key, found, err := modules.PlexAlbumForItem(client, item.Path, library.Path, models.Correlation{
				MBReleaseID: item.MBReleaseID, MBReleaseTrackID: item.MBReleaseTrackID, MBRecordingID: item.MBRecordingID,
			})
			if err != nil {
				logger.Log.Warnf("failed to find %s on %s: %s", item.Path, client.DisplayName(), err.Error())
				continue
			}
			if !found || key == "" || seen[client.ServerID+key] {
				continue
			}
			seen[client.ServerID+key] = true
			webURL, err := client.AlbumWebURL(key)
			if err != nil {
				logger.Log.Warnf("failed to build a Plex link: %s", err.Error())
				continue
			}
			name := library.Name
			if serverCount > 1 {
				name += " · " + client.DisplayName()
			}
			links = append(links, collection.ExternalLink{Kind: "plex", Name: name, URL: webURL})
		}
	}
	return links, nil
}

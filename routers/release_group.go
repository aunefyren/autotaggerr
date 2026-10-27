package routers

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// maxReleaseGroupLength bounds a hand-set release group. It names a download's group
// ("GRP"), and it lands in file names through {Release Group}; anything near this long
// is a pasted folder name, not a group.
const maxReleaseGroupLength = 64

// setReleaseGroup records the download release group of one or more files by hand —
// what {Release Group} renders (LibraryItem.ReleaseGroup). It is not MusicBrainz's
// release group and writes no tags: the next Organise names the files by it.
//
// Refused (409) for files in a Lidarr library. Lidarr reports the group on its track
// file and processing records it on every run, so a hand-set value would be silently
// replaced — and Lidarr libraries are never organised, so it would name nothing.
// Detach does not lift this, unlike attach: a detached artist's files are still read
// from Lidarr, group included. Empty clears it to "none", which is how an import
// review records a cleared field.
func (a *API) setReleaseGroup(c *gin.Context) {
	var body struct {
		ItemIDs      []uuid.UUID `json:"item_ids"`
		ReleaseGroup string      `json:"release_group"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || len(body.ItemIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "item_ids is required"})
		return
	}
	group := strings.TrimSpace(body.ReleaseGroup)
	if len(group) > maxReleaseGroupLength {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("a release group is at most %d characters", maxReleaseGroupLength)})
		return
	}

	var items []models.LibraryItem
	if err := a.DB.Where("id IN ?", body.ItemIDs).Find(&items).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read items"})
		return
	}
	if len(items) != len(body.ItemIDs) {
		c.JSON(http.StatusNotFound, gin.H{"error": "item not found"})
		return
	}
	editable, err := a.librariesReleaseGroupEditable(items)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to resolve who records these files' release group"})
		return
	}
	for _, item := range items {
		if !editable[item.LibraryID] {
			c.JSON(http.StatusConflict, gin.H{"error": item.Path + " is in a library managed by Lidarr, which records its release group"})
			return
		}
	}

	if err := a.DB.Model(&models.LibraryItem{}).Where("id IN ?", body.ItemIDs).
		Update("release_group", group).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save the release group"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"updated": len(items), "release_group": group})
}

// librariesReleaseGroupEditable answers, per library of the given files, whether their
// release group may be set by hand: not where Lidarr manages the library. It is the
// library half of libraryIdentityEditable alone — detach lets a file's identity be
// attached by hand, but its group still comes from Lidarr's track file.
func (a *API) librariesReleaseGroupEditable(items []models.LibraryItem) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	for _, item := range items {
		if _, seen := out[item.LibraryID]; seen {
			continue
		}
		editable, err := a.libraryIdentityEditable(item.LibraryID)
		if err != nil {
			return nil, fmt.Errorf("library %s: %w", item.LibraryID, err)
		}
		out[item.LibraryID] = editable
	}
	return out, nil
}

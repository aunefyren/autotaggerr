package routers

import (
	"net/http"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/gin-gonic/gin"
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

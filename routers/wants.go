package routers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// releaseGroupSearchResponse is an album search. ReleaseMBID is set when the query
// was a pasted release link: the album it belongs to is the hit, and that edition is
// the one the picker opens ticked.
type releaseGroupSearchResponse struct {
	ReleaseGroups []models.MusicBrainzReleaseGroupSearchResult `json:"release_groups"`
	ReleaseMBID   string                                       `json:"release_mb_id,omitempty"`
}

// searchReleaseGroups searches albums for *Add album*. A pasted MusicBrainz link or
// ID is resolved through the search's own ID fields — an album by rgid, an edition by
// reid (the album holding it), an artist by arid (their albums) — so it costs no
// lookup the port does not already have.
func (a *API) searchReleaseGroups(c *gin.Context) {
	query := strings.TrimSpace(c.Query("q"))
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a search query is required"})
		return
	}
	resp := releaseGroupSearchResponse{}
	search := func(q string) bool {
		hits, err := a.meta().SearchReleaseGroups(q)
		if err != nil {
			logger.Log.Errorf("album search failed for %q: %s", q, err.Error())
			c.JSON(http.StatusBadGateway, gin.H{"error": "MusicBrainz search failed"})
			return false
		}
		resp.ReleaseGroups = hits
		return true
	}

	if ref, ok := modules.ParseMBIDInput(query); ok {
		switch ref.Entity {
		case "release-group":
			if !search("rgid:" + ref.MBID) {
				return
			}
		case "release":
			if !search("reid:" + ref.MBID) {
				return
			}
			resp.ReleaseMBID = ref.MBID
		case "artist":
			if !search("arid:" + ref.MBID) {
				return
			}
		case "recording":
			c.JSON(http.StatusBadRequest, gin.H{"error": "that is a song — search it under Track"})
			return
		default:
			// A bare ID: an album first, then the album an edition of that ID is on.
			if !search("rgid:" + ref.MBID) {
				return
			}
			if len(resp.ReleaseGroups) == 0 {
				if !search("reid:" + ref.MBID) {
					return
				}
				if len(resp.ReleaseGroups) > 0 {
					resp.ReleaseMBID = ref.MBID
				}
			}
		}
	} else if !search(query) {
		return
	}

	if resp.ReleaseGroups == nil {
		resp.ReleaseGroups = []models.MusicBrainzReleaseGroupSearchResult{}
	}
	c.JSON(http.StatusOK, resp)
}

// searchRecordings searches songs for *Add track*. A pasted recording link resolves
// to that song, an artist link to their songs.
func (a *API) searchRecordings(c *gin.Context) {
	query := strings.TrimSpace(c.Query("q"))
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a search query is required"})
		return
	}
	if ref, ok := modules.ParseMBIDInput(query); ok {
		switch ref.Entity {
		case "artist":
			query = "arid:" + ref.MBID
		case "release", "release-group":
			c.JSON(http.StatusBadRequest, gin.H{"error": "that is an album — search it under Album"})
			return
		default:
			query = "rid:" + ref.MBID
		}
	}
	hits, err := a.meta().SearchRecordings(query)
	if err != nil {
		logger.Log.Errorf("song search failed for %q: %s", query, err.Error())
		c.JSON(http.StatusBadGateway, gin.H{"error": "MusicBrainz search failed"})
		return
	}
	if hits == nil {
		hits = []models.MusicBrainzRecordingSearchResult{}
	}
	c.JSON(http.StatusOK, gin.H{"recordings": hits})
}

// addWant is *Add album* / *Add track*: want an album, editions of it or songs on it,
// adding its artist first when the collection does not hold them. Following is a
// separate request (POST /artists/:mbid/follow), as it is after *Add artist*, so a
// want is kept even when the discography sync behind a follow fails.
func (a *API) addWant(c *gin.Context) {
	var body struct {
		Artist struct {
			MBID string `json:"mb_id"`
			Name string `json:"name"`
		} `json:"artist"`
		ReleaseGroup struct {
			MBID             string `json:"mb_id"`
			Title            string `json:"title"`
			PrimaryType      string `json:"primary_type"`
			SecondaryTypes   string `json:"secondary_types"`
			FirstReleaseDate string `json:"first_release_date"`
		} `json:"release_group"`
		Picks []struct {
			ReleaseMBID    string   `json:"release_mb_id"`
			RecordingMBIDs []string `json:"recording_mb_ids"`
		} `json:"picks"`
		LibraryID string `json:"library_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	library, err := parseLibraryParam(body.LibraryID)
	if err != nil {
		respondErr(c, err)
		return
	}

	in := collection.AddWantInput{
		ArtistMBID:       body.Artist.MBID,
		ArtistName:       body.Artist.Name,
		ReleaseGroupMBID: body.ReleaseGroup.MBID,
		Title:            body.ReleaseGroup.Title,
		PrimaryType:      body.ReleaseGroup.PrimaryType,
		SecondaryTypes:   body.ReleaseGroup.SecondaryTypes,
		FirstReleaseDate: body.ReleaseGroup.FirstReleaseDate,
	}
	for _, p := range body.Picks {
		in.Picks = append(in.Picks, collection.WantPick{ReleaseMBID: p.ReleaseMBID, RecordingMBIDs: p.RecordingMBIDs})
	}

	res, err := a.wantFromSearch(in, library)
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"artist":       a.artistViewFor(res.Artist),
		"artist_added": res.ArtistAdded,
		"desires":      res.Desires,
	})
}

// wantFromSearch runs collection.AddWant with the API's library resolution and maps
// its refusals onto statuses. Shared by the HTTP handler and the MCP tool.
func (a *API) wantFromSearch(in collection.AddWantInput, library *uuid.UUID) (collection.AddWantResult, error) {
	res, err := collection.AddWant(a.DB, in, func(artistMBID string) (uuid.UUID, error) {
		return a.wantTarget(artistMBID, library)
	})
	var ae *apiError
	switch {
	case err == nil:
		return res, nil
	case errors.As(err, &ae):
		return res, err
	case errors.Is(err, collection.ErrAllLibrariesManaged):
		return res, failWith(http.StatusConflict, err.Error())
	default:
		return res, failWith(http.StatusBadRequest, err.Error())
	}
}

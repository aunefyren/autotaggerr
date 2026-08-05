package routers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Artwork is proxied rather than linked directly from the browser for three
// reasons: the fanart.tv API key must not leave the server, the disk cache means a
// cover is fetched once per install instead of once per visitor, and a page full
// of covers never reveals the user's IP to an external host.

// artworkProviders resolves the enabled artwork data sources. Both are optional;
// an absent source simply means that kind of image is unavailable, which the
// handler reports as a 204 and the UI renders as a monogram tile.
func artworkProviders(db *gorm.DB) modules.ArtworkProviders {
	providers := modules.ArtworkProviders{}

	var coverArt models.DataSource
	if err := db.Where("type = ? AND enabled = ?", models.DataSourceTypeCoverArtArchive, true).
		First(&coverArt).Error; err == nil {
		providers.CoverArtEnabled = true
		providers.CoverArtBaseURL = coverArt.BaseURL
	}

	var fanart models.DataSource
	if err := db.Where("type = ? AND enabled = ?", models.DataSourceTypeFanart, true).
		First(&fanart).Error; err == nil {
		providers.FanartEnabled = true
		providers.FanartBaseURL = fanart.BaseURL
		providers.FanartAPIKey = fanart.APIKey
	}

	return providers
}

// artworkMaxSize caps the requested edge length. Nothing in the UI renders larger
// than the album hero, and an unbounded number here is a request for the provider
// to send megabytes.
const artworkMaxSize = 1200

// artwork serves one image for a MusicBrainz entity:
//
//	GET /artwork/release-group/:mbid          front cover
//	GET /artwork/release/:mbid                front cover
//	GET /artwork/artist/:mbid?kind=thumb      artist portrait
//	GET /artwork/artist/:mbid?kind=background artist backdrop
//
// A missing image is a 204 with no body: the <img> tag's error event is what the UI
// acts on, and "this album has no cover" is the ordinary case, not something to
// report as a failure.
func (a *API) artwork(c *gin.Context) {
	entity := c.Param("entity")
	mbid := c.Param("mbid")

	kind := c.DefaultQuery("kind", modules.ArtworkKindFront)
	if entity == modules.ArtworkEntityArtist && c.Query("kind") == "" {
		kind = modules.ArtworkKindThumb
	}

	size := 250
	if raw := c.Query("size"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "size must be a positive number of pixels"})
			return
		}
		if parsed > artworkMaxSize {
			parsed = artworkMaxSize
		}
		size = parsed
	}

	art, err := modules.GetArtwork(artworkProviders(a.DB), entity, mbid, kind, size)
	if err != nil {
		if errors.Is(err, modules.ErrNoArtwork) {
			// 204, not 404. Most artists have no fanart.tv entry and most releases have
			// no cover, so an ordinary collection page asks for hundreds of images that
			// legitimately do not exist. As 404s that is indistinguishable from a client
			// probing for files, and log-watchers (fail2ban and friends) ban the user for
			// their own browsing — which is what this endpoint actually did.
			//
			// "No content" is also the more honest reading: the request was understood
			// and answered, and the answer is that there is no image. The resource is
			// the artwork *for* an entity, not a file that may or may not be there.
			//
			// An empty body still fires the <img> error event, so the UI's monogram tile
			// keeps working. Serving a placeholder image instead would suppress that
			// event and replace a tile showing the artist's initials with a generic
			// glyph — worse, to fix a problem that is about status codes.
			//
			// Cache the negative regardless. "No image for this MBID" is as stable an
			// answer as the image itself — a disabled provider will keep saying no, and
			// an enabled one rarely gains art an existing entity lacked — so without a
			// cache header the browser re-asks on every navigation. 204 is cacheable by
			// default (RFC 7231 §6.1), so this is honoured. A day is short enough that
			// enabling a provider is picked up the same session.
			c.Header("Cache-Control", "public, max-age=86400")
			c.Status(http.StatusNoContent)
			return
		}
		// A malformed URL is this caller's mistake, and no provider was contacted —
		// reporting it as a bad gateway would send someone looking at the wrong
		// end of the connection.
		if errors.Is(err, modules.ErrBadArtworkRequest) {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		// A provider that is down or misconfigured is worth distinguishing from one
		// that simply has no image, so the UI can fall back without pretending the
		// artwork does not exist.
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	// Immutable in practice: the cache key contains the MBID and the size, and the
	// art for a given release does not change. A week of browser caching is what
	// keeps a long collection list from re-asking on every navigation.
	c.Header("Cache-Control", "public, max-age=604800")
	if art.FromCache {
		c.Header("X-Artwork-Cache", "hit")
	}
	c.Data(http.StatusOK, art.ContentType, art.Data)
}

// artworkCapabilities reports which kinds of artwork can actually be served, so the
// UI can render a monogram tile directly instead of firing an <img> request that is
// certain to come back empty. It reflects the same truth the artwork handler enforces
// — fanart enabled but keyless still reports false, because it cannot resolve an
// image — so a provider that cannot deliver produces no requests at all.
//
// It answers the whole-provider case. The per-entity case (fanart configured, this
// particular artist unknown to it) cannot be known without asking, which is why the
// answer to that one is a 204 rather than an error.
//
//	GET /artwork-capabilities -> {"cover": bool, "artist": bool}
func (a *API) artworkCapabilities(c *gin.Context) {
	providers := artworkProviders(a.DB)
	c.JSON(http.StatusOK, gin.H{
		"cover":  providers.CoverArtEnabled,
		"artist": providers.FanartEnabled && strings.TrimSpace(providers.FanartAPIKey) != "",
	})
}

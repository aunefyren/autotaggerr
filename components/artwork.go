package components

import (
	"strings"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// ArtworkProviders resolves the enabled artwork data sources into the value
// modules.GetArtwork takes. Both are optional; an absent source simply means that
// kind of image is unavailable, which the API reports as a 204 and the UI renders
// as a monogram tile.
//
// It lives here rather than in modules because the artwork client is deliberately
// free of the database — providers are a value its caller supplies, exactly as the
// AcoustID client takes its key and base URL. It lives here rather than in routers
// because the artwork handler is no longer the only caller: the metadata refresh
// warms images ahead of a user asking for them, and two copies of these two queries
// would be two answers to "is fanart configured".
func ArtworkProviders(db *gorm.DB) modules.ArtworkProviders {
	providers := modules.ArtworkProviders{}
	if db == nil {
		return providers
	}

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

// CanServeCovers reports whether album covers can actually be fetched.
//
// Separate from the Enabled field because "configured" and "able to deliver" differ
// for the artist half, and a caller that warms images ahead of time must ask the
// same question the UI's capability endpoint asks. Fetching against a disabled
// provider is not merely wasted: GetArtwork records the resulting ErrNoArtwork as a
// negative cache entry, so a pass that ignored this would remember "this album has
// no cover" for every album in the collection and keep saying so for a week after
// the provider was switched on.
func CanServeCovers(providers modules.ArtworkProviders) bool {
	return providers.CoverArtEnabled
}

// CanServeArtistImages reports whether artist portraits and backdrops can be
// fetched. fanart.tv is useless without a personal key, so an enabled but keyless
// source answers false — the same truth the artwork handler enforces.
func CanServeArtistImages(providers modules.ArtworkProviders) bool {
	return providers.FanartEnabled && strings.TrimSpace(providers.FanartAPIKey) != ""
}

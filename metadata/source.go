// Package metadata declares the MetadataSource port: the set of network-fetching
// MusicBrainz calls the rest of the app depends on, expressed as an interface so
// it can be injected and faked. modules/ provides the concrete adapter
// (modules.NewMetadataSource) that routes each method through the cached,
// rate-limited free functions; a test hands in a fake with zero network.
//
// This package is a leaf: it imports only models, so every caller (collection,
// components, mirror, scan, routers) can depend on it without an import cycle,
// and a fake does not have to import modules.
package metadata

import "github.com/aunefyren/autotaggerr/models"

// MetadataSource is the port for MusicBrainz network fetches. Only the calls that
// actually hit the network live here; the cache/stats/mirror helpers stay as free
// functions in modules/ because they are already testable and are not fetch
// decisions.
type MetadataSource interface {
	GetRelease(mbID string) (models.MusicBrainzReleaseResponse, error)
	GetArtist(artistID string) (models.MusicBrainzArtistLookup, error)
	GetArtistReleaseGroups(artistID string) ([]models.MusicBrainzArtistReleaseGroup, bool, error)
	GetReleaseGroupReleases(releaseGroupID string) ([]models.MusicBrainzReleaseSearchResult, error)
	SearchReleases(query ReleaseSearchQuery) (ReleaseSearchPage, error)
	SearchArtists(query string) ([]models.MusicBrainzArtistSearchResult, error)
}

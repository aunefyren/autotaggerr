package modules

import (
	"github.com/aunefyren/autotaggerr/metadata"
	"github.com/aunefyren/autotaggerr/models"
)

// metadataSource is the concrete MetadataSource: it routes every method through
// the cached, rate-limited MusicBrainz free functions in this package. It holds no
// state — the caches and rate limiter are package-global — so the zero value works
// and NewMetadataSource is the seam callers inject and tests replace with a fake.
type metadataSource struct{}

// NewMetadataSource returns the real MusicBrainz-backed metadata source. Wire it in
// main beside the Lidarr/Plex clients and inject it where MB fetches happen; tests
// pass a fake implementing metadata.MetadataSource instead.
func NewMetadataSource() metadata.MetadataSource { return metadataSource{} }

func (metadataSource) GetRelease(mbID string) (models.MusicBrainzReleaseResponse, error) {
	return GetMusicBrainzRelease(mbID)
}

func (metadataSource) GetArtist(artistID string) (models.MusicBrainzArtistLookup, error) {
	return GetMusicBrainzArtist(artistID)
}

func (metadataSource) GetArtistReleaseGroups(artistID string) ([]models.MusicBrainzArtistReleaseGroup, bool, error) {
	return GetMusicBrainzArtistReleaseGroups(artistID)
}

func (metadataSource) GetReleaseGroupReleases(releaseGroupID string) ([]models.MusicBrainzReleaseSearchResult, error) {
	return GetMusicBrainzReleaseGroupReleases(releaseGroupID)
}

func (metadataSource) SearchReleases(query metadata.ReleaseSearchQuery) (metadata.ReleaseSearchPage, error) {
	return SearchMusicBrainzReleases(query)
}

func (metadataSource) SearchArtists(query string) ([]models.MusicBrainzArtistSearchResult, error) {
	return SearchMusicBrainzArtists(query)
}

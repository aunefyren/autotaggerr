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

// GetArtistReleaseGroups routes to the *cached* discography, not the pager beneath
// it. Pointing it at the pager meant every caller of the port — collection.SyncArtist
// above all, which runs on every follow toggle — paged the discography over the
// network each time, neither reading the cache nor filling it.
func (metadataSource) GetArtistReleaseGroups(artistID string) ([]models.MusicBrainzArtistReleaseGroup, bool, error) {
	return GetArtistDiscography(artistID)
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

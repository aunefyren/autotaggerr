package modules

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/models"
)

// CachedRelease returns a release from the in-memory cache without ever fetching.
// The collection aggregation uses it so building the "present" view can't trigger
// a wave of rate-limited MusicBrainz calls.
func CachedRelease(mbID string) (models.MusicBrainzReleaseResponse, bool) {
	musicbrainzReleaseCacheMu.RLock()
	defer musicbrainzReleaseCacheMu.RUnlock()
	cached, ok := musicbrainzReleaseCache[mbID]
	return cached.Release, ok
}

// artistReleaseGroupPageSize / maxArtistReleaseGroupPages bound a discography fetch.
const (
	artistReleaseGroupPageSize = 100
	maxArtistReleaseGroupPages = 5
)

// GetMusicBrainzArtistReleaseGroups fetches an artist's release-groups (their
// discography) from MusicBrainz, paging through results. Each request is rate
// limited; the caller filters to the release-group types it treats as "wanted".
func GetMusicBrainzArtistReleaseGroups(artistID string) ([]models.MusicBrainzArtistReleaseGroup, error) {
	var all []models.MusicBrainzArtistReleaseGroup

	for page := 0; page < maxArtistReleaseGroupPages; page++ {
		if err := RateLimit(); err != nil {
			return all, err
		}

		offset := page * artistReleaseGroupPageSize
		url := fmt.Sprintf("%s/release-group?artist=%s&limit=%d&offset=%d&fmt=json",
			musicbrainzBaseURL, artistID, artistReleaseGroupPageSize, offset)

		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return all, err
		}
		req.Header.Set("User-Agent", "Autotaggerr/"+files.ConfigFile.AutotaggerrVersion+" (https://github.com/aunefyren/autotaggerr)")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return all, fmt.Errorf("MusicBrainz request failed for artist %q: %w", artistID, err)
		}
		if resp.StatusCode != http.StatusOK {
			snippet := readBodySnippet(resp.Body)
			resp.Body.Close()
			return all, fmt.Errorf("MusicBrainz returned HTTP %d for artist %q: %s", resp.StatusCode, artistID, snippet)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return all, err
		}

		var parsed models.MusicBrainzArtistReleaseGroups
		if err := json.Unmarshal(body, &parsed); err != nil {
			return all, fmt.Errorf("failed to parse artist release-groups for %q: %w", artistID, err)
		}

		all = append(all, parsed.ReleaseGroups...)
		if len(parsed.ReleaseGroups) == 0 || offset+artistReleaseGroupPageSize >= parsed.Count {
			break
		}
	}

	return all, nil
}

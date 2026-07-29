package modules

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

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

// Discography lookups are cached in memory: browsing an artist pages through up to
// five rate-limited requests, so re-opening the same artist would otherwise stall
// the UI for seconds at a time. The cache is process-local and short-lived — a
// discography is reference data that changes rarely, and a restart costs one refetch.
var (
	artistDiscographyTTL     = 6 * time.Hour
	artistDiscographyCache   = map[string]cachedDiscography{}
	artistDiscographyCacheMu sync.RWMutex
)

type cachedDiscography struct {
	groups  []models.MusicBrainzArtistReleaseGroup
	expires time.Time
}

// GetArtistDiscography returns an artist's full release-group list, cached. Unlike
// the sync path it filters nothing: browsing a catalog should show the catalog, and
// deciding what counts as wanted is a separate question.
func GetArtistDiscography(artistID string) ([]models.MusicBrainzArtistReleaseGroup, error) {
	artistDiscographyCacheMu.RLock()
	cached, ok := artistDiscographyCache[artistID]
	artistDiscographyCacheMu.RUnlock()
	if ok && time.Now().Before(cached.expires) {
		return cached.groups, nil
	}

	groups, err := GetMusicBrainzArtistReleaseGroups(artistID)
	if err != nil {
		// Serve a stale copy rather than an empty page when MusicBrainz is down.
		if ok {
			return cached.groups, nil
		}
		return nil, err
	}

	artistDiscographyCacheMu.Lock()
	artistDiscographyCache[artistID] = cachedDiscography{groups: groups, expires: time.Now().Add(artistDiscographyTTL)}
	artistDiscographyCacheMu.Unlock()
	return groups, nil
}

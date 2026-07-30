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
//
// The second return value reports whether the discography is *complete* — that the
// paging reached the end rather than stopping at maxArtistReleaseGroupPages. Every
// caller that merely displays or upserts can ignore it, but a caller that treats
// absence from this list as meaningful (pruning rows MusicBrainz no longer has)
// must not: against a truncated list, "missing" means "past page five", and acting
// on it would delete real release-groups from the most prolific artists in a
// collection. An error return likewise yields complete=false, since a discography
// that failed halfway is indistinguishable from a short one.
func GetMusicBrainzArtistReleaseGroups(artistID string) ([]models.MusicBrainzArtistReleaseGroup, bool, error) {
	var all []models.MusicBrainzArtistReleaseGroup
	complete := false

	for page := 0; page < maxArtistReleaseGroupPages; page++ {
		if err := RateLimit(); err != nil {
			return all, false, err
		}

		offset := page * artistReleaseGroupPageSize
		url := fmt.Sprintf("%s/release-group?artist=%s&limit=%d&offset=%d&fmt=json",
			musicbrainzBaseURL, artistID, artistReleaseGroupPageSize, offset)

		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return all, false, err
		}
		req.Header.Set("User-Agent", "Autotaggerr/"+files.ConfigFile.AutotaggerrVersion+" (https://github.com/aunefyren/autotaggerr)")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return all, false, fmt.Errorf("MusicBrainz request failed for artist %q: %w", artistID, err)
		}
		if resp.StatusCode != http.StatusOK {
			snippet := readBodySnippet(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
				RecordDeletion(models.MigrationEntityArtist, artistID)
				return all, false, newGoneError(models.MigrationEntityArtist, artistID, resp.StatusCode, snippet)
			}
			return all, false, fmt.Errorf("MusicBrainz returned HTTP %d for artist %q: %s", resp.StatusCode, artistID, snippet)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return all, false, err
		}

		var parsed models.MusicBrainzArtistReleaseGroups
		if err := json.Unmarshal(body, &parsed); err != nil {
			return all, false, fmt.Errorf("failed to parse artist release-groups for %q: %w", artistID, err)
		}

		all = append(all, parsed.ReleaseGroups...)
		if len(parsed.ReleaseGroups) == 0 || offset+artistReleaseGroupPageSize >= parsed.Count {
			complete = true
			break
		}
	}

	return all, complete, nil
}

// GetMusicBrainzArtist fetches who an artist is — kind, origin, active years,
// genres. Rate limited like every other MusicBrainz call, and cached in the
// persistent entity cache (see musicbrainz_cache.go).
//
// Failure is not fatal to anything: the artist page renders from the database
// without it, so callers log and carry on rather than surfacing an error.
func GetMusicBrainzArtist(artistID string) (models.MusicBrainzArtistLookup, error) {
	var fresh models.MusicBrainzArtistLookup
	if mbCacheGet(models.MBEntityArtist, artistID, &fresh) {
		return fresh, nil
	}

	// Read the stale copy up front: every failure path below wants to fall back to
	// it, and after the request has failed there is nothing left to read it from.
	var stale models.MusicBrainzArtistLookup
	ok := mbCacheGetStale(models.MBEntityArtist, artistID, &stale)

	if err := RateLimit(); err != nil {
		return models.MusicBrainzArtistLookup{}, err
	}

	url := fmt.Sprintf("%s/artist/%s?inc=genres+tags&fmt=json", musicbrainzBaseURL, artistID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return models.MusicBrainzArtistLookup{}, err
	}
	req.Header.Set("User-Agent", "Autotaggerr/"+files.ConfigFile.AutotaggerrVersion+" (https://github.com/aunefyren/autotaggerr)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ok {
			// Stale beats blank: these are facts about a person, not live data.
			return stale, nil
		}
		return models.MusicBrainzArtistLookup{}, fmt.Errorf("MusicBrainz request failed for artist %q: %w", artistID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet := readBodySnippet(resp.Body)
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
			// Recorded even when a stale copy can be served: the cached artist is a
			// fine answer for *this* request, but the deletion is a fact about the
			// collection that has to outlive the cache entry.
			RecordDeletion(models.MigrationEntityArtist, artistID)
			if ok {
				return stale, nil
			}
			return models.MusicBrainzArtistLookup{}, newGoneError(models.MigrationEntityArtist, artistID, resp.StatusCode, snippet)
		}
		if ok {
			return stale, nil
		}
		return models.MusicBrainzArtistLookup{}, fmt.Errorf("MusicBrainz returned HTTP %d for artist %q: %s",
			resp.StatusCode, artistID, snippet)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return models.MusicBrainzArtistLookup{}, err
	}
	var parsed models.MusicBrainzArtistLookup
	if err := json.Unmarshal(body, &parsed); err != nil {
		return models.MusicBrainzArtistLookup{}, fmt.Errorf("failed to parse artist %q: %w", artistID, err)
	}

	// Same merge signal as releases: the id we got back is not the id we asked for.
	if parsed.ID != "" && parsed.ID != artistID {
		RecordRedirect(models.MigrationEntityArtist, artistID, parsed.ID, parsed.Name)
	}

	mbCachePut(models.MBEntityArtist, artistID, parsed)
	return parsed, nil
}

// GetArtistDiscography returns an artist's full release-group list, cached in the
// persistent entity cache. Browsing an artist pages through up to five
// rate-limited requests, so without a cache re-opening the same artist stalls the
// UI for seconds at a time.
//
// Unlike the sync path it filters nothing: browsing a catalog should show the
// catalog, and deciding what counts as wanted is a separate question.
func GetArtistDiscography(artistID string) ([]models.MusicBrainzArtistReleaseGroup, error) {
	var fresh []models.MusicBrainzArtistReleaseGroup
	if mbCacheGet(models.MBEntityDiscography, artistID, &fresh) {
		return fresh, nil
	}

	var stale []models.MusicBrainzArtistReleaseGroup
	ok := mbCacheGetStale(models.MBEntityDiscography, artistID, &stale)

	groups, _, err := GetMusicBrainzArtistReleaseGroups(artistID)
	if err != nil {
		// Serve a stale copy rather than an empty page when MusicBrainz is down.
		if ok {
			return stale, nil
		}
		return nil, err
	}

	mbCachePut(models.MBEntityDiscography, artistID, groups)
	return groups, nil
}

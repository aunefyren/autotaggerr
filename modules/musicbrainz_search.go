package modules

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/metadata"
	"github.com/aunefyren/autotaggerr/models"
)

// releaseSearchLimit bounds a manual-attach search page, and maxReleaseSearchLimit
// caps what a caller may ask for. A free-text search of a common album title
// returns hundreds of editions, so the fix for "I cannot find it" is a narrower
// query plus paging — not an unbounded list.
const (
	releaseSearchLimit    = 25
	maxReleaseSearchLimit = 100
)

// SearchMusicBrainzReleases runs a fielded release search. It is used by manual
// attach, where a human identifies a file MusicBrainz could not. Results are not
// cached: search queries are one-off and the picked release is fetched (and cached)
// separately by GetMusicBrainzRelease.
//
// The query/page types live in the metadata package so the MetadataSource port can
// name them without importing modules.
//
// A transient failure is retried once. Nothing stands in for a failed search — there
// is no cache to fall back to — so the alternative is telling someone who is in the
// middle of identifying a file to press the button again themselves.
func SearchMusicBrainzReleases(query metadata.ReleaseSearchQuery) (metadata.ReleaseSearchPage, error) {
	return retryTransient("release search", func() (metadata.ReleaseSearchPage, error) {
		return searchMusicBrainzReleasesOnce(query)
	})
}

func searchMusicBrainzReleasesOnce(query metadata.ReleaseSearchQuery) (metadata.ReleaseSearchPage, error) {
	if query.Empty() {
		return metadata.ReleaseSearchPage{}, nil
	}
	limit := query.Limit
	if limit <= 0 {
		limit = releaseSearchLimit
	}
	if limit > maxReleaseSearchLimit {
		limit = maxReleaseSearchLimit
	}
	offset := query.Offset
	if offset < 0 {
		offset = 0
	}

	lucene := query.Lucene()

	if err := RateLimit(); err != nil {
		return metadata.ReleaseSearchPage{}, err
	}

	endpoint := fmt.Sprintf("%s/release?query=%s&limit=%d&offset=%d&fmt=json",
		musicbrainzBaseURL, url.QueryEscape(lucene), limit, offset)

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return metadata.ReleaseSearchPage{}, err
	}
	req.Header.Set("User-Agent", "Autotaggerr/"+files.ConfigFile.AutotaggerrVersion+" (https://github.com/aunefyren/autotaggerr)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return metadata.ReleaseSearchPage{}, newTransientError(err, "MusicBrainz search failed")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet := readBodySnippet(resp.Body)
		if transientStatus(resp.StatusCode) {
			return metadata.ReleaseSearchPage{}, newTransientError(nil,
				"MusicBrainz unavailable for search %q (HTTP %d, retry later): %s", lucene, resp.StatusCode, snippet)
		}
		return metadata.ReleaseSearchPage{}, fmt.Errorf("MusicBrainz returned HTTP %d for search %q: %s",
			resp.StatusCode, lucene, snippet)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return metadata.ReleaseSearchPage{}, err
	}

	var parsed models.MusicBrainzReleaseSearchResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return metadata.ReleaseSearchPage{}, fmt.Errorf("failed to parse MusicBrainz search results: %w", err)
	}
	return metadata.ReleaseSearchPage{Count: parsed.Count, Offset: offset, Releases: parsed.Releases}, nil
}

// SearchResultFromRelease projects a full release onto the lean search-hit shape,
// so a release reached by pasting its MBID renders in the same list as a searched
// one instead of needing a second UI.
func SearchResultFromRelease(release models.MusicBrainzReleaseResponse) models.MusicBrainzReleaseSearchResult {
	hit := models.MusicBrainzReleaseSearchResult{
		ID:             release.ID,
		Title:          release.Title,
		Status:         release.Status,
		Date:           release.Date,
		Country:        release.Country,
		Disambiguation: release.Disambiguation,
		ArtistCredit:   release.ArtistCredit,
	}
	hit.ReleaseGroup.ID = release.ReleaseGroup.ID
	hit.ReleaseGroup.Title = release.ReleaseGroup.Title
	hit.ReleaseGroup.PrimaryType = release.ReleaseGroup.PrimaryType
	for _, medium := range release.Media {
		count := medium.TrackCount
		if count == 0 {
			count = len(medium.Tracks)
		}
		hit.Media = append(hit.Media, struct {
			Format     string `json:"format"`
			TrackCount int    `json:"track-count"`
		}{Format: medium.Format, TrackCount: count})
	}
	return hit
}

// mbidPattern matches a bare MusicBrainz ID. Anchoring on the surrounding
// non-hex boundary lets it pull the ID out of a pasted musicbrainz.org URL.
var mbidPattern = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// ParsedMBID is an entity reference recognised in what the user typed.
type ParsedMBID struct {
	Entity string // "release", "release-group", "artist", or "" when unqualified
	MBID   string
}

// ParseMBIDInput recognises a bare MBID or a pasted musicbrainz.org URL. It is the
// escape hatch for a release that search cannot surface: MusicBrainz's own site is
// always a better search engine than a form, so let the user use it and paste the
// result back. Entity is empty for a bare MBID, where the caller decides what to
// try.
func ParseMBIDInput(input string) (ParsedMBID, bool) {
	input = strings.TrimSpace(input)
	if input == "" {
		return ParsedMBID{}, false
	}
	mbid := mbidPattern.FindString(input)
	if mbid == "" {
		return ParsedMBID{}, false
	}
	mbid = strings.ToLower(mbid)

	entity := ""
	for _, candidate := range []string{"release-group", "release", "artist"} {
		// Match the URL path segment, not a substring of the title: "/release/<id>".
		if strings.Contains(strings.ToLower(input), "/"+candidate+"/") {
			entity = candidate
			break
		}
	}
	return ParsedMBID{Entity: entity, MBID: mbid}, true
}

// ReleaseTrack is one selectable track of a release, flattened across media so the
// attach UI can show a single numbered list. RecordingID is carried because it —
// not the release-scoped track ID — is the identity that survives across releases.
type ReleaseTrack struct {
	TrackID     string `json:"track_id"`
	RecordingID string `json:"recording_id"`
	Title       string `json:"title"`
	Position    int    `json:"position"`
	Number      string `json:"number"`
	Medium      int    `json:"medium"`
	MediumTitle string `json:"medium_title"`
	Length      int    `json:"length"`
}

// ReleaseTracks flattens a release's media into a single track list. It goes
// through GetMusicBrainzRelease, so a release already in the cache costs nothing.
func ReleaseTracks(release models.MusicBrainzReleaseResponse) []ReleaseTrack {
	var out []ReleaseTrack
	for _, medium := range release.Media {
		for _, track := range medium.Tracks {
			out = append(out, ReleaseTrack{
				TrackID:     track.ID,
				RecordingID: track.Recording.ID,
				Title:       track.Title,
				Position:    track.Position,
				Number:      track.Number,
				Medium:      medium.Position,
				MediumTitle: medium.Title,
				Length:      track.Length,
			})
		}
	}
	return out
}

// FindReleaseTrack locates a track within a release by its release-scoped track ID.
// Manual attach validates the caller's choice against the real release rather than
// trusting the request body, so a typo cannot pin a file to a nonexistent track.
func FindReleaseTrack(release models.MusicBrainzReleaseResponse, trackID string) (ReleaseTrack, bool) {
	for _, t := range ReleaseTracks(release) {
		if t.TrackID == trackID {
			return t, true
		}
	}
	return ReleaseTrack{}, false
}

// artistSearchLimit bounds an artist search — again a human picking one result.
const artistSearchLimit = 25

// SearchMusicBrainzArtists runs a free-text artist search, used to add an artist to
// the collection before owning any of their files.
func SearchMusicBrainzArtists(query string) ([]models.MusicBrainzArtistSearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}

	endpoint := fmt.Sprintf("%s/artist?query=%s&limit=%d&fmt=json",
		musicbrainzBaseURL, url.QueryEscape(query), artistSearchLimit)

	var parsed models.MusicBrainzArtistSearchResponse
	if err := musicbrainzGetJSON(endpoint, &parsed); err != nil {
		return nil, err
	}
	return parsed.Artists, nil
}

// releaseBrowseLimit bounds how many editions of one release-group are listed.
// Prolific groups (long-running reissue histories) can have dozens; this is enough
// to choose from without paging.
const releaseBrowseLimit = 100

// GetMusicBrainzReleaseGroupReleases lists every release (edition) of a
// release-group. This is the catalog side of "I want *this* specific release" —
// it browses MusicBrainz rather than reading what is owned on disk.
func GetMusicBrainzReleaseGroupReleases(releaseGroupID string) ([]models.MusicBrainzReleaseSearchResult, error) {
	releaseGroupID = strings.TrimSpace(releaseGroupID)
	if releaseGroupID == "" {
		return nil, nil
	}

	// Cached like the discography: the release-group page reads this on every open,
	// and an edition list changes about as often as a discography does.
	var fresh []models.MusicBrainzReleaseSearchResult
	if mbCacheGet(models.MBEntityEditions, releaseGroupID, &fresh) {
		return fresh, nil
	}

	var stale []models.MusicBrainzReleaseSearchResult
	ok := mbCacheGetStale(models.MBEntityEditions, releaseGroupID, &stale)

	// inc=media is required: browse (unlike search) omits format and track counts
	// without it, and those are exactly what distinguishes one edition from another.
	endpoint := fmt.Sprintf("%s/release?release-group=%s&inc=media&limit=%d&fmt=json",
		musicbrainzBaseURL, url.QueryEscape(releaseGroupID), releaseBrowseLimit)

	var parsed models.MusicBrainzReleaseBrowseResponse
	if err := musicbrainzGetJSON(endpoint, &parsed); err != nil {
		// Serve a stale list rather than an empty page when MusicBrainz is down.
		if ok {
			return stale, nil
		}
		return nil, err
	}

	mbCachePut(models.MBEntityEditions, releaseGroupID, parsed.Releases)
	return parsed.Releases, nil
}

// musicbrainzGetJSON performs a rate-limited, User-Agent'd GET and decodes JSON,
// retrying once if the request failed transiently. Factored out once a third call
// site appeared; the release fetch keeps its own version because it maps HTTP status
// codes to specific, actionable errors.
func musicbrainzGetJSON(endpoint string, out any) error {
	return retryTransientErr("request", func() error {
		return musicbrainzGetJSONOnce(endpoint, out)
	})
}

// musicbrainzGetJSONOnce is one attempt. Decoding into `out` on a retried attempt is
// safe because a failed attempt never reaches the decode — the transient cases all
// return before the body is read.
func musicbrainzGetJSONOnce(endpoint string, out any) error {
	if err := RateLimit(); err != nil {
		return err
	}

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Autotaggerr/"+files.ConfigFile.AutotaggerrVersion+" (https://github.com/aunefyren/autotaggerr)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return newTransientError(err, "MusicBrainz request failed")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet := readBodySnippet(resp.Body)
		if transientStatus(resp.StatusCode) {
			return newTransientError(nil, "MusicBrainz unavailable (HTTP %d, retry later): %s", resp.StatusCode, snippet)
		}
		return fmt.Errorf("MusicBrainz returned HTTP %d: %s", resp.StatusCode, snippet)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("failed to parse MusicBrainz response: %w", err)
	}
	return nil
}

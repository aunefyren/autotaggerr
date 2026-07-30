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

// ReleaseSearchQuery is a fielded release search. Every field is optional and
// ANDed with the others; Text is free text matched against the whole document.
//
// Fielded search exists because free text alone cannot separate the editions that
// actually differ: "Greatest Hits" matches thousands of releases, but
// artist + year + track count identifies one. The field names map onto
// MusicBrainz's Lucene schema for /ws/2/release.
type ReleaseSearchQuery struct {
	Text     string // free text, matched against all fields
	Artist   string // artist credit name
	ArtistID string // artist MBID — exact, and immune to spelling

	Release string // release title
	Date    string // year ("1977") or full date ("1977-11-15")
	Country string // release country code ("GB", "US", "XW" for worldwide)
	Format  string // medium format ("CD", "Vinyl", "Digital Media")
	Tracks  int    // total track count across all media
	Status  string // "Official", "Promotion", "Bootleg", "Pseudo-Release"
	CatNo   string // label catalogue number
	Barcode string // UPC/EAN

	Limit  int
	Offset int
}

// ReleaseSearchPage is one page of search hits. Count is MusicBrainz's total match
// count, not the page size — it is what tells the user there is more to page to.
type ReleaseSearchPage struct {
	Count    int                                     `json:"count"`
	Offset   int                                     `json:"offset"`
	Releases []models.MusicBrainzReleaseSearchResult `json:"releases"`
}

// Empty reports whether the query would search for nothing. Used to avoid burning
// a rate-limit slot on a request that cannot return anything useful.
func (q ReleaseSearchQuery) Empty() bool {
	return strings.TrimSpace(q.Text) == "" && strings.TrimSpace(q.Artist) == "" &&
		strings.TrimSpace(q.ArtistID) == "" &&
		strings.TrimSpace(q.Release) == "" && strings.TrimSpace(q.Date) == "" &&
		strings.TrimSpace(q.Country) == "" && strings.TrimSpace(q.Format) == "" &&
		strings.TrimSpace(q.Status) == "" && strings.TrimSpace(q.CatNo) == "" &&
		strings.TrimSpace(q.Barcode) == "" && q.Tracks <= 0
}

// Lucene renders the query in MusicBrainz's search syntax. Free text is passed
// through unescaped so a user who knows the syntax can write their own clause
// (`artist:Bee AND date:1977`); the structured fields are escaped and quoted,
// because those come from form inputs where a stray colon or bracket is a typo,
// not an operator.
func (q ReleaseSearchQuery) Lucene() string {
	var clauses []string
	if text := strings.TrimSpace(q.Text); text != "" {
		clauses = append(clauses, "("+text+")")
	}
	field := func(name, value string) {
		if value = strings.TrimSpace(value); value != "" {
			clauses = append(clauses, name+":"+quoteLucene(value))
		}
	}
	field("artist", q.Artist)
	field("arid", q.ArtistID)
	field("release", q.Release)
	field("date", q.Date)
	field("country", q.Country)
	field("format", q.Format)
	field("status", q.Status)
	field("catno", q.CatNo)
	field("barcode", q.Barcode)
	if q.Tracks > 0 {
		clauses = append(clauses, fmt.Sprintf("tracks:%d", q.Tracks))
	}
	return strings.Join(clauses, " AND ")
}

// luceneEscaper escapes what a quoted phrase still treats as syntax: the quote
// that would end it and the backslash that escapes. Everything else — colons,
// brackets, hyphens — is literal inside quotes, and escaping those would corrupt
// the very values that need to match exactly (an MBID is full of hyphens).
var luceneEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

func quoteLucene(value string) string { return `"` + luceneEscaper.Replace(value) + `"` }

// SearchMusicBrainzReleases runs a fielded release search. It is used by manual
// attach, where a human identifies a file MusicBrainz could not. Results are not
// cached: search queries are one-off and the picked release is fetched (and cached)
// separately by GetMusicBrainzRelease.
func SearchMusicBrainzReleases(query ReleaseSearchQuery) (ReleaseSearchPage, error) {
	if query.Empty() {
		return ReleaseSearchPage{}, nil
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
		return ReleaseSearchPage{}, err
	}

	endpoint := fmt.Sprintf("%s/release?query=%s&limit=%d&offset=%d&fmt=json",
		musicbrainzBaseURL, url.QueryEscape(lucene), limit, offset)

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return ReleaseSearchPage{}, err
	}
	req.Header.Set("User-Agent", "Autotaggerr/"+files.ConfigFile.AutotaggerrVersion+" (https://github.com/aunefyren/autotaggerr)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ReleaseSearchPage{}, fmt.Errorf("MusicBrainz search failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ReleaseSearchPage{}, fmt.Errorf("MusicBrainz returned HTTP %d for search %q: %s",
			resp.StatusCode, lucene, readBodySnippet(resp.Body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ReleaseSearchPage{}, err
	}

	var parsed models.MusicBrainzReleaseSearchResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ReleaseSearchPage{}, fmt.Errorf("failed to parse MusicBrainz search results: %w", err)
	}
	return ReleaseSearchPage{Count: parsed.Count, Offset: offset, Releases: parsed.Releases}, nil
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

// musicbrainzGetJSON performs a rate-limited, User-Agent'd GET and decodes JSON.
// Factored out once a third call site appeared; the release fetch keeps its own
// version because it maps HTTP status codes to specific, actionable errors.
func musicbrainzGetJSON(endpoint string, out any) error {
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
		return fmt.Errorf("MusicBrainz request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("MusicBrainz returned HTTP %d: %s", resp.StatusCode, readBodySnippet(resp.Body))
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

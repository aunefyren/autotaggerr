package modules

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/utilities"
)

var (
	lidarrArtistsCachePath        = "config/lidarr_artists.json"
	lidarrArtistsCacheDuration    = time.Hour // 1 hour
	lidarrArtistsCache            = map[string]models.CachedLidarrArtistRelease{}
	lidarrArtistsCacheMu          sync.RWMutex
	lidarrAlbumsCachePath         = "config/lidarr_albums.json"
	lidarrAlbumsCacheDuration     = time.Hour // 1 hour
	lidarrAlbumsCache             = map[string]models.CachedLidarrAlbumRelease{}
	lidarrAlbumsCacheMu           sync.RWMutex
	lidarrTracksCachePath         = "config/lidarr_tracks.json"
	lidarrTracksCacheDuration     = time.Hour // 1 hour
	lidarrTracksCache             = map[string]models.CachedLidarrTracksRelease{}
	lidarrTracksCacheMu           sync.RWMutex
	lidarrTrackFilesCachePath     = "config/lidarr_trackfiles.json"
	lidarrTrackFilesCacheDuration = time.Hour // 1 hour
	lidarrTrackFilesCache         = map[string]models.CachedLidarrTrackFilesRelease{}
	lidarrTrackFilesCacheMu       sync.RWMutex
)

// LidarrInvalidateCaches drops every Lidarr cache, in memory and in the database.
// The next lookup re-fetches from Lidarr, which is how a release selection changed
// in Lidarr reaches GetMonitoredAlbumMBID before the 1h TTL would otherwise expire. It is a whole-cache flush rather than artist-scoped:
// the album/track/trackfile caches are keyed by Lidarr's own IDs, not by artist, so a
// scoped drop would have to walk their values to map back — not worth it for an action
// the user triggers by hand, and re-fetching from Lidarr is not rate-limited the way
// MusicBrainz is.
func LidarrInvalidateCaches() {
	lidarrArtistsCacheMu.Lock()
	lidarrArtistsCache = map[string]models.CachedLidarrArtistRelease{}
	lidarrArtistsCacheMu.Unlock()

	lidarrAlbumsCacheMu.Lock()
	lidarrAlbumsCache = map[string]models.CachedLidarrAlbumRelease{}
	lidarrAlbumsCacheMu.Unlock()

	lidarrTracksCacheMu.Lock()
	lidarrTracksCache = map[string]models.CachedLidarrTracksRelease{}
	lidarrTracksCacheMu.Unlock()

	lidarrTrackFilesCacheMu.Lock()
	lidarrTrackFilesCache = map[string]models.CachedLidarrTrackFilesRelease{}
	lidarrTrackFilesCacheMu.Unlock()

	// The rows go too. Emptying only the maps would have the next restart restore
	// exactly what the user asked to discard.
	for _, source := range []string{
		models.ProviderCacheLidarrArtists, models.ProviderCacheLidarrAlbums,
		models.ProviderCacheLidarrTracks, models.ProviderCacheLidarrTrackFiles,
	} {
		providerCacheDropSource(source)
	}

	logger.Log.Debug("invalidated Lidarr caches (artists, albums, tracks, trackfiles)")
}

// ErrLidarrArtistNotFound means Lidarr answered, and none of the artists it manages
// has a folder matching the file's artist directory. It is a distinct sentinel because
// it is the one Lidarr failure that is about *the library*, not about Lidarr: nothing
// is broken, the two sides simply disagree on a folder name. Callers use it to say so
// instead of reporting a lookup failure.
var ErrLidarrArtistNotFound = errors.New("artist folder not found in Lidarr")

// must be local in the file
type LidarrClient struct {
	BaseURL   string
	APIKey    string
	HTTP      *http.Client
	RateLimit func(func() error) error // optional: your 1 rps limiter
	Cookie    *string
}

// create new Lidarr client with url, api key...
func NewLidarrClient(baseURL, apiKey string, cookie *string) *LidarrClient {
	return &LidarrClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: 15 * time.Second},
		Cookie:  cookie,
	}
}

// lidarrBodySnippet caps how much of a response body an error quotes. A proxy login
// page is tens of kilobytes of HTML; the first few hundred bytes identify it.
const lidarrBodySnippet = 400

// retrieves the Lidarr API path JSON.
//
// Every error names the endpoint, where the request actually ended up, and — when the
// answer was not JSON — what it was instead. Lidarr normally sits behind a reverse
// proxy (Authelia and friends), and the two failures that look identical from inside
// a decoder are exactly the ones worth telling apart: "Lidarr said no" and "the proxy
// answered a login page instead of Lidarr". A bare `invalid character '<'` names
// neither, and neither does a status line when the portal replies 200.
func (c *LidarrClient) getJSON(pathWithQuery string, dst any) error {
	url := c.BaseURL + pathWithQuery
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("lidarr GET %s: could not build request: %w", url, err)
	}
	req.Header.Set("X-Api-Key", c.APIKey)
	req.Header.Set("Accept", "application/json")

	// An empty cookie is not a cookie: setting the header anyway makes the trace log
	// claim credentials were sent when nothing was.
	sentCookie := c.Cookie != nil && *c.Cookie != ""
	if sentCookie {
		req.Header.Set("Cookie", *c.Cookie)
	}

	do := func() error {
		logger.Log.Tracef("lidarr GET %s (api key: %t, cookie: %t)", url, c.APIKey != "", sentCookie)

		resp, err := c.HTTP.Do(req)
		if err != nil {
			return fmt.Errorf("lidarr GET %s: request failed: %w", url, err)
		}
		defer resp.Body.Close()

		// Where the request ended up is load-bearing. Go strips the Cookie header when
		// a redirect crosses to another host, so a proxy that bounces an API call to its
		// login portal gets answered without the credentials we carefully set — and the
		// portal happily returns 200.
		where := "lidarr GET " + url
		redirected := false
		if resp.Request != nil && resp.Request.URL != nil && resp.Request.URL.String() != url {
			where += " (redirected to " + resp.Request.URL.String() + ")"
			redirected = true
		}

		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, lidarrBodySnippet))
			return fmt.Errorf("%s -> %d %s: %s%s", where, resp.StatusCode,
				http.StatusText(resp.StatusCode), strings.TrimSpace(string(b)),
				authHint(resp.StatusCode, redirected, sentCookie))
		}

		// Peek rather than buffer the whole body: the artist list is megabytes, and all
		// an error needs is the first line of whatever came back.
		buffered := bufio.NewReader(resp.Body)
		head, _ := buffered.Peek(lidarrBodySnippet)
		contentType := resp.Header.Get("Content-Type")

		if err := json.NewDecoder(buffered).Decode(dst); err != nil {
			return fmt.Errorf("%s -> 200 but the body is not JSON (content-type %q): %w; body starts: %q%s",
				where, contentType, err, strings.TrimSpace(string(head)),
				authHint(resp.StatusCode, redirected, sentCookie))
		}
		return nil
	}

	if c.RateLimit != nil {
		return c.RateLimit(do)
	}
	return do()
}

// authHint appends the reading that turns a raw HTTP outcome into an instruction, for
// the responses that mean "you never reached Lidarr". Everything else gets nothing —
// a hint on an error that is not about auth is noise that outlives its usefulness.
func authHint(status int, redirected, sentCookie bool) string {
	switch {
	case redirected:
		hint := " — the request was redirected, which usually means an authentication proxy answered instead of Lidarr"
		if sentCookie {
			hint += "; note that Go drops the configured Cookie header when a redirect crosses to another host, so the session never reached the target"
		}
		return hint
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		if sentCookie {
			return " — credentials were sent (API key + cookie) but rejected; the cookie may have expired"
		}
		return " — no cookie is configured for this manager; if Lidarr is behind an authentication proxy it needs one"
	default:
		return ""
	}
}

// FindArtistByName searches the Lidarr artist list for one whose folder name matches artistName.
func (c *LidarrClient) FindArtistByName(artistName string) ([]models.LidarrArtist, error) {
	// Return any fresh cached artist(s) matching the name; only fall through to
	// the API when nothing fresh is cached. (Previously inverted: a fresh entry
	// forced a refetch and a stale entry was served — so /api/v1/artist was hit on
	// essentially every file, a real drag on full-library scans.)
	foundCachedArtist := []models.LidarrArtist{}
	lidarrArtistsCacheMu.RLock()
	for _, cachedArtist := range lidarrArtistsCache {
		if strings.EqualFold(cachedArtist.Artist.Name, artistName) &&
			time.Since(cachedArtist.Timestamp) < lidarrArtistsCacheDuration {
			foundCachedArtist = append(foundCachedArtist, cachedArtist.Artist)
		}
	}
	lidarrArtistsCacheMu.RUnlock()

	if len(foundCachedArtist) > 0 {
		logger.Log.Debug("returning cached Lidarr artist(s)")
		return foundCachedArtist, nil
	}

	logger.Log.Debug("cached artist not found for: " + artistName)

	// get lidarr API response
	var artists []models.LidarrArtist
	if err := c.getJSON("/api/v1/artist", &artists); err != nil {
		return nil, err
	}

	want := strings.ToLower(strings.TrimSpace(artistName))
	logger.Log.Debugf("we want artist: %s", want)

	validArtists := []models.LidarrArtist{}
	stored := make([]providerCacheItem, 0, len(artists))
	lidarrArtistsCacheMu.Lock()
	for i := range artists {
		// add artist to cache
		key := strconv.FormatInt(artists[i].ID, 10)
		entry := models.CachedLidarrArtistRelease{
			Artist:    artists[i],
			Timestamp: time.Now(),
		}
		lidarrArtistsCache[key] = entry
		stored = append(stored, providerCacheItem{key: key, value: entry})

		// Extract last folder from Lidarr's stored path
		lidarrArtistFolder := filepath.Base(utilities.NormPath(artists[i].Path))
		logger.Log.Debugf("comparing artist folder: %s, original path: %s", lidarrArtistFolder, artists[i].Path)

		if strings.EqualFold(lidarrArtistFolder, want) {
			validArtists = append(validArtists, artists[i])
		}
	}
	lidarrArtistsCacheMu.Unlock()

	// The whole artist list was just (re)populated, so it persists as one statement
	// rather than one per artist.
	providerCachePutMany(models.ProviderCacheLidarrArtists, lidarrArtistsCacheDuration, stored...)

	if len(validArtists) == 1 {
		return validArtists, nil
	} else if len(validArtists) > 1 {
		logger.Log.Warnf("multiple artists found in Lidarr by that name %s", artistName)
		return validArtists, nil
	}

	// Name what was compared, not just that it failed. The match is folder-to-folder
	// (our artist directory against the last segment of Lidarr's stored path), never
	// artist-name-to-artist-name, so "Lidarr obviously has this artist" and "no match"
	// are entirely compatible — a differently spelled folder on either side is the
	// usual cause, and the message has to point at the folders to be actionable.
	return nil, fmt.Errorf("%w: no Lidarr artist has folder %q (compared against the last path segment of all %d artists Lidarr returned)",
		ErrLidarrArtistNotFound, artistName, len(artists))
}

// getTrackFilesByArtist returns all Lidarr track files for an artist, cached per
// artist ID. Without the cache this endpoint is re-fetched for every track in the
// library (each call returns the artist's entire track file list), which is a
// major driver of full-library scan time.
func (c *LidarrClient) getTrackFilesByArtist(artistID int64) ([]models.LidarrTrackFile, error) {
	key := strconv.FormatInt(artistID, 10)

	lidarrTrackFilesCacheMu.RLock()
	cached, ok := lidarrTrackFilesCache[key]
	lidarrTrackFilesCacheMu.RUnlock()
	if ok && time.Since(cached.Timestamp) < lidarrTrackFilesCacheDuration {
		logger.Log.Debug("returning cached Lidarr track files for artist ID: " + key)
		return cached.TrackFiles, nil
	}

	logger.Log.Debug("cached track files not found for artist ID: " + key)

	var files []models.LidarrTrackFile
	if err := c.getJSON(fmt.Sprintf("/api/v1/trackfile?artistId=%d", artistID), &files); err != nil {
		return nil, err
	}

	entry := models.CachedLidarrTrackFilesRelease{
		TrackFiles: files,
		Timestamp:  time.Now(),
	}
	lidarrTrackFilesCacheMu.Lock()
	lidarrTrackFilesCache[key] = entry
	lidarrTrackFilesCacheMu.Unlock()
	providerCachePut(models.ProviderCacheLidarrTrackFiles, key, entry, lidarrTrackFilesCacheDuration)

	return files, nil
}

// retrieves the Lidarr track object from a Lidarr artist ID and track file path.
// Matches on (album folder, optional media folder, file basename). The media folder
// (CD1/CD2/Disc 2…) MUST be part of the match: a multi-disc release routinely repeats
// a track basename across discs, and ignoring the media folder let the wrong disc's
// trackfile win — resolving the file to a different MB track, so its disc/position tags
// never converged and every scan rewrote it (an endless-retag loop).
//
// Lidarr's stored paths sit under a different root than ours (a container mapping), so
// we cannot re-split them against rootDir; we compare by path *position* instead. A
// candidate path is structurally ambiguous without its root — …/A/B/file could be
// album B (flat) or album A + media B — so each candidate is tested under both
// readings, and a match requires album AND media (both empty, equal, or naming the
// same disc number) to agree.
//
// If two candidates still fit, the triple did not identify the file and no match is
// returned: guessing between two discs is the very failure this function exists to
// prevent, and the caller's "unmatched" is the honest answer.
func (c *LidarrClient) FindTrackFileByPath(artistID int64, fullTrackPath string, rootDir string) (*models.LidarrTrackFile, error) {
	files, err := c.getTrackFilesByArtist(artistID)
	if err != nil {
		return nil, err
	}
	if len(files) < 1 {
		logger.Log.Errorf("no Lidarr track files found for artist ID: %d", artistID)
		return nil, nil
	}

	// get album name from file path
	targetAlbum, err := utilities.ExtractAlbumNameFromTrackFilePath(rootDir, fullTrackPath)
	if err != nil {
		return nil, err
	}

	// get the optional media folder (CD1/Disc 2/…); "" when the album folder holds the
	// tracks directly. Its error case is only an empty segment, which a valid path
	// cannot produce, so treat any failure as "no media folder".
	targetMedia, err := utilities.ExtractMediaNameFromTrackFilePath(rootDir, fullTrackPath)
	if err != nil {
		targetMedia = ""
	}

	// get track file name from path
	targetFile, err := utilities.ExtractTrackFileName(fullTrackPath)
	if err != nil {
		return nil, err
	}

	// clean strings
	tAlbum := utilities.Canon(targetAlbum)
	tMedia := utilities.Canon(targetMedia)
	tFile := utilities.Canon(targetFile)

	logger.Log.Trace("target album: " + tAlbum + " | target media: " + tMedia + " | target file: " + tFile)

	var match *models.LidarrTrackFile
	for i := range files {
		logger.Log.Trace(files[i].Path)
		parent := utilities.Canon(utilities.BaseDirOfPathAny(files[i].Path))             // album (flat) or media
		grandparent := utilities.Canon(utilities.GrandfatherDirOfPathAny(files[i].Path)) // artist (flat) or album
		fFile := utilities.Canon(utilities.BaseOfPathAny(files[i].Path))

		if fFile != tFile {
			continue
		}

		// Reading A: candidate is flat (parent == album, no media folder).
		flatMatch := parent == tAlbum && tMedia == ""
		// Reading B: candidate has a media folder (grandparent == album, parent == media).
		mediaMatch := tMedia != "" && grandparent == tAlbum && mediaFoldersAgree(parent, tMedia)

		logger.Log.Trace("compare album=" + grandparent + "/" + parent + " file=" + fFile + " against target")

		if !flatMatch && !mediaMatch {
			continue
		}

		// Two candidates for one file means the (album, media, basename) triple did not
		// identify it after all — the multi-disc case this match exists for. Taking the
		// first would be a coin flip between two discs, so refuse instead: an unmatched
		// file is visible and fixable, a silently mistagged one is neither.
		if match != nil {
			logger.Log.Warnf("ambiguous Lidarr trackfile match for %q: both %q and %q fit; refusing to guess",
				fullTrackPath, match.Path, files[i].Path)
			return nil, nil
		}
		match = &files[i]
	}

	// return error if no match
	if match == nil {
		logger.Log.Warnf("trackfile not found by album+media+file; album=%q media=%q file=%q", targetAlbum, targetMedia, targetFile)
	}

	return match, nil
}

// mediaFoldersAgree compares a candidate's media folder with the file's own. Equal
// names agree; when both name a disc number, the number decides, so "CD 02" on disk
// still matches "CD2"/"Disc 2" in Lidarr's stored path. Without that, a padding or
// wording difference between the two sides drops the file to unmatched — the same
// evidence lost, just failing the other way.
func mediaFoldersAgree(candidate, target string) bool {
	if candidate == target {
		return true
	}
	if candidate == "" || target == "" {
		return false
	}
	candidateDisc := discNumberFromFolderName(candidate)
	return candidateDisc > 0 && candidateDisc == discNumberFromFolderName(target)
}

// GetArtists returns every artist Lidarr manages (with their MusicBrainz IDs),
// used by the collection mirror to match Autotaggerr artists to Lidarr.
func (c *LidarrClient) GetArtists() ([]models.LidarrArtist, error) {
	var artists []models.LidarrArtist
	if err := c.getJSON("/api/v1/artist", &artists); err != nil {
		return nil, err
	}
	return artists, nil
}

// GetArtistAlbums returns an artist's albums with monitoring + have/total track
// statistics, which the mirror maps onto owned/wanted release-groups.
func (c *LidarrClient) GetArtistAlbums(artistID int64) ([]models.LidarrAlbum, error) {
	var albums []models.LidarrAlbum
	if err := c.getJSON(fmt.Sprintf("/api/v1/album?artistId=%d", artistID), &albums); err != nil {
		return nil, err
	}
	return albums, nil
}

// retrieves the Lidarr album object from a Lidarr artist ID and album ID
func (c *LidarrClient) GetMonitoredAlbumMBID(artistID, albumID int64) (*string, error) {
	albumKey := strconv.FormatInt(albumID, 10)

	lidarrAlbumsCacheMu.RLock()
	cached, ok := lidarrAlbumsCache[albumKey]
	lidarrAlbumsCacheMu.RUnlock()
	// Guard on Album.ID: only trust a cache entry that actually holds the album we
	// asked for (defends against any legacy poisoned entries — see below).
	if ok && cached.Album.ID == albumID {
		logger.Log.Trace("cached entry found for Lidarr album")
		if time.Since(cached.Timestamp) < lidarrAlbumsCacheDuration {
			for _, r := range cached.Album.Releases {
				if r.Monitored && r.ForeignReleaseID != "" {
					logger.Log.Debug("returning cached album release: " + albumKey)
					return &r.ForeignReleaseID, nil
				}
			}
		}
		logger.Log.Trace("cached entry not found for album release")
	}

	// includeAllArtistAlbums=true returns every album for the artist, so each must
	// be cached under ITS OWN id. Previously they were all written under the
	// requested albumKey, which transiently poisoned that key with other albums and
	// — under concurrency — made a parallel lookup return the wrong release.
	var albums []models.LidarrAlbum
	q := fmt.Sprintf("/api/v1/album?artistId=%d&albumIds=%d&includeAllArtistAlbums=true", artistID, albumID)
	if err := c.getJSON(q, &albums); err != nil {
		return nil, err
	}

	lidarrAlbumsCacheMu.Lock()
	now := time.Now()
	stored := make([]providerCacheItem, 0, len(albums))
	for _, a := range albums {
		key := strconv.FormatInt(a.ID, 10)
		entry := models.CachedLidarrAlbumRelease{
			Album:     a,
			Timestamp: now,
		}
		lidarrAlbumsCache[key] = entry
		stored = append(stored, providerCacheItem{key: key, value: entry})
	}
	lidarrAlbumsCacheMu.Unlock()
	providerCachePutMany(models.ProviderCacheLidarrAlbums, lidarrAlbumsCacheDuration, stored...)

	for _, a := range albums {
		if a.ID != albumID {
			continue
		}
		for _, r := range a.Releases {
			if r.Monitored && r.ForeignReleaseID != "" {
				return &r.ForeignReleaseID, nil
			}
		}
		// Named, with the count, because this is the one Lidarr state that looks like a
		// bug in Autotaggerr: an album whose releases are all unmonitored has no edition
		// to tag against, so its files go unmatched while everything else about the
		// album looks healthy. The fix is in Lidarr, and the message has to say so.
		logger.Log.Errorf("Lidarr album %q (%d) has no monitored release out of %d — pick an edition for it in Lidarr, "+
			"or its files cannot be matched", a.Title, albumID, len(a.Releases))
		return nil, nil
	}

	logger.Log.Errorf("Lidarr returned no album with ID %d for artist %d", albumID, artistID)
	return nil, nil
}

func (c *LidarrClient) GetTracksByAlbumAndArtistID(artistID int64, albumID int64) ([]models.LidarrTrack, error) {
	albumKey := strconv.FormatInt(albumID, 10)

	lidarrTracksCacheMu.RLock()
	cached, ok := lidarrTracksCache[albumKey]
	lidarrTracksCacheMu.RUnlock()
	if ok && time.Since(cached.Timestamp) < lidarrTracksCacheDuration {
		logger.Log.Debug("returning cached tracks for album: " + albumKey)
		return cached.Tracks, nil
	}

	logger.Log.Debug("cached tracks not found for album ID: " + albumKey)

	var t []models.LidarrTrack
	if err := c.getJSON(fmt.Sprintf("/api/v1/track?artistId=%d&albumId=%d", artistID, albumID), &t); err != nil {
		return nil, err
	}

	if t == nil {
		logger.Log.Error("no Lidarr tracks found for album and artist ID")
		return nil, nil
	}

	// add tracks to cache
	tracks := models.CachedLidarrTracksRelease{
		Tracks:    t,
		Timestamp: time.Now(),
	}
	lidarrTracksCacheMu.Lock()
	lidarrTracksCache[albumKey] = tracks
	lidarrTracksCacheMu.Unlock()
	providerCachePut(models.ProviderCacheLidarrTracks, albumKey, tracks, lidarrTracksCacheDuration)

	return t, nil
}

// The four Lidarr caches are warmed from the database at startup and written
// through as they are populated. Each keeps its legacy JSON file readable exactly
// once, so an upgrade does not re-ask Lidarr for every artist, album and track file
// it already knew.

func LidarrLoadArtistsCache() error {
	providerCacheImportJSON(models.ProviderCacheLidarrArtists, lidarrArtistsCachePath, lidarrArtistsCacheDuration,
		providerCacheDecodeMap[models.CachedLidarrArtistRelease])

	lidarrArtistsCacheMu.Lock()
	defer lidarrArtistsCacheMu.Unlock()
	return providerCacheRestore(models.ProviderCacheLidarrArtists, lidarrArtistsCache)
}

func LidarrLoadAlbumsCache() error {
	providerCacheImportJSON(models.ProviderCacheLidarrAlbums, lidarrAlbumsCachePath, lidarrAlbumsCacheDuration,
		providerCacheDecodeMap[models.CachedLidarrAlbumRelease])

	lidarrAlbumsCacheMu.Lock()
	defer lidarrAlbumsCacheMu.Unlock()
	return providerCacheRestore(models.ProviderCacheLidarrAlbums, lidarrAlbumsCache)
}

func LidarrLoadTracksCache() error {
	providerCacheImportJSON(models.ProviderCacheLidarrTracks, lidarrTracksCachePath, lidarrTracksCacheDuration,
		providerCacheDecodeMap[models.CachedLidarrTracksRelease])

	lidarrTracksCacheMu.Lock()
	defer lidarrTracksCacheMu.Unlock()
	return providerCacheRestore(models.ProviderCacheLidarrTracks, lidarrTracksCache)
}

func LidarrLoadTrackFilesCache() error {
	providerCacheImportJSON(models.ProviderCacheLidarrTrackFiles, lidarrTrackFilesCachePath, lidarrTrackFilesCacheDuration,
		providerCacheDecodeMap[models.CachedLidarrTrackFilesRelease])

	lidarrTrackFilesCacheMu.Lock()
	defer lidarrTrackFilesCacheMu.Unlock()
	return providerCacheRestore(models.ProviderCacheLidarrTrackFiles, lidarrTrackFilesCache)
}

// HealthCheck verifies Lidarr is reachable AND that our credentials actually get
// through to it. It deliberately probes an *authenticated* endpoint via the same
// getJSON path the scanner uses (identical headers, cookie and redirect handling),
// so the check exercises the exact gate real lookups pass through.
//
// It does NOT use /api/v1/system/status: that endpoint is commonly whitelisted at a
// reverse proxy / Authelia so monitors can reach it without a session, which means it
// can return 200 while every authenticated endpoint the scanner needs (e.g.
// /api/v1/artist) is rejected — a green health check masking a fully broken scan.
// /api/v1/rootfolder is cheap and requires auth, so a 401/redirect surfaces here.
func (c *LidarrClient) HealthCheck() (health bool, err error) {
	var dst json.RawMessage
	if err := c.getJSON("/api/v1/rootfolder", &dst); err != nil {
		logger.Log.Error("failed to ping Lidarr. error: " + err.Error())
		return false, err
	}

	logger.Log.Debug("managed to ping Lidarr")
	return true, nil
}

// try to retrieve the MB release from Lidarr
func ResolveMetadataDetailsFromLidarr(cli *LidarrClient, trackPath string, rootDir string) (*models.LidarrTrackMetadataDetails, error) {
	// derive the artist from the path folder
	artistName, err := utilities.ExtractArtistNameFromTrackFilePath(rootDir, trackPath)
	if err != nil {
		return nil, fmt.Errorf("could not read an artist folder out of the file path (expected <root>/<artist>/<album>/[<media>/]<track> under root %q): %w", rootDir, err)
	}
	logger.Log.Debugf("artist name found: %s", artistName)

	artists, err := cli.FindArtistByName(artistName)
	if err != nil {
		return nil, fmt.Errorf("artist lookup for %q failed: %w", artistName, err)
	} else if len(artists) > 1 {
		logger.Log.Warnf("%d artists found by that name, checking all and returning first match", len(artists))
	}

	for _, artist := range artists {
		logger.Log.Debugf("checking for artist: %s (%d)", artist.Name, artist.ID)
		lidarrTrackMetadataDetails := models.LidarrTrackMetadataDetails{}

		tf, err := cli.FindTrackFileByPath(artist.ID, trackPath, rootDir)
		if err != nil {
			return nil, fmt.Errorf("track file lookup for artist %q (Lidarr ID %d) failed: %w", artist.Name, artist.ID, err)
		} else if tf == nil {
			logger.Log.Warn("tracks not found in Lidarr by file path")
			continue
		}

		logger.Log.Tracef("Lidarr track file found: %d", tf.ID)

		tracks, err := cli.GetTracksByAlbumAndArtistID(artist.ID, tf.AlbumID)
		if err != nil {
			return &lidarrTrackMetadataDetails, fmt.Errorf("track list lookup for album %d (artist %q, Lidarr ID %d) failed: %w", tf.AlbumID, artist.Name, artist.ID, err)
		} else if tracks == nil {
			logger.Log.Warn("tracks not found in Lidarr by album and artist")
			continue
		} else if len(tracks) < 1 {
			logger.Log.Warn("tracks list found in Lidarr by album and artist is empty")
		}

		found := false
		for _, track := range tracks {
			// Deref only after the nil check: Lidarr omits trackFileId on a track it has
			// no file for, and logging args are evaluated whether or not trace is on.
			if track.TrackFileID == nil {
				continue
			}
			logger.Log.Tracef("comparing track ID %d, file ID %d", track.ID, *track.TrackFileID)
			if *track.TrackFileID == tf.ID {
				logger.Log.Tracef("Lidarr track found: %d", track.ID)
				lidarrTrackMetadataDetails.MBTrackID = track.ForeignTrackID
				lidarrTrackMetadataDetails.MBRecordingID = track.ForeignRecordingID
				lidarrTrackMetadataDetails.TrackTitle = track.Title
				found = true
				break
			}
		}

		if !found {
			logger.Log.Warn("track not found in tracks returned by Lidarr")
			continue
		}

		mbReleaseID, err := cli.GetMonitoredAlbumMBID(artist.ID, tf.AlbumID)
		if err != nil {
			return &lidarrTrackMetadataDetails, fmt.Errorf("monitored release lookup for album %d (artist %q, Lidarr ID %d) failed: %w", tf.AlbumID, artist.Name, artist.ID, err)
		} else if mbReleaseID == nil {
			logger.Log.Warn("MusicBrainz Release ID not found in Lidarr by album ID")
			continue
		}

		lidarrTrackMetadataDetails.MBReleaseID = *mbReleaseID

		logger.Log.Debug("found Lidarr details")
		return &lidarrTrackMetadataDetails, nil
	}

	logger.Log.Warn("no artists in Lidarr had the matching tracks for file")
	return nil, nil
}

package modules

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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

func init() {
	registerCache(cacheNameLidarrArtists, LidarrSaveArtistsCache)
	registerCache(cacheNameLidarrAlbums, LidarrSaveAlbumsCache)
	registerCache(cacheNameLidarrTracks, LidarrSaveTracksCache)
	registerCache(cacheNameLidarrTrackFiles, LidarrSaveTrackFilesCache)
}

// LidarrInvalidateCaches drops every in-memory Lidarr cache and flags them dirty so
// the emptied state is persisted. The next lookup re-fetches from Lidarr, which is
// how a release selection changed in Lidarr reaches GetMonitoredAlbumMBID before the
// 1h TTL would otherwise expire. It is a whole-cache flush rather than artist-scoped:
// the album/track/trackfile caches are keyed by Lidarr's own IDs, not by artist, so a
// scoped drop would have to walk their values to map back — not worth it for an action
// the user triggers by hand, and re-fetching from Lidarr is not rate-limited the way
// MusicBrainz is.
func LidarrInvalidateCaches() {
	lidarrArtistsCacheMu.Lock()
	lidarrArtistsCache = map[string]models.CachedLidarrArtistRelease{}
	lidarrArtistsCacheMu.Unlock()
	markCacheDirty(cacheNameLidarrArtists)

	lidarrAlbumsCacheMu.Lock()
	lidarrAlbumsCache = map[string]models.CachedLidarrAlbumRelease{}
	lidarrAlbumsCacheMu.Unlock()
	markCacheDirty(cacheNameLidarrAlbums)

	lidarrTracksCacheMu.Lock()
	lidarrTracksCache = map[string]models.CachedLidarrTracksRelease{}
	lidarrTracksCacheMu.Unlock()
	markCacheDirty(cacheNameLidarrTracks)

	lidarrTrackFilesCacheMu.Lock()
	lidarrTrackFilesCache = map[string]models.CachedLidarrTrackFilesRelease{}
	lidarrTrackFilesCacheMu.Unlock()
	markCacheDirty(cacheNameLidarrTrackFiles)

	logger.Log.Debug("invalidated Lidarr caches (artists, albums, tracks, trackfiles)")
}

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

// retrieves the Lidarr API path JSON
func (c *LidarrClient) getJSON(pathWithQuery string, dst any) error {
	req, err := http.NewRequest("GET", c.BaseURL+pathWithQuery, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.APIKey)
	req.Header.Set("Accept", "application/json")

	if c.Cookie != nil {
		req.Header.Set("Cookie", *c.Cookie)
	}

	do := func() error {
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			b, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("lidarr %s -> %d: %s", pathWithQuery, resp.StatusCode, strings.TrimSpace(string(b)))
		}
		return json.NewDecoder(resp.Body).Decode(dst)
	}

	if c.RateLimit != nil {
		return c.RateLimit(do)
	}
	return do()
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
	lidarrArtistsCacheMu.Lock()
	for i := range artists {
		// add artist to cache
		lidarrArtistsCache[strconv.FormatInt(artists[i].ID, 10)] = models.CachedLidarrArtistRelease{
			Artist:    artists[i],
			Timestamp: time.Now(),
		}

		// Extract last folder from Lidarr's stored path
		lidarrArtistFolder := filepath.Base(utilities.NormPath(artists[i].Path))
		logger.Log.Debugf("comparing artist folder: %s, original path: %s", lidarrArtistFolder, artists[i].Path)

		if strings.EqualFold(lidarrArtistFolder, want) {
			validArtists = append(validArtists, artists[i])
		}
	}
	lidarrArtistsCacheMu.Unlock()

	// persistence is batched; the whole artist list was just (re)populated
	markCacheDirty(cacheNameLidarrArtists)

	if len(validArtists) == 1 {
		return validArtists, nil
	} else if len(validArtists) > 1 {
		logger.Log.Warnf("multiple artists found in Lidarr by that name %s", artistName)
		return validArtists, nil
	}

	return nil, fmt.Errorf("artist %q not found in Lidarr", artistName)
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

	lidarrTrackFilesCacheMu.Lock()
	lidarrTrackFilesCache[key] = models.CachedLidarrTrackFilesRelease{
		TrackFiles: files,
		Timestamp:  time.Now(),
	}
	lidarrTrackFilesCacheMu.Unlock()
	markCacheDirty(cacheNameLidarrTrackFiles)

	return files, nil
}

// retrieves the Lidarr track object from a Lidarr artist ID and track file path
// retrieves the Lidarr track object from a Lidarr artist ID and track file path
// Matches on (album folder, file basename) only — ignores the rest of the path.
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

	// get track file name from path
	targetFile, err := utilities.ExtractTrackFileName(fullTrackPath)
	if err != nil {
		return nil, err
	}

	// clean strings
	tAlbum := utilities.Canon(targetAlbum)
	tFile := utilities.Canon(targetFile)

	logger.Log.Trace("target album: " + tAlbum + " | target file: " + tFile)

	var match *models.LidarrTrackFile
	for i := range files {
		// get album and track name and clean them
		// sometimes there are media folder, so unsure what is album name
		logger.Log.Trace(files[i].Path)
		fAlbumOrMedia := utilities.Canon(utilities.BaseDirOfPathAny(files[i].Path))
		fAlbumOrArtist := utilities.Canon(utilities.GrandfatherDirOfPathAny(files[i].Path))
		fFile := utilities.Canon(utilities.BaseOfPathAny(files[i].Path))

		// log comparing
		logger.Log.Trace("compare album=" + fAlbumOrArtist + "/" + fAlbumOrMedia + " file=" + fFile + " against target")

		// find match
		if (fAlbumOrMedia == tAlbum || fAlbumOrArtist == tAlbum) && fFile == tFile {
			match = &files[i]
			break
		}
	}

	// return error if no match
	if match == nil {
		logger.Log.Warnf("trackfile not found by album+file; album=%q file=%q", targetAlbum, targetFile)
	}

	return match, nil
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
	for _, a := range albums {
		lidarrAlbumsCache[strconv.FormatInt(a.ID, 10)] = models.CachedLidarrAlbumRelease{
			Album:     a,
			Timestamp: now,
		}
	}
	lidarrAlbumsCacheMu.Unlock()
	markCacheDirty(cacheNameLidarrAlbums)

	for _, a := range albums {
		if a.ID != albumID {
			continue
		}
		for _, r := range a.Releases {
			if r.Monitored && r.ForeignReleaseID != "" {
				return &r.ForeignReleaseID, nil
			}
		}
	}

	logger.Log.Errorf("no monitored release with MB ID for album %d", albumID)
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
	lidarrTracksCacheMu.Lock()
	lidarrTracksCache[albumKey] = models.CachedLidarrTracksRelease{
		Tracks:    t,
		Timestamp: time.Now(),
	}
	lidarrTracksCacheMu.Unlock()
	markCacheDirty(cacheNameLidarrTracks)

	return t, nil
}

func LidarrLoadArtistsCache() error {
	data, err := os.ReadFile(lidarrArtistsCachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No cache yet
		}
		return err
	}

	lidarrArtistsCacheMu.Lock()
	defer lidarrArtistsCacheMu.Unlock()
	return json.Unmarshal(data, &lidarrArtistsCache)
}

func LidarrSaveArtistsCache() error {
	lidarrArtistsCacheMu.RLock()
	data, err := json.MarshalIndent(lidarrArtistsCache, "", "  ")
	lidarrArtistsCacheMu.RUnlock()
	if err != nil {
		return err
	}

	return os.WriteFile(lidarrArtistsCachePath, data, 0644)
}

func LidarrLoadAlbumsCache() error {
	data, err := os.ReadFile(lidarrAlbumsCachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No cache yet
		}
		return err
	}

	lidarrAlbumsCacheMu.Lock()
	defer lidarrAlbumsCacheMu.Unlock()
	return json.Unmarshal(data, &lidarrAlbumsCache)
}

func LidarrSaveAlbumsCache() error {
	lidarrAlbumsCacheMu.RLock()
	data, err := json.MarshalIndent(lidarrAlbumsCache, "", "  ")
	lidarrAlbumsCacheMu.RUnlock()
	if err != nil {
		return err
	}

	return os.WriteFile(lidarrAlbumsCachePath, data, 0644)
}

func LidarrLoadTracksCache() error {
	data, err := os.ReadFile(lidarrTracksCachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No cache yet
		}
		return err
	}

	lidarrTracksCacheMu.Lock()
	defer lidarrTracksCacheMu.Unlock()
	return json.Unmarshal(data, &lidarrTracksCache)
}

func LidarrSaveTracksCache() error {
	lidarrTracksCacheMu.RLock()
	data, err := json.MarshalIndent(lidarrTracksCache, "", "  ")
	lidarrTracksCacheMu.RUnlock()
	if err != nil {
		return err
	}

	return os.WriteFile(lidarrTracksCachePath, data, 0644)
}

func LidarrLoadTrackFilesCache() error {
	data, err := os.ReadFile(lidarrTrackFilesCachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No cache yet
		}
		return err
	}

	lidarrTrackFilesCacheMu.Lock()
	defer lidarrTrackFilesCacheMu.Unlock()
	return json.Unmarshal(data, &lidarrTrackFilesCache)
}

func LidarrSaveTrackFilesCache() error {
	lidarrTrackFilesCacheMu.RLock()
	data, err := json.MarshalIndent(lidarrTrackFilesCache, "", "  ")
	lidarrTrackFilesCacheMu.RUnlock()
	if err != nil {
		return err
	}

	return os.WriteFile(lidarrTrackFilesCachePath, data, 0644)
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
		return nil, err
	}
	logger.Log.Debugf("artist name found: %s", artistName)

	artists, err := cli.FindArtistByName(artistName)
	if err != nil {
		return nil, err
	} else if len(artists) > 1 {
		logger.Log.Warnf("%d artists found by that name, checking all and returning first match", len(artists))
	}

	for _, artist := range artists {
		logger.Log.Debugf("checking for artist: %s (%d)", artist.Name, artist.ID)
		lidarrTrackMetadataDetails := models.LidarrTrackMetadataDetails{}

		tf, err := cli.FindTrackFileByPath(artist.ID, trackPath, rootDir)
		if err != nil {
			return nil, err
		} else if tf == nil {
			logger.Log.Warn("tracks not found in Lidarr by file path")
			continue
		}

		logger.Log.Tracef("Lidarr track file found: %d", tf.ID)

		tracks, err := cli.GetTracksByAlbumAndArtistID(artist.ID, tf.AlbumID)
		if err != nil {
			return &lidarrTrackMetadataDetails, err
		} else if tracks == nil {
			logger.Log.Warn("tracks not found in Lidarr by album and artist")
			continue
		} else if len(tracks) < 1 {
			logger.Log.Warn("tracks list found in Lidarr by album and artist is empty")
		}

		found := false
		for _, track := range tracks {
			logger.Log.Tracef("comparing track ID %d, file ID %d", track.ID, *track.TrackFileID)
			if track.TrackFileID != nil && *track.TrackFileID == tf.ID {
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
			return &lidarrTrackMetadataDetails, err
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

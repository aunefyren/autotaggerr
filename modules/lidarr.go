package modules

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/utilities"
)

var (
	lidarrArtistsCachePath     = "config/lidarr_artists.json"
	lidarrArtistsCacheDuration = time.Hour // 1 hour
	lidarrArtistsCache         = map[string]models.CachedLidarrArtistRelease{}
	lidarrAlbumsCachePath      = "config/lidarr_albums.json"
	lidarrAlbumsCacheDuration  = time.Hour // 1 hour
	lidarrAlbumsCache          = map[string]models.CachedLidarrAlbumRelease{}
	lidarrTracksCachePath      = "config/lidarr_tracks.json"
	lidarrTracksCacheDuration  = time.Hour // 1 hour
	lidarrTracksCache          = map[string]models.CachedLidarrTracksRelease{}
)

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
func (c *LidarrClient) FindArtistByName(artistName string) (*models.LidarrArtist, error) {
	err := LidarrLoadArtistsCache()
	if err != nil {
		return nil, err
	}

	if cached, ok := lidarrArtistsCache[artistName]; ok {
		logger.Log.Trace("cached entry found")
		if time.Since(cached.Timestamp) < lidarrArtistsCacheDuration {
			logger.Log.Debug("returning cached release for artist: " + artistName)
			return &cached.Artist, nil
		}
	}

	logger.Log.Debug("cached artist not found for: " + artistName)

	// get lidarr API response
	var artists []models.LidarrArtist
	if err := c.getJSON("/api/v1/artist", &artists); err != nil {
		return nil, err
	}

	want := strings.ToLower(strings.TrimSpace(artistName))

	for i := range artists {
		// add artist to cache
		lidarrArtistsCache[artists[i].Name] = models.CachedLidarrArtistRelease{
			Artist:    artists[i],
			Timestamp: time.Now(),
		}

		// save new cache
		err = LidarrSaveArtistsCache()
		if err != nil {
			return nil, err
		}

		// Extract last folder from Lidarr's stored path
		lidarrArtistFolder := filepath.Base(utilities.NormPath(artists[i].Path))
		if strings.ToLower(lidarrArtistFolder) == want {
			return &artists[i], nil
		}
	}

	return nil, fmt.Errorf("artist %q not found in Lidarr", artistName)
}

// retrieves the Lidarr track object from a Lidarr artist ID and track file path
// retrieves the Lidarr track object from a Lidarr artist ID and track file path
// Matches on (album folder, file basename) only — ignores the rest of the path.
func (c *LidarrClient) FindTrackFileByPath(artistID int64, fullTrackPath string, rootDir string) (*models.LidarrTrackFile, error) {
	var files []models.LidarrTrackFile
	if err := c.getJSON(fmt.Sprintf("/api/v1/trackfile?artistId=%d", artistID), &files); err != nil {
		return nil, err
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
		fAlbum := utilities.Canon(utilities.BaseDirOfPathAny(files[i].Path))
		fFile := utilities.Canon(utilities.BaseOfPathAny(files[i].Path))

		// log comparing
		logger.Log.Trace("compare album=" + fAlbum + " file=" + fFile + " against target")

		// find match
		if fAlbum == tAlbum && fFile == tFile {
			match = &files[i]
			break
		}
	}

	// return error if no match
	if match == nil {
		return nil, fmt.Errorf("trackfile not found by album+file; album=%q file=%q", targetAlbum, targetFile)
	}

	return match, nil
}

// retrieves the Lidarr album object from a Lidarr artist ID and album ID
func (c *LidarrClient) GetMonitoredAlbumMBID(artistID, albumID int64) (string, error) {
	err := LidarrLoadAlbumsCache()
	if err != nil {
		return "", err
	}

	if cached, ok := lidarrAlbumsCache[strconv.FormatInt(albumID, 10)]; ok {
		logger.Log.Trace("cached entry found for album")
		if time.Since(cached.Timestamp) < lidarrAlbumsCacheDuration {
			for _, r := range cached.Album.Releases {
				if r.Monitored && r.ForeignReleaseID != "" {
					logger.Log.Debug("returning cached album release: " + strconv.FormatInt(albumID, 10))
					return r.ForeignReleaseID, nil
				}
			}
		}
		logger.Log.Trace("cached entry not found for album release")
	}

	var albums []models.LidarrAlbum
	q := fmt.Sprintf("/api/v1/album?artistId=%d&albumIds=%d&includeAllArtistAlbums=true", artistID, albumID)
	if err := c.getJSON(q, &albums); err != nil {
		return "", err
	}

	for _, a := range albums {
		// add artist to cache
		lidarrAlbumsCache[strconv.FormatInt(albumID, 10)] = models.CachedLidarrAlbumRelease{
			Album:     a,
			Timestamp: time.Now(),
		}

		// save new cache
		err = LidarrSaveAlbumsCache()
		if err != nil {
			return "", err
		}

		if a.ID != albumID {
			continue
		}
		for _, r := range a.Releases {
			if r.Monitored && r.ForeignReleaseID != "" {
				return r.ForeignReleaseID, nil
			}
		}
	}

	return "", fmt.Errorf("no monitored release with MB ID for album %d", albumID)
}

func (c *LidarrClient) GetTracksByAlbumAndArtistID(artistID int64, albumID int64) ([]models.LidarrTrack, error) {
	err := LidarrLoadTracksCache()
	if err != nil {
		return nil, err
	}

	if cached, ok := lidarrTracksCache[strconv.FormatInt(albumID, 10)]; ok {
		logger.Log.Trace("cached entry found")
		if time.Since(cached.Timestamp) < lidarrTracksCacheDuration {
			logger.Log.Debug("returning cached tracks for album: " + strconv.FormatInt(albumID, 10))
			return cached.Tracks, nil
		}
	}

	logger.Log.Debug("cached tracks not found for album ID: " + strconv.FormatInt(albumID, 10))

	var t []models.LidarrTrack
	if err := c.getJSON(fmt.Sprintf("/api/v1/track?artistId=%d&albumId=%d", artistID, albumID), &t); err != nil {
		return nil, err
	}

	// add tracks to cache
	lidarrTracksCache[strconv.FormatInt(albumID, 10)] = models.CachedLidarrTracksRelease{
		Tracks:    t,
		Timestamp: time.Now(),
	}

	// save new cache
	err = LidarrSaveTracksCache()
	if err != nil {
		return nil, err
	}

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

	return json.Unmarshal(data, &lidarrArtistsCache)
}

func LidarrSaveArtistsCache() error {
	data, err := json.MarshalIndent(lidarrArtistsCache, "", "  ")
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

	return json.Unmarshal(data, &lidarrAlbumsCache)
}

func LidarrSaveAlbumsCache() error {
	data, err := json.MarshalIndent(lidarrAlbumsCache, "", "  ")
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

	return json.Unmarshal(data, &lidarrTracksCache)
}

func LidarrSaveTracksCache() error {
	data, err := json.MarshalIndent(lidarrTracksCache, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(lidarrTracksCachePath, data, 0644)
}

// HealthCheck hits a cheap endpoint and checks auth.
// Uses /api/v1/system/status (we only care that it decodes / returns 200).
func (c *LidarrClient) HealthCheck() (health bool, err error) {
	health = false

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// decode into a loose map to avoid tight coupling to fields
	var status map[string]any

	// wrap existing getJSON with context timeout if you like
	reqPath := "/api/v1/system/status"
	// quick context-aware version of getJSON:
	req, err := http.NewRequestWithContext(ctx, "GET", c.BaseURL+reqPath, nil)
	if err != nil {
		return health, err
	}
	req.Header.Set("X-Api-Key", c.APIKey)
	req.Header.Set("Accept", "application/json")

	if c.Cookie != nil {
		req.Header.Set("Cookie", *c.Cookie)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return health, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		logger.Log.Error("failed to ping Lidarr. response: " + string(b))
		return health, err
	}
	// optional: verify JSON parses
	_ = json.NewDecoder(resp.Body).Decode(&status)
	logger.Log.Debug("managed to ping Lidarr")

	return true, err
}

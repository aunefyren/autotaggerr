package modules

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/utilities"
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
	var artists []models.LidarrArtist
	if err := c.getJSON("/api/v1/artist", &artists); err != nil {
		return nil, err
	}

	want := strings.ToLower(strings.TrimSpace(artistName))

	for i := range artists {
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
func (c *LidarrClient) FindTrackFileByPath(artistID int64, fullTrackPath string) (*models.LidarrTrackFile, error) {
	var files []models.LidarrTrackFile
	if err := c.getJSON(fmt.Sprintf("/api/v1/trackfile?artistId=%d", artistID), &files); err != nil {
		return nil, err
	}

	// get album name from file path
	targetAlbum, err := utilities.ExtractAlbumNameFromTrackFilePath(fullTrackPath)
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
	var albums []models.LidarrAlbum
	q := fmt.Sprintf("/api/v1/album?artistId=%d&albumIds=%d&includeAllArtistAlbums=true", artistID, albumID)
	if err := c.getJSON(q, &albums); err != nil {
		return "", err
	}

	for _, a := range albums {
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
	var t []models.LidarrTrack
	if err := c.getJSON(fmt.Sprintf("/api/v1/track?artistId=%d&albumId=%d", artistID, albumID), &t); err != nil {
		return nil, err
	}
	return t, nil
}

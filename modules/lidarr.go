package modules

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/utilities"
)

type LidarrClient struct {
	BaseURL   string
	APIKey    string
	HTTP      *http.Client
	RateLimit func(func() error) error // optional: your 1 rps limiter
}

func NewLidarrClient(baseURL, apiKey string) *LidarrClient {
	return &LidarrClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *LidarrClient) getJSON(pathWithQuery string, dst any) error {
	req, err := http.NewRequest("GET", c.BaseURL+pathWithQuery, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.APIKey)

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

// 1) Find artist by matching the artist folder against artist.Path
func (c *LidarrClient) FindArtistByPath(artistFolder string) (*models.LidarrArtist, error) {
	var artists []models.LidarrArtist
	if err := c.getJSON("/api/v1/artist", &artists); err != nil {
		return nil, err
	}
	artistFolderN := utilities.NormPath(artistFolder)
	for i := range artists {
		if utilities.NormPath(artists[i].Path) == artistFolderN {
			return &artists[i], nil
		}
	}
	return nil, fmt.Errorf("artist not found for path %q", artistFolder)
}

// 2) Find our trackfile under the artist by full path
func (c *LidarrClient) FindTrackFileByPath(artistID int64, fullTrackPath string) (*models.LidarrTrackFile, error) {
	var files []models.LidarrTrackFile
	if err := c.getJSON(fmt.Sprintf("/api/v1/trackfile?artistId=%d", artistID), &files); err != nil {
		return nil, err
	}
	target := utilities.NormPath(fullTrackPath)
	for i := range files {
		if utilities.NormPath(files[i].Path) == target {
			return &files[i], nil
		}
	}
	return nil, fmt.Errorf("trackfile not found for %q", fullTrackPath)
}

// 3) Get album and pick monitored release → MBID
func (c *LidarrClient) GetMonitoredAlbumMBID(artistID, albumID int64) (string, error) {
	var albums []models.LidarrAlbum
	q := fmt.Sprintf("/api/v1/album?artistId=%d&albumIds=%d&includeAllArtistAlbums=true", artistID, albumID)
	if err := c.getJSON(q, &albums); err != nil {
		return "", err
	}
	for _, a := range albums {
		if a.Id != albumID {
			continue
		}
		for _, r := range a.Releases {
			if r.Monitored && r.ForeignReleaseId != "" {
				return r.ForeignReleaseId, nil
			}
		}
	}
	return "", fmt.Errorf("no monitored release with MBID for album %d", albumID)
}

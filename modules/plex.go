package modules

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// must be local in the file
type PlexClient struct {
	BaseURL   string
	Token     string
	HTTP      *http.Client
	RateLimit func(func() error) error // optional: your 1 rps limiter
}

// create new Plex client
func NewPlexClient(baseURL, token string) *PlexClient {
	return &PlexClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *PlexClient) findMusicSectionForPath(plexBaseURL, token, albumDir string) (string, error) {
	req, _ := http.NewRequest("GET", plexBaseURL+"/library/sections?X-Plex-Token="+token, nil)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	var sections models.PlexSections
	if err := json.NewDecoder(res.Body).Decode(&sections); err != nil {
		return "", fmt.Errorf("decode sections: %w", err)
	}
	albumDirN := strings.ToLower(filepath.ToSlash(filepath.Clean(albumDir)))

	// choose the *music* section (type == "artist") whose Location is a prefix of albumDir
	for _, dir := range sections.MediaContainer.Directory {
		if strings.ToLower(dir.Type) != "artist" { // music library
			continue
		}
		for _, loc := range dir.Location {
			prefix := strings.ToLower(filepath.ToSlash(filepath.Clean(loc.Path)))
			if strings.HasPrefix(albumDirN, prefix) {
				return dir.Key, nil // sectionId
			}
		}
	}

	return "", fmt.Errorf("no Plex music section contains %q", albumDir)
}

func (c *PlexClient) RefreshAlbumByPath(plexBaseURL, token, albumDir string) error {
	sectionId, err := c.findMusicSectionForPath(plexBaseURL, token, albumDir)
	if err != nil {
		return err
	}

	q := url.Values{}
	q.Set("path", albumDir) // absolute path, as Plex sees it
	q.Set("X-Plex-Token", token)

	req, _ := http.NewRequest("GET",
		fmt.Sprintf("%s/library/sections/%s/refresh?%s", plexBaseURL, sectionId, q.Encode()),
		nil,
	)

	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode/100 != 2 {
		b, _ := io.ReadAll(res.Body)
		return fmt.Errorf("plex refresh failed: %s - %s", res.Status, strings.TrimSpace(string(b)))
	}

	return nil
}

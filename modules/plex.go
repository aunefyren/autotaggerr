package modules

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/utilities"
)

var (
	plexAlbumKeyCachePath     = "config/plex_album_keys.json"
	plexAlbumKeyCacheDuration = time.Hour // 1 hour
	plexAlbumKeyCache         = map[string]models.PlexAlbumKeyCache{}
)

type PlexClient struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func NewPlexClient(baseURL, token string) *PlexClient {
	return &PlexClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 10 * time.Second},
	}
}

func (p *PlexClient) get(path string, dst any) error {
	u := p.BaseURL + path
	if strings.Contains(path, "?") {
		u += "&"
	} else {
		u += "?"
	}
	u += "X-Plex-Token=" + url.QueryEscape(p.Token)

	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("Accept", "application/xml")
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("plex %s -> %d: %s", path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return xml.NewDecoder(resp.Body).Decode(dst)
}

func (p *PlexClient) put(path string) error {
	u := p.BaseURL + path
	if strings.Contains(path, "?") {
		u += "&"
	} else {
		u += "?"
	}
	u += "X-Plex-Token=" + url.QueryEscape(p.Token)

	req, _ := http.NewRequest("PUT", u, nil)
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("plex refresh -> %d", resp.StatusCode)
	}
	return nil
}

// find first music section (type="artist")
func (p *PlexClient) FindMusicSectionID() (string, error) {
	var mc models.PlexMediaContainer
	if err := p.get("/library/sections", &mc); err != nil {
		return "", err
	}
	for _, d := range mc.Directory {
		if strings.EqualFold(d.Type, "artist") {
			return d.Key, nil
		}
	}
	return "", errors.New("no music section found (type=artist)")
}

func (p *PlexClient) FindArtistKey(sectionID, artistName string) (string, error) {
	var mc models.PlexMediaContainer
	path := fmt.Sprintf("/library/sections/%s/all?type=8&title=%s",
		sectionID, url.QueryEscape(artistName))
	if err := p.get(path, &mc); err != nil {
		return "", err
	}

	want := utilities.Canon(artistName)
	for _, d := range mc.Directory {
		if d.Type == "artist" && utilities.Canon(d.Title) == want {
			return d.Key, nil
		}
	}
	return "", fmt.Errorf("artist not found: %s", artistName)
}

// normalizeArtistKey ensures we end up with "/library/metadata/<id>/children"
func normalizeArtistChildrenPath(artistKey string) (string, error) {
	artistKey = strings.TrimSpace(artistKey)
	if artistKey == "" {
		return "", fmt.Errorf("empty artist key")
	}
	if strings.HasPrefix(artistKey, "/library/metadata/") {
		return strings.TrimSuffix(artistKey, "/children") + "/children", nil
	}
	if _, err := strconv.Atoi(artistKey); err == nil {
		return "/library/metadata/" + artistKey + "/children", nil
	}
	return "", fmt.Errorf("unrecognized artistKey format: %q", artistKey)
}

// "/library/metadata/196905" or "/library/metadata/196905/children" -> "/library/metadata/196905"
func normalizeAlbumKey(key string) string {
	key = strings.TrimSpace(key)
	key = strings.TrimSuffix(key, "/children")
	return key
}

// Search a section for album (type=9) first; if nothing, search track (type=10).
// Returns the album ratingKey path ("/library/metadata/<id>") suitable for refresh.
func (p *PlexClient) ResolveAlbumKeyInSection(sectionID, artistName, albumTitle string, trackTitle string) (string, error) {
	// 1) Album search (type=9)
	{
		q := fmt.Sprintf(
			"/library/sections/%s/all?type=9&title=%s&artist.title=%s",
			url.PathEscape(sectionID),
			url.QueryEscape(albumTitle),
			url.QueryEscape(artistName),
		)

		var mc models.PlexMediaContainer
		if err := p.get(q, &mc); err != nil {
			return "", err
		}
		logger.Log.Trace(mc)

		wantAlbum := utilities.Canon(albumTitle)
		wantArtist := utilities.Canon(artistName)

		for _, d := range mc.Directory {
			// Albums come back here
			if d.Type != "album" {
				continue
			}
			if utilities.Canon(d.Title) == wantAlbum &&
				utilities.Canon(d.ParentTitle) == wantArtist {

				return normalizeAlbumKey(d.Key), nil
			}
		}
	}

	// 2) Track search fallback (type=10) – useful for odd cases / singles
	{
		q := fmt.Sprintf(
			"/library/sections/%s/all?type=10&artist.title=%s&album.title=%s",
			url.PathEscape(sectionID),
			url.QueryEscape(artistName),
			url.QueryEscape(albumTitle),
		)
		if trackTitle != "" {
			q += "&title=" + url.QueryEscape(trackTitle)
		}

		var mc models.PlexMediaContainer
		if err := p.get(q, &mc); err != nil {
			return "", err
		}
		logger.Log.Trace(mc)

		wantAlbum := utilities.Canon(albumTitle)
		wantArtist := utilities.Canon(artistName)
		wantTrack := utilities.Canon(trackTitle)

		for _, t := range mc.Track {
			logger.Log.Trace(t)
			if utilities.Canon(t.GrandparentTitle) == wantArtist &&
				utilities.Canon(t.ParentTitle) == wantAlbum &&
				(trackTitle == "" || utilities.Canon(t.Title) == wantTrack) {

				// Prefer ParentKey; fallback to ParentRatingKey if needed
				if t.ParentKey != "" {
					return normalizeAlbumKey(t.ParentKey), nil
				}
				if t.ParentRatingKey != "" {
					return "/library/metadata/" + t.ParentRatingKey, nil
				}
			}
		}
	}

	return "", fmt.Errorf("album/single not found in section: artist=%q album=%q track=%q section=%s",
		artistName, albumTitle, trackTitle, sectionID)
}

// RefreshAlbum triggers a metadata refresh on an album (ratingKey path like "/library/metadata/196905").
func (p *PlexClient) RefreshAlbum(albumKey string) error {
	key := normalizeAlbumKey(albumKey)
	refreshPath := path.Join(key, "refresh") // => "/library/metadata/196905/refresh"

	// First try GET (most reliable across installs)
	u := p.buildURL(refreshPath, map[string]string{"force": "1"})
	req, _ := http.NewRequest("GET", u, nil)

	resp, err := p.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent {
		return nil
	}

	// Optional fallback: try PUT if GET didn’t work
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		req2, _ := http.NewRequest("PUT", u, nil)
		resp2, err2 := p.HTTP.Do(req2)
		if err2 != nil {
			return err2
		}
		defer resp2.Body.Close()
		if resp2.StatusCode == http.StatusOK || resp2.StatusCode == http.StatusNoContent {
			return nil
		}
		body, _ := io.ReadAll(resp2.Body)
		return fmt.Errorf("plex refresh (PUT) failed: %d %s", resp2.StatusCode, strings.TrimSpace(string(body)))
	}

	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("plex refresh (GET) failed: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

// HealthCheck calls /identity (fast, auth-validating) and returns latency.
func (p *PlexClient) HealthCheck() (health bool, err error) {
	health = false
	path := "/identity"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Build URL with token
	u := p.BaseURL + path
	if strings.Contains(path, "?") {
		u += "&"
	} else {
		u += "?"
	}
	u += "X-Plex-Token=" + url.QueryEscape(p.Token)

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return health, err
	}
	req.Header.Set("Accept", "application/xml")

	resp, err := p.HTTP.Do(req)
	if err != nil {
		return health, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		logger.Log.Error("failed to ping Plex. response: " + string(b))
		return health, err
	}

	// Optional: parse identity to confirm structure
	var id models.PlexIdentity
	if err := xml.NewDecoder(resp.Body).Decode(&id); err != nil {
		// parsing failure isn't fatal if 200 OK, but you can treat it as warning
		logger.Log.Warn("managed to ping Plex, but can't parse response. error: " + err.Error())
		return true, nil
	}

	logger.Log.Debug("managed to ping Plex. " + id.FriendlyName + id.Version)
	return true, err
}

// Build a URL with query params safely (no manual "?" / "&" juggling)
func (p *PlexClient) buildURL(path string, q map[string]string) string {
	u, _ := url.Parse(p.BaseURL)
	u.Path = path

	query := u.Query()
	query.Set("X-Plex-Token", p.Token)
	for k, v := range q {
		query.Set(k, v)
	}
	u.RawQuery = query.Encode()
	return u.String()
}

func PlexLoadAlbumKeyCache() error {
	data, err := os.ReadFile(plexAlbumKeyCachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No cache yet
		}
		return err
	}

	return json.Unmarshal(data, &plexAlbumKeyCache)
}

func PlexSaveAlbumKeyCache() error {
	data, err := json.MarshalIndent(plexAlbumKeyCache, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(plexAlbumKeyCachePath, data, 0644)
}

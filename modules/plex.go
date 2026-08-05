package modules

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/utilities"
)

var (
	plexAlbumKeyCachePath     = "config/plex_album_keys.json"
	plexAlbumKeyCacheDuration = time.Hour // 1 hour
	plexAlbumKeyCache         = map[string]models.PlexAlbumKeyCache{}
	plexAlbumKeyCacheMu       sync.RWMutex
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

	// Exact (normalized) match first for precision.
	want := utilities.Canon(artistName)
	for _, d := range mc.Directory {
		if d.Type == "artist" && utilities.Canon(d.Title) == want {
			return d.Key, nil
		}
	}

	// Fall back to loose matching so typographic differences still match Plex's
	// stored title — e.g. Plex holds "Jay‑Z" with a non-breaking hyphen/en-dash
	// while our tag has an ASCII "Jay-Z", or curly vs straight apostrophes. This
	// mirrors the tolerance ResolveAlbumKeyInSection already uses.
	for _, d := range mc.Directory {
		if d.Type == "artist" && utilities.EqLoose(d.Title, artistName) {
			logger.Log.Debugf("Plex artist matched loosely: %q ~= %q", d.Title, artistName)
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
			"/library/sections/%s/all?type=9&artist.title=%s",
			url.PathEscape(sectionID),
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
			logger.Log.Trace("looping over title: " + d.Title)
			logger.Log.Trace("looping over parent title: " + d.Title)
			if utilities.EqLoose(utilities.Canon(d.Title), wantAlbum) &&
				utilities.EqLoose(utilities.Canon(d.ParentTitle), wantArtist) {

				return normalizeAlbumKey(d.Key), nil
			}
		}
	}

	// 2) Track search fallback (type=10) – useful for odd cases / singles
	{
		q := fmt.Sprintf(
			"/library/sections/%s/all?type=10&artist.title=%s",
			url.PathEscape(sectionID),
			url.QueryEscape(artistName),
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
			if utilities.EqLoose(utilities.Canon(t.GrandparentTitle), wantArtist) &&
				utilities.EqLoose(utilities.Canon(t.ParentTitle), wantAlbum) &&
				(trackTitle == "" || utilities.EqLoose(utilities.Canon(t.Title), wantTrack)) {

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

// PlexLoadAlbumKeyCache warms the album-key map from the database at startup, and
// reads the legacy JSON file exactly once so an upgrade keeps the keys it already
// resolved.
func PlexLoadAlbumKeyCache() error {
	providerCacheImportJSON(models.ProviderCachePlexAlbumKeys, plexAlbumKeyCachePath, plexAlbumKeyCacheDuration,
		providerCacheDecodeMap[models.PlexAlbumKeyCache])

	plexAlbumKeyCacheMu.Lock()
	defer plexAlbumKeyCacheMu.Unlock()
	return providerCacheRestore(models.ProviderCachePlexAlbumKeys, plexAlbumKeyCache)
}

func PlexRefreshForFile(unchanged bool, tagsWritten int, refreshSet *AlbumRefreshSet, plexClient PlexClient, albumTitle string, releaseArtist string, trackTitle string) error {
	albumKey := ""
	plexAlbumKeyCacheMu.RLock()
	cached, ok := plexAlbumKeyCache[albumTitle]
	plexAlbumKeyCacheMu.RUnlock()
	if ok {
		logger.Log.Trace("cached entry for Plex Album key found")
		if time.Since(cached.Timestamp) < plexAlbumKeyCacheDuration {
			logger.Log.Debug("returning cached album key for album: " + albumTitle)
			albumKey = cached.AlbumKey
		}
	} else {
		// Failures here are non-fatal: the file's tags were already written; we just
		// can't queue a Plex refresh for it. Return the wrapped cause without logging
		// — the caller logs it once at Warn level with the album context.
		sectionID, err := plexClient.FindMusicSectionID()
		if err != nil {
			return fmt.Errorf("find Plex music section: %w", err)
		}

		artistKey, err := plexClient.FindArtistKey(sectionID, releaseArtist)
		if err != nil {
			return fmt.Errorf("find Plex artist %q: %w", releaseArtist, err)
		}

		logger.Log.Trace(artistKey + " - " + albumTitle)

		resolvedKey, err := plexClient.ResolveAlbumKeyInSection(sectionID, releaseArtist, albumTitle, trackTitle)
		if err != nil {
			return fmt.Errorf("resolve Plex album key for %q: %w", albumTitle, err)
		}
		albumKey = resolvedKey
		logger.Log.Trace(albumKey)

		// add album key to cache
		entry := models.PlexAlbumKeyCache{
			AlbumKey:  albumKey,
			Timestamp: time.Now(),
		}
		plexAlbumKeyCacheMu.Lock()
		plexAlbumKeyCache[albumTitle] = entry
		plexAlbumKeyCacheMu.Unlock()
		providerCachePut(models.ProviderCachePlexAlbumKeys, albumTitle, entry, plexAlbumKeyCacheDuration)
	}

	if !unchanged && tagsWritten > 0 {
		refreshSet.Add(albumTitle, albumKey)
	}

	return nil
}

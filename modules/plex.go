package modules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/utilities"
)

var (
	plexAlbumKeyCachePath = "config/plex_album_keys.json"
	// plexAlbumKeyCacheDuration is how long a folder's album is believed. Long, because
	// the answer was checked against the file itself and is what an album page links
	// to; a refresh Plex refuses drops it early (InvalidatePlexAlbum).
	plexAlbumKeyCacheDuration = 30 * 24 * time.Hour
	// plexAlbumMissingDuration is how long "Plex does not hold this folder" is
	// believed. Short: Plex's own scan is what changes it, and nothing tells us when.
	plexAlbumMissingDuration = time.Hour
	plexAlbumKeyCache        = map[string]models.PlexAlbumKeyCache{}
	plexAlbumKeyCacheMu      sync.RWMutex
)

type PlexClient struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
	// SectionKey narrows album lookups to one library section. Empty searches every
	// music section — the file-path check is what picks the album, so searching wide
	// costs requests, never correctness. Set per library by ForLibrary.
	SectionKey string
	// identity is the server's machine identifier, fetched once and shared by every
	// copy ForLibrary makes; links into Plex's web app are addressed by it.
	identity *plexIdentity
}

type plexIdentity struct {
	mu        sync.Mutex
	machineID string
}

// ForLibrary is the client as one library uses it: narrowed to the library's Plex
// section, or nil when the library has Plex refresh turned off. Plex is one server
// for the process, but which of its sections holds a library — if any — is a fact
// about the library. A nil receiver stays nil, so callers need no extra check.
func (p *PlexClient) ForLibrary(library models.Library) *PlexClient {
	if p == nil || library.PlexSkip {
		return nil
	}
	scoped := *p
	scoped.SectionKey = strings.TrimSpace(library.PlexSectionKey)
	return &scoped
}

func NewPlexClient(baseURL, token string) *PlexClient {
	return &PlexClient{
		BaseURL:  strings.TrimRight(baseURL, "/"),
		Token:    token,
		HTTP:     &http.Client{Timeout: 10 * time.Second},
		identity: &plexIdentity{},
	}
}

// MachineIdentifier is the server's ID, from /identity, asked once per process. A
// failure is not remembered, so a Plex that was down answers next time.
func (p *PlexClient) MachineIdentifier() (string, error) {
	if p.identity == nil {
		p.identity = &plexIdentity{}
	}
	p.identity.mu.Lock()
	defer p.identity.mu.Unlock()
	if p.identity.machineID != "" {
		return p.identity.machineID, nil
	}
	var id models.PlexIdentity
	if err := p.get("/identity", &id); err != nil {
		return "", err
	}
	if id.MachineIdentifier == "" {
		return "", errors.New("plex /identity named no machine identifier")
	}
	p.identity.machineID = id.MachineIdentifier
	return id.MachineIdentifier, nil
}

// AlbumWebURL is a link to one album in Plex's web app, through app.plex.tv: it
// reaches the server from any browser signed in to the account, where the base URL
// Autotaggerr talks to is often an address only it can reach.
func (p *PlexClient) AlbumWebURL(albumKey string) (string, error) {
	machineID, err := p.MachineIdentifier()
	if err != nil {
		return "", err
	}
	return "https://app.plex.tv/desktop/#!/server/" + url.PathEscape(machineID) +
		"/details?key=" + url.QueryEscape(normalizeAlbumKey(albumKey)), nil
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

// MusicSections lists the server's music sections (type="artist") with the folders
// each one scans. It used to be "the first music section", which made every library
// refresh against whichever section Plex listed first.
func (p *PlexClient) MusicSections() ([]models.PlexSection, error) {
	var mc models.PlexMediaContainer
	if err := p.get("/library/sections", &mc); err != nil {
		return nil, err
	}
	var out []models.PlexSection
	for _, d := range mc.Directory {
		if !strings.EqualFold(d.Type, "artist") {
			continue
		}
		s := models.PlexSection{Key: d.Key, Title: d.Title}
		for _, l := range d.Location {
			s.Locations = append(s.Locations, l.Path)
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, errors.New("no music section found (type=artist)")
	}
	return out, nil
}

// searchSections is the sections an album lookup covers: the client's own, or every
// music section.
func (p *PlexClient) searchSections() ([]string, error) {
	if p.SectionKey != "" {
		return []string{p.SectionKey}, nil
	}
	sections, err := p.MusicSections()
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(sections))
	for _, s := range sections {
		keys = append(keys, s.Key)
	}
	return keys, nil
}

// "/library/metadata/196905" or "/library/metadata/196905/children" -> "/library/metadata/196905"
func normalizeAlbumKey(key string) string {
	key = strings.TrimSpace(key)
	key = strings.TrimSuffix(key, "/children")
	return key
}

// albumCandidates lists the albums in one section that could hold this file: albums
// by the artist with a matching title (type=9), then the albums of tracks matching
// artist, album and title (type=10). Each is the album ratingKey path
// ("/library/metadata/<id>"), deduplicated, in that order. Titles only nominate;
// albumHoldsFile decides.
func (p *PlexClient) albumCandidates(sectionID, artistName, albumTitle, trackTitle string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	add := func(key string) {
		if key != "" && !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	wantAlbum := utilities.Canon(albumTitle)
	wantArtist := utilities.Canon(artistName)

	var albums models.PlexMediaContainer
	if err := p.get(fmt.Sprintf("/library/sections/%s/all?type=9&artist.title=%s",
		url.PathEscape(sectionID), url.QueryEscape(artistName)), &albums); err != nil {
		return nil, err
	}
	for _, d := range albums.Directory {
		if utilities.EqLoose(utilities.Canon(d.Title), wantAlbum) &&
			utilities.EqLoose(utilities.Canon(d.ParentTitle), wantArtist) {
			add(normalizeAlbumKey(d.Key))
		}
	}

	// Track search — for singles, and for an album Plex files under another artist.
	q := fmt.Sprintf("/library/sections/%s/all?type=10&artist.title=%s",
		url.PathEscape(sectionID), url.QueryEscape(artistName))
	if trackTitle != "" {
		q += "&title=" + url.QueryEscape(trackTitle)
	}
	var tracks models.PlexMediaContainer
	if err := p.get(q, &tracks); err != nil {
		return nil, err
	}
	wantTrack := utilities.Canon(trackTitle)
	for _, t := range tracks.Track {
		if utilities.EqLoose(utilities.Canon(t.ParentTitle), wantAlbum) &&
			(trackTitle == "" || utilities.EqLoose(utilities.Canon(t.Title), wantTrack)) {
			if t.ParentKey != "" {
				add(normalizeAlbumKey(t.ParentKey))
			} else if t.ParentRatingKey != "" {
				add("/library/metadata/" + t.ParentRatingKey)
			}
		}
	}
	return out, nil
}

// albumHoldsFile reports whether one of the album's tracks is this file. relPath is
// the file's path below its library root; a Plex path matches when it ends with it,
// so the two sides may mount the library at different places (a Docker volume, a
// share) without any mapping being configured.
func (p *PlexClient) albumHoldsFile(albumKey, relPath string) (bool, error) {
	var mc models.PlexMediaContainer
	if err := p.get(normalizeAlbumKey(albumKey)+"/children", &mc); err != nil {
		return false, err
	}
	for _, t := range mc.Track {
		for _, m := range t.Media {
			for _, part := range m.Part {
				if plexPathEndsWith(part.File, relPath) {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// plexPathEndsWith compares by whole path segments, ignoring separator style and
// case: Plex may run on Windows while Autotaggerr runs in a container, and the
// library root is the one part of the path the two never agree on.
func plexPathEndsWith(plexPath, relPath string) bool {
	norm := func(s string) string {
		return strings.ToLower(strings.Trim(strings.ReplaceAll(s, "\\", "/"), "/"))
	}
	full, rel := norm(plexPath), norm(relPath)
	if rel == "" {
		return false
	}
	return full == rel || strings.HasSuffix(full, "/"+rel)
}

// ErrPlexAlbumNotFound is a file no Plex album holds: Plex has not scanned it yet, or
// the library is not in Plex at all. Not an error worth a warning per file — Plex's
// own scan picks a new album up, and there is nothing to refresh before it does.
var ErrPlexAlbumNotFound = errors.New("no Plex album holds this file")

// ResolveAlbumForFile finds the Plex album holding filePath. Candidates come from
// title searches in the client's sections; one is taken only if Plex lists this very
// file among its tracks. Matching titles alone refreshed whatever same-named album
// Plex had — in another section, or when this one was not in Plex at all.
func (p *PlexClient) ResolveAlbumForFile(filePath, rootDir, artistName, albumTitle, trackTitle string) (string, error) {
	rel, err := filepath.Rel(rootDir, filePath)
	if err != nil || rootDir == "" || strings.HasPrefix(rel, "..") {
		// No library root to measure from: the file name is all that can be compared.
		rel = filepath.Base(filePath)
	}
	sections, err := p.searchSections()
	if err != nil {
		return "", err
	}
	for _, section := range sections {
		candidates, err := p.albumCandidates(section, artistName, albumTitle, trackTitle)
		if err != nil {
			return "", err
		}
		for _, key := range candidates {
			holds, err := p.albumHoldsFile(key, filepath.ToSlash(rel))
			if err != nil {
				return "", err
			}
			if holds {
				return key, nil
			}
		}
	}
	return "", ErrPlexAlbumNotFound
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
		return false, fmt.Errorf("plex %s -> %d: %s", path, resp.StatusCode, strings.TrimSpace(string(b)))
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

// PlexAlbumForFile is the Plex album holding a file, or "" when Plex holds none. The
// answer is cached per album folder and section scope — not per album title, which
// two albums can share — for a month when found and an hour when not, so an album
// missing from Plex costs one lookup rather than one per track. The refresh path and
// the album page's link both ask here, so a page opened after a run costs nothing.
func PlexAlbumForFile(plexClient *PlexClient, filePath, rootDir, albumTitle, releaseArtist, trackTitle string) (string, error) {
	if plexClient == nil {
		return "", nil
	}
	folder := filepath.Dir(filePath)
	cacheKey := plexFolderKey(plexClient.SectionKey, folder)

	plexAlbumKeyCacheMu.RLock()
	cached, ok := plexAlbumKeyCache[cacheKey]
	plexAlbumKeyCacheMu.RUnlock()
	if ok && time.Since(cached.Timestamp) < plexAlbumTTL(cached.AlbumKey) {
		return cached.AlbumKey, nil
	}

	resolved, err := plexClient.ResolveAlbumForFile(filePath, rootDir, releaseArtist, albumTitle, trackTitle)
	if err != nil && !errors.Is(err, ErrPlexAlbumNotFound) {
		return "", err
	}
	entry := models.PlexAlbumKeyCache{AlbumKey: resolved, Title: albumTitle, Folder: folder, Timestamp: time.Now()}
	plexAlbumKeyCacheMu.Lock()
	plexAlbumKeyCache[cacheKey] = entry
	plexAlbumKeyCacheMu.Unlock()
	providerCachePut(models.ProviderCachePlexAlbumKeys, cacheKey, entry, plexAlbumTTL(resolved))
	return resolved, nil
}

func plexAlbumTTL(albumKey string) time.Duration {
	if albumKey == "" {
		return plexAlbumMissingDuration
	}
	return plexAlbumKeyCacheDuration
}

// InvalidatePlexAlbum forgets every folder resolved to this album. Called when Plex
// refuses a refresh of it: a month-long answer has to be dropped the moment Plex says
// the album is not what it was — merged, re-matched or deleted — or every later run
// would refresh the same dead key.
func InvalidatePlexAlbum(albumKey string) {
	albumKey = normalizeAlbumKey(albumKey)
	plexAlbumKeyCacheMu.Lock()
	var dropped []string
	for k, v := range plexAlbumKeyCache {
		if normalizeAlbumKey(v.AlbumKey) == albumKey {
			delete(plexAlbumKeyCache, k)
			dropped = append(dropped, k)
		}
	}
	plexAlbumKeyCacheMu.Unlock()
	for _, k := range dropped {
		providerCacheDrop(models.ProviderCachePlexAlbumKeys, k)
	}
}

// PlexRefreshForFile queues a refresh of the Plex album holding a file whose tags
// just changed. A file no album holds queues nothing.
func PlexRefreshForFile(refreshSet *AlbumRefreshSet, plexClient *PlexClient, filePath, rootDir, albumTitle, releaseArtist, trackTitle string) error {
	albumKey, err := PlexAlbumForFile(plexClient, filePath, rootDir, albumTitle, releaseArtist, trackTitle)
	if err != nil {
		// Non-fatal: the file's tags were already written; we just can't queue a Plex
		// refresh for it. The caller logs it once at Warn level with the album context.
		return fmt.Errorf("resolve Plex album for %q: %w", albumTitle, err)
	}
	if albumKey == "" {
		logger.Log.Debugf("no Plex album holds %s; nothing to refresh", filePath)
		return nil
	}
	refreshSet.Add(albumTitle, albumKey)
	return nil
}

// PlexAlbumForItem is PlexAlbumForFile for an indexed file, with the names the
// search needs read from its cached release the way the tagging path derives them.
// ok is false when the release is not cached — then there is nothing to search by,
// and the caller has no link rather than a wrong one.
func PlexAlbumForItem(plexClient *PlexClient, filePath, rootDir string, correlation models.Correlation) (key string, ok bool, err error) {
	release, cached := CachedRelease(correlation.MBReleaseID)
	if !cached {
		return "", false, nil
	}
	track, media, found := matchCorrelatedTrack(release, correlation)
	if !found {
		return "", false, nil
	}
	tags, err := BuildFileTags(track, media, release, models.TaggerSettings{})
	if err != nil {
		return "", false, err
	}
	key, err = PlexAlbumForFile(plexClient, filePath, rootDir, release.Title, tags.AlbumArtist, track.Title)
	return key, err == nil, err
}

// plexFolderKey is the cache key for one album folder in one section scope. Hashed:
// a path can outrun the key column, and the folder is kept in the payload for anyone
// reading the table.
func plexFolderKey(sectionKey, folder string) string {
	sum := sha256.Sum256([]byte(sectionKey + "\x00" + folder))
	return "folder:" + hex.EncodeToString(sum[:16])
}

// ForgetPlexFolder drops what the cache knows about one album folder, in every section
// scope — for a folder whose files have just been moved away.
func ForgetPlexFolder(folder string) {
	plexAlbumKeyCacheMu.Lock()
	var dropped []string
	for k, v := range plexAlbumKeyCache {
		if v.Folder == folder {
			delete(plexAlbumKeyCache, k)
			dropped = append(dropped, k)
		}
	}
	plexAlbumKeyCacheMu.Unlock()
	for _, k := range dropped {
		providerCacheDrop(models.ProviderCachePlexAlbumKeys, k)
	}
}

// ScanSections asks Plex to scan the client's section — or every music section — for
// changes. A moved file is found by a scan, not by refreshing the album it left: the
// album's metadata refresh re-reads files that are no longer at their paths. Plex scans
// only folders that changed, so asking for the whole section is cheap.
func (p *PlexClient) ScanSections() error {
	sections, err := p.searchSections()
	if err != nil {
		return err
	}
	for _, key := range sections {
		u := p.buildURL("/library/sections/"+url.PathEscape(key)+"/refresh", nil)
		resp, err := p.HTTP.Get(u)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("plex section %s scan: %s", key, resp.Status)
		}
	}
	return nil
}

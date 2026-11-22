package modules

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// plexHistoryPage is how many plays one history request asks for.
const plexHistoryPage = 500

var (
	// plexTrackFileTTL is how long a track's file is believed; a missing track (Plex
	// answered 404) is believed for a day, since a rescan can bring it back.
	plexTrackFileTTL        = 30 * 24 * time.Hour
	plexTrackFileMissingTTL = 24 * time.Hour
	plexTrackFiles          = map[string]plexTrackFile{}
	plexTrackFilesMu        sync.RWMutex
)

// plexTrackFile is a cached answer to "which file is this rating key?".
type plexTrackFile struct {
	File      string    `json:"file"`
	Timestamp time.Time `json:"timestamp"`
}

// PlexLoadTrackFileCache warms the rating-key → file map at startup.
func PlexLoadTrackFileCache() error {
	plexTrackFilesMu.Lock()
	defer plexTrackFilesMu.Unlock()
	return providerCacheRestore(models.ProviderCachePlexTrackFiles, plexTrackFiles)
}

// PlexAccount is one account on a Plex server: the owner (id 1) or a shared or
// managed user.
type PlexAccount struct {
	ID   string `xml:"id,attr" json:"id"`
	Name string `xml:"name,attr" json:"name"`
}

// Accounts lists the server's accounts.
func (p *PlexClient) Accounts() ([]PlexAccount, error) {
	var mc struct {
		Account []PlexAccount `xml:"Account"`
	}
	if err := p.get("/accounts", &mc); err != nil {
		return nil, err
	}
	out := make([]PlexAccount, 0, len(mc.Account))
	for _, a := range mc.Account {
		// Plex lists a nameless account 0 for "the server itself".
		if a.ID == "" || a.ID == "0" {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

// PlexPlay is one play from the server's history.
type PlexPlay struct {
	HistoryKey string
	RatingKey  string
	ViewedAt   time.Time
	Title      string
	Artist     string
	Album      string
}

type plexHistoryItem struct {
	HistoryKey       string `xml:"historyKey,attr"`
	RatingKey        string `xml:"ratingKey,attr"`
	Type             string `xml:"type,attr"`
	Title            string `xml:"title,attr"`
	ParentTitle      string `xml:"parentTitle,attr"`
	GrandparentTitle string `xml:"grandparentTitle,attr"`
	OriginalTitle    string `xml:"originalTitle,attr"`
	ViewedAt         int64  `xml:"viewedAt,attr"`
}

type plexHistoryContainer struct {
	TotalSize int               `xml:"totalSize,attr"`
	Size      int               `xml:"size,attr"`
	Track     []plexHistoryItem `xml:"Track"`
	Video     []plexHistoryItem `xml:"Video"`
	Metadata  []plexHistoryItem `xml:"Metadata"`
}

// History reads one account's plays after since, oldest first, a page at a time,
// handing each page of tracks to store as it is read. It returns the newest play
// time seen, which is where the next pass starts.
func (p *PlexClient) History(accountID string, since time.Time, store func([]PlexPlay) error) (time.Time, error) {
	newest := since
	for start := 0; ; start += plexHistoryPage {
		q := "/status/sessions/history/all?sort=viewedAt:asc&accountID=" + url.QueryEscape(accountID) +
			"&X-Plex-Container-Start=" + strconv.Itoa(start) + "&X-Plex-Container-Size=" + strconv.Itoa(plexHistoryPage)
		if !since.IsZero() {
			q += "&viewedAt>=" + strconv.FormatInt(since.Unix(), 10)
		}
		var mc plexHistoryContainer
		if err := p.get(q, &mc); err != nil {
			return newest, err
		}
		items := append(append(append([]plexHistoryItem{}, mc.Track...), mc.Metadata...), mc.Video...)
		var plays []PlexPlay
		for _, it := range items {
			if it.Type != "" && it.Type != "track" {
				continue
			}
			at := time.Unix(it.ViewedAt, 0).UTC()
			if !since.IsZero() && !at.After(since) {
				continue
			}
			artist := it.GrandparentTitle
			if it.OriginalTitle != "" {
				artist = it.OriginalTitle
			}
			key := it.HistoryKey
			if key == "" {
				key = it.RatingKey + "@" + strconv.FormatInt(it.ViewedAt, 10)
			}
			plays = append(plays, PlexPlay{HistoryKey: key, RatingKey: it.RatingKey, ViewedAt: at,
				Title: it.Title, Artist: artist, Album: it.ParentTitle})
			if at.After(newest) {
				newest = at
			}
		}
		if len(plays) > 0 {
			if err := store(plays); err != nil {
				return newest, err
			}
		}
		if len(items) < plexHistoryPage {
			return newest, nil
		}
	}
}

// errPlexNotFound is a Plex 404, which for a track means it is gone.
func isPlexNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "-> 404")
}

// TrackFile is the file a track's rating key stands for, as Plex sees the path, or ""
// when Plex no longer has the track. Cached per server and key.
func (p *PlexClient) TrackFile(ratingKey string) (string, error) {
	if ratingKey == "" {
		return "", nil
	}
	cacheKey := p.ServerID + ":" + ratingKey
	plexTrackFilesMu.RLock()
	cached, ok := plexTrackFiles[cacheKey]
	plexTrackFilesMu.RUnlock()
	if ok {
		ttl := plexTrackFileTTL
		if cached.File == "" {
			ttl = plexTrackFileMissingTTL
		}
		if time.Since(cached.Timestamp) < ttl {
			return cached.File, nil
		}
	}

	var mc models.PlexMediaContainer
	err := p.get("/library/metadata/"+url.PathEscape(ratingKey), &mc)
	if err != nil && !isPlexNotFound(err) {
		return "", err
	}
	file := ""
	for _, t := range mc.Track {
		for _, m := range t.Media {
			for _, part := range m.Part {
				if file == "" && part.File != "" {
					file = part.File
				}
			}
		}
	}
	entry := plexTrackFile{File: file, Timestamp: time.Now()}
	plexTrackFilesMu.Lock()
	plexTrackFiles[cacheKey] = entry
	plexTrackFilesMu.Unlock()
	ttl := plexTrackFileTTL
	if file == "" {
		ttl = plexTrackFileMissingTTL
	}
	providerCachePut(models.ProviderCachePlexTrackFiles, cacheKey, entry, ttl)
	return file, nil
}

// TrailingPathIndex finds a library file by the end of another machine's path to
// it: Plex may mount the library somewhere else, or run on Windows, so only the last
// few segments (artist folder, album folder, file) are comparable. The same idea as
// plexPathEndsWith, turned around so one map answers every play of a sync.
type TrailingPathIndex struct {
	bySuffix [3]map[string][]int
	items    []models.LibraryItem
}

// trailingDepths are the suffix lengths indexed: three segments first, longer ones
// only to tell apart two files that share three.
var trailingDepths = [3]int{3, 4, 5}

func pathSegments(p string) []string {
	p = strings.ToLower(strings.Trim(strings.ReplaceAll(p, "\\", "/"), "/"))
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func suffixKey(segs []string, n int) string {
	if len(segs) < n {
		n = len(segs)
	}
	return strings.Join(segs[len(segs)-n:], "/")
}

// NewTrailingPathIndex indexes the given files.
func NewTrailingPathIndex(items []models.LibraryItem) *TrailingPathIndex {
	idx := &TrailingPathIndex{items: items}
	for d := range idx.bySuffix {
		idx.bySuffix[d] = make(map[string][]int, len(items))
	}
	for i, it := range items {
		segs := pathSegments(it.Path)
		if len(segs) == 0 {
			continue
		}
		for d, n := range trailingDepths {
			k := suffixKey(segs, n)
			idx.bySuffix[d][k] = append(idx.bySuffix[d][k], i)
		}
	}
	return idx
}

// Lookup is the one indexed file the path ends like, if exactly one does.
func (idx *TrailingPathIndex) Lookup(path string) (models.LibraryItem, bool) {
	segs := pathSegments(path)
	if idx == nil || len(segs) == 0 {
		return models.LibraryItem{}, false
	}
	for d, n := range trailingDepths {
		found := idx.bySuffix[d][suffixKey(segs, n)]
		switch len(found) {
		case 0:
			return models.LibraryItem{}, false
		case 1:
			return idx.items[found[0]], true
		}
		if len(segs) <= n {
			break
		}
	}
	return models.LibraryItem{}, false
}

// ErrPlexAccountNotFound is a plex.tv identity with no account on the server.
var ErrPlexAccountNotFound = errors.New("this Plex account has no access to the server")

// PlexTVBaseURL is plex.tv's API, for the sign-in that proves which Plex account a
// user is. A variable so tests can point it at a stub.
var PlexTVBaseURL = "https://plex.tv"

// PlexTVClientID identifies this install to plex.tv during sign-in.
var PlexTVClientID = "autotaggerr"

// PlexPin is a plex.tv sign-in in progress: the user opens AuthURL, and the pin
// carries a token once they have signed in.
type PlexPin struct {
	ID        int64  `json:"id"`
	Code      string `json:"code"`
	AuthToken string `json:"authToken"`
}

// PlexAuthURL is where the user signs in to approve the pin.
func PlexAuthURL(code string) string {
	return "https://app.plex.tv/auth#?" + url.Values{
		"clientID":                 {PlexTVClientID},
		"code":                     {code},
		"context[device][product]": {"Autotaggerr"},
	}.Encode()
}

// PlexUser is the plex.tv account a token belongs to.
type PlexUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Title    string `json:"title"`
}

// PlexResource is a server a plex.tv account can reach.
type PlexResource struct {
	ClientIdentifier string `json:"clientIdentifier"`
	Owned            bool   `json:"owned"`
	Provides         string `json:"provides"`
}

// plexTV sends one plex.tv API request.
func plexTV(method, path, token string, dst any) error {
	req, err := newJSONRequest(method, strings.TrimRight(PlexTVBaseURL, "/")+path)
	if err != nil {
		return err
	}
	req.Header.Set("X-Plex-Client-Identifier", PlexTVClientID)
	req.Header.Set("X-Plex-Product", "Autotaggerr")
	if token != "" {
		req.Header.Set("X-Plex-Token", token)
	}
	return doJSON(req, dst, "plex.tv")
}

// CreatePlexPin starts a plex.tv sign-in.
func CreatePlexPin() (PlexPin, error) {
	var pin PlexPin
	err := plexTV("POST", "/api/v2/pins?strong=true", "", &pin)
	return pin, err
}

// CheckPlexPin reads a pin; AuthToken is set once the user has signed in.
func CheckPlexPin(id int64) (PlexPin, error) {
	var pin PlexPin
	err := plexTV("GET", "/api/v2/pins/"+strconv.FormatInt(id, 10), "", &pin)
	return pin, err
}

// PlexTVUser is the account a plex.tv token belongs to.
func PlexTVUser(token string) (PlexUser, error) {
	var u PlexUser
	err := plexTV("GET", "/api/v2/user", token, &u)
	return u, err
}

// PlexTVResources lists the servers a plex.tv token can reach.
func PlexTVResources(token string) ([]PlexResource, error) {
	var out []PlexResource
	err := plexTV("GET", "/api/v2/resources?includeHttps=1", token, &out)
	return out, err
}

// PlexAccountFor is the server-local account a signed-in plex.tv user has on this
// server: the owner's is always 1, which Plex does not tie to the plex.tv id, so
// ownership is read from plex.tv's resource list; anyone else is matched by id
// against the server's own account list.
func (p *PlexClient) PlexAccountFor(user PlexUser, resources []PlexResource) (PlexAccount, error) {
	machineID, err := p.MachineIdentifier()
	if err != nil {
		return PlexAccount{}, err
	}
	accounts, err := p.Accounts()
	if err != nil {
		return PlexAccount{}, err
	}
	for _, r := range resources {
		if r.ClientIdentifier == machineID && r.Owned {
			for _, a := range accounts {
				if a.ID == "1" {
					return a, nil
				}
			}
			return PlexAccount{ID: "1", Name: user.Username}, nil
		}
	}
	want := strconv.FormatInt(user.ID, 10)
	for _, a := range accounts {
		if a.ID == want {
			return a, nil
		}
	}
	return PlexAccount{}, fmt.Errorf("%w (%s)", ErrPlexAccountNotFound, p.DisplayName())
}

// plexTVHTTP is the client plex.tv requests go through.
var plexTVHTTP = &http.Client{Timeout: 15 * time.Second}

func newJSONRequest(method, u string) (*http.Request, error) {
	req, err := http.NewRequest(method, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

func doJSON(req *http.Request, dst any, service string) error {
	resp, err := plexTVHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s %s -> %d: %s", service, req.URL.Path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, dst)
}

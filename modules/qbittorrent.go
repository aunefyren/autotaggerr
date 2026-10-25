package modules

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// qBittorrent's Web API (v2). The client logs in with a username and password, holds
// the session cookie it is given, and logs in again once when a request is refused —
// a session expires when qBittorrent restarts, which is not the user's credentials
// being wrong.
//
// Two of qBittorrent's defences shape every request: it refuses a request whose
// Referer does not match its own host (CSRF protection), and it bans an address after
// repeated failed logins. So every request carries a Referer of the base URL, and a
// rejected login is reported rather than retried.

// qbittorrentTimeout bounds one request. A poll is a handful of small JSON documents;
// anything slower than this is a client that is not going to answer.
const qbittorrentTimeout = 30 * time.Second

// QBittorrentClient talks to one qBittorrent instance.
type QBittorrentClient struct {
	baseURL  string
	username string
	password string
	http     *http.Client

	// loginMu keeps concurrent requests from logging in twice at once; loggedIn is
	// whether the cookie jar holds a session worth trying.
	loginMu  sync.Mutex
	loggedIn bool
}

// NewQBittorrentClient builds a client. An empty username skips logging in, for a
// qBittorrent that bypasses authentication for this host.
func NewQBittorrentClient(baseURL, username, password string) *QBittorrentClient {
	jar, _ := cookiejar.New(nil) // never fails without options
	return &QBittorrentClient{
		baseURL:  strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		username: username,
		password: password,
		http:     &http.Client{Timeout: qbittorrentTimeout, Jar: jar},
	}
}

// errQBittorrentForbidden is a 403: no session, an expired one, or a Referer the
// client rejected.
var errQBittorrentForbidden = errors.New("qBittorrent refused the request (403)")

// qbittorrentTorrent is the part of /torrents/info this client reads.
type qbittorrentTorrent struct {
	Hash         string  `json:"hash"`
	Name         string  `json:"name"`
	Category     string  `json:"category"`
	Tags         string  `json:"tags"`
	ContentPath  string  `json:"content_path"`
	SavePath     string  `json:"save_path"`
	Size         int64   `json:"size"`
	Progress     float64 `json:"progress"`
	State        string  `json:"state"`
	CompletionOn int64   `json:"completion_on"`
	AmountLeft   int64   `json:"amount_left"`
}

// qbittorrentFile is one entry of /torrents/files.
type qbittorrentFile struct {
	Name     string  `json:"name"`
	Size     int64   `json:"size"`
	Progress float64 `json:"progress"`
	Priority int     `json:"priority"`
}

// qbittorrentSettled lists the states of a finished torrent whose files are where
// qBittorrent says and will stay there. Anything else — checking, moving, allocating,
// missing files, an error — is a torrent to look at again on the next poll. stoppedUP
// is qBittorrent 5's name for pausedUP.
var qbittorrentSettled = map[string]bool{
	"uploading": true,
	"stalledUP": true,
	"pausedUP":  true,
	"stoppedUP": true,
	"queuedUP":  true,
	"forcedUP":  true,
}

// Finished lists the finished torrents in each category.
//
// One request per category rather than one for everything: a seedbox holds thousands
// of torrents, and only the watched categories are worth transferring every poll.
func (c *QBittorrentClient) Finished(categories []string) ([]Download, error) {
	var out []Download
	seen := map[string]bool{}
	for _, category := range categories {
		var torrents []qbittorrentTorrent
		query := url.Values{"filter": {"completed"}, "category": {category}}
		if err := c.getJSON("/api/v2/torrents/info?"+query.Encode(), &torrents); err != nil {
			return nil, err
		}
		for _, t := range torrents {
			// The category filter is exact, but a subcategory's torrents are reported
			// under it on some versions; only the category asked for is ours.
			if t.Category != category || seen[t.Hash] {
				continue
			}
			if t.Progress < 1 || t.AmountLeft > 0 || !qbittorrentSettled[t.State] {
				continue
			}
			seen[t.Hash] = true
			d := Download{
				ID:        t.Hash,
				Name:      t.Name,
				Category:  t.Category,
				Path:      t.ContentPath,
				FilesRoot: t.SavePath,
				Size:      t.Size,
				Seeding:   true,
				Imported:  hasTag(t.Tags, models.DownloadClientImportedTag),
			}
			if d.Path == "" {
				// Older versions report no content path; the save path and name are what
				// it was made of.
				d.Path = strings.TrimRight(t.SavePath, `/\`) + "/" + t.Name
			}
			if t.CompletionOn > 0 {
				d.CompletedAt = time.Unix(t.CompletionOn, 0)
			}
			out = append(out, d)
		}
	}
	return out, nil
}

// Files lists a torrent's files, as absolute paths on the client's host. A file's
// name is relative to the torrent's save path — which, for a finished torrent, is
// where its files are, whatever temporary folder it downloaded into.
func (c *QBittorrentClient) Files(d Download) ([]DownloadFile, error) {
	var files []qbittorrentFile
	if err := c.getJSON("/api/v2/torrents/files?"+url.Values{"hash": {d.ID}}.Encode(), &files); err != nil {
		return nil, err
	}
	// Joined with the separator the client's own paths use: a Windows-hosted
	// qBittorrent reports D:\Torrents, and its names with forward slashes.
	sep := "/"
	if strings.Contains(d.FilesRoot, `\`) && !strings.Contains(d.FilesRoot, "/") {
		sep = `\`
	}
	root := strings.TrimRight(d.FilesRoot, `/\`)
	out := make([]DownloadFile, 0, len(files))
	for _, f := range files {
		out = append(out, DownloadFile{
			Path:     root + sep + strings.ReplaceAll(f.Name, "/", sep),
			Size:     f.Size,
			Complete: f.Priority > 0 && f.Progress >= 1,
		})
	}
	return out, nil
}

// MarkImported tags a torrent as imported.
func (c *QBittorrentClient) MarkImported(id string) error {
	form := url.Values{"hashes": {id}, "tags": {models.DownloadClientImportedTag}}
	return c.postForm("/api/v2/torrents/addTags", form)
}

// HealthCheck logs in and reads the version — the smallest request that proves both
// the address and the credentials.
func (c *QBittorrentClient) HealthCheck() (bool, error) {
	body, err := c.do(func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, c.baseURL+"/api/v2/app/version", nil)
	})
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(string(body)) == "" {
		return false, errors.New("qBittorrent answered, but did not report a version — check the base URL points at its Web UI")
	}
	return true, nil
}

func (c *QBittorrentClient) getJSON(path string, dst any) error {
	body, err := c.do(func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, c.baseURL+path, nil)
	})
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("qBittorrent sent something that is not its API's JSON for %s — check the base URL: %w", path, err)
	}
	return nil
}

func (c *QBittorrentClient) postForm(path string, form url.Values) error {
	_, err := c.do(func() (*http.Request, error) {
		req, err := http.NewRequest(http.MethodPost, c.baseURL+path, strings.NewReader(form.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		return req, err
	})
	return err
}

// do sends a request, logging in first when there is no session, and once more when
// the session is refused. The request is rebuilt for the retry, since a body cannot be
// sent twice.
func (c *QBittorrentClient) do(build func() (*http.Request, error)) ([]byte, error) {
	if err := c.ensureLogin(false); err != nil {
		return nil, err
	}
	body, err := c.send(build)
	if !errors.Is(err, errQBittorrentForbidden) || c.username == "" {
		if errors.Is(err, errQBittorrentForbidden) {
			return nil, errors.New("qBittorrent refused the request — set a username and password, or allow this host to skip authentication in qBittorrent's Web UI options")
		}
		return body, err
	}
	if err := c.ensureLogin(true); err != nil {
		return nil, err
	}
	body, err = c.send(build)
	if errors.Is(err, errQBittorrentForbidden) {
		return nil, errors.New("qBittorrent accepted the login but refused the request — if it sits behind a reverse proxy, check the proxy passes the Host header through")
	}
	return body, err
}

func (c *QBittorrentClient) send(build func() (*http.Request, error)) ([]byte, error) {
	req, err := build()
	if err != nil {
		return nil, err
	}
	// qBittorrent's CSRF protection compares this with its own host.
	req.Header.Set("Referer", c.baseURL)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("qBittorrent didn't respond — check the base URL: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusForbidden:
		return nil, errQBittorrentForbidden
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, errQBittorrentForbidden
	case resp.StatusCode >= 300:
		return nil, fmt.Errorf("qBittorrent answered %s for %s: %s", resp.Status, req.URL.Path, strings.TrimSpace(snippet(body)))
	}
	return body, nil
}

// ensureLogin logs in when there is no session yet, or always when force is set.
func (c *QBittorrentClient) ensureLogin(force bool) error {
	if c.username == "" {
		return nil
	}
	c.loginMu.Lock()
	defer c.loginMu.Unlock()
	if c.loggedIn && !force {
		return nil
	}
	form := url.Values{"username": {c.username}, "password": {c.password}}
	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/api/v2/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", c.baseURL)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("qBittorrent didn't respond — check the base URL: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	text := strings.TrimSpace(string(body))
	c.loggedIn = false
	switch {
	case resp.StatusCode == http.StatusForbidden:
		return errors.New("qBittorrent has banned this address after too many failed logins — wait, or restart qBittorrent, then check the username and password")
	case resp.StatusCode == http.StatusUnauthorized || text == "Fails.":
		// "Fails." with a 200 before qBittorrent 5.2; a 401 from 5.2 on.
		return errors.New("qBittorrent rejected the username or password")
	case resp.StatusCode >= 300:
		return fmt.Errorf("qBittorrent answered %s to the login: %s", resp.Status, snippet(body))
	case loginSucceeded(resp, text):
		c.loggedIn = true
		return nil
	case strings.HasPrefix(strings.ToLower(text), "<"):
		return errors.New("the login was answered with a web page, not qBittorrent's API — if an authentication proxy sits in front of it, point the base URL past the proxy")
	}
	return fmt.Errorf("qBittorrent did not confirm the login (it answered %s: %q) — check the base URL points at its Web UI", resp.Status, snippet(body))
}

// loginSucceeded reads a 2xx login answer. Before qBittorrent 5.2 a login answered 200
// "Ok."; from 5.2 it answers 204 with no body. Either way the session is the cookie it
// sets, so a response setting one is a login too, whatever the version says.
func loginSucceeded(resp *http.Response, text string) bool {
	return text == "Ok." || resp.StatusCode == http.StatusNoContent || len(resp.Cookies()) > 0
}

// hasTag reports whether qBittorrent's comma-separated tag list carries tag.
func hasTag(tags, tag string) bool {
	for _, t := range strings.Split(tags, ",") {
		if strings.TrimSpace(t) == tag {
			return true
		}
	}
	return false
}

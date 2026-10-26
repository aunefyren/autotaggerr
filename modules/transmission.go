package modules

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// Transmission's RPC. Every call is a JSON POST to /transmission/rpc, behind HTTP basic
// auth when the daemon asks for it, and behind Transmission's CSRF guard: a request
// without the current X-Transmission-Session-Id is answered 409 with the id to use, and
// is sent once more with it.
//
// Transmission has no categories, only labels (from 3.0), so a watched category is a
// label: a torrent is in the category when it carries the label. The imported mark is
// a label too, and a post-import category swaps the watched label for another.

// TransmissionClient talks to one Transmission daemon.
type TransmissionClient struct {
	rpcURL   string
	username string
	password string
	http     *http.Client

	mu        sync.Mutex
	sessionID string
}

// NewTransmissionClient builds a client. The base URL is the daemon's web address
// (http://host:9091); one already ending in /rpc is used as it is, for a daemon whose
// RPC path was changed.
func NewTransmissionClient(baseURL, username, password string) *TransmissionClient {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	rpc := base + "/transmission/rpc"
	if strings.HasSuffix(base, "/rpc") {
		rpc = base
	}
	return &TransmissionClient{rpcURL: rpc, username: username, password: password,
		http: &http.Client{Timeout: qbittorrentTimeout}}
}

// transmissionTorrent is the part of torrent-get this client reads.
type transmissionTorrent struct {
	HashString    string   `json:"hashString"`
	Name          string   `json:"name"`
	Labels        []string `json:"labels"`
	DownloadDir   string   `json:"downloadDir"`
	PercentDone   float64  `json:"percentDone"`
	LeftUntilDone int64    `json:"leftUntilDone"`
	Status        int      `json:"status"`
	Error         int      `json:"error"`
	DoneDate      int64    `json:"doneDate"`
	TotalSize     int64    `json:"totalSize"`
	IsFinished    bool     `json:"isFinished"`
	Files         []struct {
		Name           string `json:"name"`
		Length         int64  `json:"length"`
		BytesCompleted int64  `json:"bytesCompleted"`
	} `json:"files"`
	FileStats []struct {
		Wanted bool `json:"wanted"`
	} `json:"fileStats"`
}

// Transmission's torrent status codes.
const (
	transmissionStopped   = 0
	transmissionSeedQueue = 5
	transmissionSeeding   = 6
)

// Finished lists the finished torrents carrying any of the categories' labels. A
// torrent counts only stopped, queued to seed or seeding — checking, verifying and
// downloading ones are looked at again on the next poll — and without a local error.
func (c *TransmissionClient) Finished(categories []string) ([]Download, error) {
	torrents, err := c.torrents(nil, "hashString", "name", "labels", "downloadDir", "percentDone",
		"leftUntilDone", "status", "error", "doneDate", "totalSize")
	if err != nil {
		return nil, err
	}
	var out []Download
	for _, t := range torrents {
		category := ""
		for _, want := range categories {
			if slices.Contains(t.Labels, want) {
				category = want
				break
			}
		}
		if category == "" || t.PercentDone < 1 || t.LeftUntilDone > 0 || t.Error != 0 {
			continue
		}
		if t.Status != transmissionStopped && t.Status != transmissionSeedQueue && t.Status != transmissionSeeding {
			continue
		}
		d := Download{
			ID:        t.HashString,
			Name:      t.Name,
			Category:  category,
			Path:      joinClientPath(t.DownloadDir, t.Name),
			FilesRoot: t.DownloadDir,
			Size:      t.TotalSize,
			Seeding:   true,
			Imported:  slices.Contains(t.Labels, models.DownloadClientImportedTag),
		}
		if t.DoneDate > 0 {
			d.CompletedAt = time.Unix(t.DoneDate, 0)
		}
		out = append(out, d)
	}
	return out, nil
}

// Files lists a torrent's files. A file's name is relative to the download folder; a
// deselected one, or one not fully on disk, is not complete.
func (c *TransmissionClient) Files(d Download) ([]DownloadFile, error) {
	torrents, err := c.torrents([]string{d.ID}, "hashString", "files", "fileStats")
	if err != nil {
		return nil, err
	}
	if len(torrents) == 0 {
		return nil, fmt.Errorf("Transmission no longer has the torrent %s", d.Name)
	}
	t := torrents[0]
	out := make([]DownloadFile, 0, len(t.Files))
	for i, f := range t.Files {
		wanted := i >= len(t.FileStats) || t.FileStats[i].Wanted
		out = append(out, DownloadFile{
			Path:      joinClientPath(d.FilesRoot, f.Name),
			FilesRoot: d.FilesRoot,
			Size:      f.Length,
			Complete:  wanted && f.BytesCompleted >= f.Length,
		})
	}
	return out, nil
}

// MarkImported adds the imported label and, when to is set, swaps the watched label
// from for it. The other labels are kept: they are someone's.
func (c *TransmissionClient) MarkImported(id, from, to string) error {
	torrents, err := c.torrents([]string{id}, "hashString", "labels")
	if err != nil {
		return err
	}
	if len(torrents) == 0 {
		return errors.New("Transmission no longer has the torrent")
	}
	labels := []string{}
	for _, l := range torrents[0].Labels {
		if strings.TrimSpace(to) != "" && l == from {
			continue
		}
		labels = append(labels, l)
	}
	for _, add := range []string{models.DownloadClientImportedTag, strings.TrimSpace(to)} {
		if add != "" && !slices.Contains(labels, add) {
			labels = append(labels, add)
		}
	}
	return c.call("torrent-set", map[string]any{"ids": []string{id}, "labels": labels}, nil)
}

// SeedingDone reports which torrents Transmission stopped for reaching their seed
// ratio or idle limit: isFinished is exactly that, and it is only ever set by a limit.
func (c *TransmissionClient) SeedingDone(ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	torrents, err := c.torrents(ids, "hashString", "status", "isFinished")
	if err != nil {
		return nil, err
	}
	for _, t := range torrents {
		out[t.HashString] = t.IsFinished && t.Status == transmissionStopped
	}
	return out, nil
}

// Remove deletes a torrent and its files.
func (c *TransmissionClient) Remove(id string) error {
	return c.call("torrent-remove", map[string]any{"ids": []string{id}, "delete-local-data": true}, nil)
}

// HealthCheck reads the session — the smallest call that proves the address, the
// credentials and the session handshake.
func (c *TransmissionClient) HealthCheck() (bool, error) {
	var session struct {
		Version string `json:"version"`
	}
	if err := c.call("session-get", map[string]any{"fields": []string{"version"}}, &session); err != nil {
		return false, err
	}
	if session.Version == "" {
		return false, errors.New("Transmission answered, but did not report a version — check the base URL")
	}
	return true, nil
}

func (c *TransmissionClient) torrents(ids []string, fields ...string) ([]transmissionTorrent, error) {
	args := map[string]any{"fields": fields}
	if ids != nil {
		args["ids"] = ids
	}
	var out struct {
		Torrents []transmissionTorrent `json:"torrents"`
	}
	if err := c.call("torrent-get", args, &out); err != nil {
		return nil, err
	}
	return out.Torrents, nil
}

// call sends one RPC, answering the session handshake once.
func (c *TransmissionClient) call(method string, args map[string]any, dst any) error {
	payload, err := json.Marshal(map[string]any{"method": method, "arguments": args})
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequest(http.MethodPost, c.rpcURL, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		c.mu.Lock()
		if c.sessionID != "" {
			req.Header.Set("X-Transmission-Session-Id", c.sessionID)
		}
		c.mu.Unlock()
		if c.username != "" || c.password != "" {
			req.SetBasicAuth(c.username, c.password)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("Transmission didn't respond — check the base URL: %w", err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if err != nil {
			return err
		}
		switch {
		case resp.StatusCode == http.StatusConflict && resp.Header.Get("X-Transmission-Session-Id") != "":
			c.mu.Lock()
			c.sessionID = resp.Header.Get("X-Transmission-Session-Id")
			c.mu.Unlock()
			continue
		case resp.StatusCode == http.StatusUnauthorized:
			return errors.New("Transmission rejected the username or password")
		case resp.StatusCode == http.StatusForbidden:
			return errors.New("Transmission refused this address — add it to rpc-whitelist, or turn the whitelist off")
		case resp.StatusCode >= 300:
			return fmt.Errorf("Transmission answered %s: %s", resp.Status, strings.TrimSpace(snippet(body)))
		}
		var reply struct {
			Result    string          `json:"result"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(body, &reply); err != nil {
			return fmt.Errorf("Transmission sent something that is not its RPC's JSON — check the base URL: %w", err)
		}
		if reply.Result != "success" {
			return fmt.Errorf("Transmission refused %s: %s", method, reply.Result)
		}
		if dst != nil && len(reply.Arguments) > 0 {
			return json.Unmarshal(reply.Arguments, dst)
		}
		return nil
	}
	return errors.New("Transmission kept refusing the session id — check nothing between here and it strips the X-Transmission-Session-Id header")
}

// joinClientPath joins a folder and a name the way the client's own paths are written:
// with backslashes for a Windows-hosted client.
func joinClientPath(dir, name string) string {
	sep := "/"
	if strings.Contains(dir, `\`) && !strings.Contains(dir, "/") {
		sep = `\`
	}
	return strings.TrimRight(dir, `/\`) + sep + strings.ReplaceAll(name, "/", sep)
}

package modules

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SABnzbd's API: GET /api with an API key, answering JSON. A finished job is in the
// history, with its final folder (storage) and its category. Nothing seeds — SABnzbd is
// done with the files once a job completes — so an import may move them, and there is
// no imported mark to write: the history has no tags, and the database row is the
// record. Nor can it list a job's files, so they are read from the folder here once
// the path is mapped (Download.FilesOnDisk).

// sabnzbdHistoryLimit bounds one history read per category: the newest jobs are the
// ones not yet seen, and a busy history holds thousands.
const sabnzbdHistoryLimit = 200

// SABnzbdClient talks to one SABnzbd instance.
type SABnzbdClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// NewSABnzbdClient builds a client from the web address and the API key.
func NewSABnzbdClient(baseURL, apiKey string) *SABnzbdClient {
	return &SABnzbdClient{baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"), apiKey: apiKey,
		http: &http.Client{Timeout: qbittorrentTimeout}}
}

type sabnzbdSlot struct {
	NzoID     string `json:"nzo_id"`
	Name      string `json:"name"`
	Category  string `json:"category"`
	Status    string `json:"status"`
	Storage   string `json:"storage"`
	Bytes     int64  `json:"bytes"`
	Completed int64  `json:"completed"`
}

// Finished lists the completed jobs in each category's history. SABnzbd lowercases
// category names, so they are compared without case.
func (c *SABnzbdClient) Finished(categories []string) ([]Download, error) {
	var out []Download
	seen := map[string]bool{}
	for _, category := range categories {
		var reply struct {
			History struct {
				Slots []sabnzbdSlot `json:"slots"`
			} `json:"history"`
		}
		q := url.Values{"mode": {"history"}, "category": {category}, "limit": {fmt.Sprint(sabnzbdHistoryLimit)}}
		if err := c.get(q, &reply); err != nil {
			return nil, err
		}
		for _, s := range reply.History.Slots {
			if !strings.EqualFold(s.Category, category) || s.Status != "Completed" || s.Storage == "" || seen[s.NzoID] {
				continue
			}
			seen[s.NzoID] = true
			d := Download{
				ID:          s.NzoID,
				Name:        s.Name,
				Category:    category,
				Path:        s.Storage,
				FilesRoot:   s.Storage,
				Size:        s.Bytes,
				FilesOnDisk: true,
			}
			if s.Completed > 0 {
				d.CompletedAt = time.Unix(s.Completed, 0)
			}
			out = append(out, d)
		}
	}
	return out, nil
}

// Files is empty: SABnzbd cannot list a finished job's files (see FilesOnDisk).
func (c *SABnzbdClient) Files(Download) ([]DownloadFile, error) { return nil, nil }

// MarkImported writes nothing: a history entry has no tags and no category to change.
func (c *SABnzbdClient) MarkImported(string, string, string) error { return nil }

// HealthCheck reads one history entry, which needs a valid API key — the version call
// would answer without one.
func (c *SABnzbdClient) HealthCheck() (bool, error) {
	var reply struct {
		History *struct{} `json:"history"`
	}
	if err := c.get(url.Values{"mode": {"history"}, "limit": {"1"}}, &reply); err != nil {
		return false, err
	}
	if reply.History == nil {
		return false, errors.New("SABnzbd answered, but not with its history — check the base URL")
	}
	return true, nil
}

func (c *SABnzbdClient) get(q url.Values, dst any) error {
	q.Set("output", "json")
	q.Set("apikey", c.apiKey)
	resp, err := c.http.Get(c.baseURL + "/api?" + q.Encode())
	if err != nil {
		return fmt.Errorf("SABnzbd didn't respond — check the base URL: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("SABnzbd answered %s: %s", resp.Status, strings.TrimSpace(snippet(body)))
	}
	// A refused key is a 200 with {"status": false, "error": "..."}.
	var refusal struct {
		Status *bool  `json:"status"`
		Error  string `json:"error"`
	}
	if json.Unmarshal(body, &refusal) == nil && refusal.Status != nil && !*refusal.Status {
		if strings.Contains(strings.ToLower(refusal.Error), "api key") {
			return errors.New("SABnzbd rejected the API key")
		}
		return fmt.Errorf("SABnzbd refused the request: %s", refusal.Error)
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("SABnzbd sent something that is not its API's JSON — check the base URL: %w", err)
	}
	return nil
}

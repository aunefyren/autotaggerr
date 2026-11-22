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
	"time"
)

// ListenBrainzBaseURL is the public API. A variable so tests can point it at a stub.
var ListenBrainzBaseURL = "https://api.listenbrainz.org"

// listenBrainzPage is how many listens one request asks for: the API's maximum.
const listenBrainzPage = 1000

// listenBrainzSleep is how the client waits out a rate limit. A variable so tests do
// not sleep.
var listenBrainzSleep = time.Sleep

// ListenBrainzClient reads one user's listens. The token is optional: public listens
// need none, and a token only matters for an account whose listens are private.
type ListenBrainzClient struct {
	BaseURL  string
	Username string
	Token    string
	HTTP     *http.Client
}

// NewListenBrainzClient builds a client for one user against ListenBrainzBaseURL.
func NewListenBrainzClient(username, token string) *ListenBrainzClient {
	return &ListenBrainzClient{
		BaseURL:  strings.TrimRight(ListenBrainzBaseURL, "/"),
		Username: strings.TrimSpace(username),
		Token:    strings.TrimSpace(token),
		HTTP:     &http.Client{Timeout: 30 * time.Second},
	}
}

// ListenBrainzArtist is one artist of a mapped listen.
type ListenBrainzArtist struct {
	MBID string
	Name string
}

// ListenBrainzListen is one listen, reduced to what is stored.
type ListenBrainzListen struct {
	ListenedAt    time.Time
	MSID          string
	RecordingMBID string
	ReleaseMBID   string
	Title         string
	Artist        string
	Album         string
	Artists       []ListenBrainzArtist
}

// ExternalKey identifies the listen at ListenBrainz: its time and its MessyBrainz id.
func (l ListenBrainzListen) ExternalKey() string {
	return strconv.FormatInt(l.ListenedAt.Unix(), 10) + ":" + l.MSID
}

type lbListensResponse struct {
	Payload struct {
		Count   int `json:"count"`
		Listens []struct {
			ListenedAt    int64  `json:"listened_at"`
			RecordingMSID string `json:"recording_msid"`
			TrackMetadata struct {
				ArtistName     string `json:"artist_name"`
				TrackName      string `json:"track_name"`
				ReleaseName    string `json:"release_name"`
				AdditionalInfo struct {
					RecordingMBID string   `json:"recording_mbid"`
					ReleaseMBID   string   `json:"release_mbid"`
					ArtistMBIDs   []string `json:"artist_mbids"`
				} `json:"additional_info"`
				MBIDMapping *struct {
					RecordingMBID string `json:"recording_mbid"`
					ReleaseMBID   string `json:"release_mbid"`
					Artists       []struct {
						ArtistMBID       string `json:"artist_mbid"`
						ArtistCreditName string `json:"artist_credit_name"`
					} `json:"artists"`
					ArtistMBIDs []string `json:"artist_mbids"`
				} `json:"mbid_mapping"`
			} `json:"track_metadata"`
		} `json:"listens"`
	} `json:"payload"`
}

// ErrListenBrainzNotFound is a user name ListenBrainz does not know.
var ErrListenBrainzNotFound = errors.New("ListenBrainz has no such user")

// do sends one request, waiting out a rate limit: once when the server says the
// window is spent, and once more on a 429.
func (c *ListenBrainzClient) do(path string, q url.Values, dst any) error {
	u := c.BaseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequest("GET", u, nil)
		if err != nil {
			return err
		}
		if c.Token != "" {
			req.Header.Set("Authorization", "Token "+c.Token)
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return err
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}
		wait := time.Duration(0)
		if reset, err := strconv.Atoi(resp.Header.Get("X-RateLimit-Reset-In")); err == nil && reset > 0 {
			wait = time.Duration(reset) * time.Second
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt == 0 {
			if wait == 0 {
				wait = 10 * time.Second
			}
			listenBrainzSleep(wait)
			continue
		}
		switch {
		case resp.StatusCode == http.StatusNotFound:
			return ErrListenBrainzNotFound
		case resp.StatusCode == http.StatusUnauthorized:
			return errors.New("ListenBrainz refused the token")
		case resp.StatusCode != http.StatusOK:
			return fmt.Errorf("ListenBrainz %s -> %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
		}
		if remaining, err := strconv.Atoi(resp.Header.Get("X-RateLimit-Remaining")); err == nil && remaining <= 0 && wait > 0 {
			listenBrainzSleep(wait)
		}
		return json.Unmarshal(body, dst)
	}
}

// Validate checks the account can be read: the token, when set, is valid, and the
// user exists. It returns the user name ListenBrainz knows the token by.
func (c *ListenBrainzClient) Validate() (string, error) {
	if c.Username == "" {
		return "", errors.New("a ListenBrainz user name is required")
	}
	if c.Token != "" {
		var v struct {
			Valid    bool   `json:"valid"`
			UserName string `json:"user_name"`
		}
		if err := c.do("/1/validate-token", nil, &v); err != nil {
			return "", err
		}
		if !v.Valid {
			return "", errors.New("ListenBrainz says the token is not valid")
		}
		if !strings.EqualFold(v.UserName, c.Username) {
			return "", fmt.Errorf("the token belongs to %s, not %s", v.UserName, c.Username)
		}
	}
	var page lbListensResponse
	if err := c.do("/1/user/"+url.PathEscape(c.Username)+"/listens", url.Values{"count": {"1"}}, &page); err != nil {
		return "", err
	}
	return c.Username, nil
}

// ListensSince reads every listen after since, newest first, page by page, handing
// each page to store as it is read: a first read of a long history is stored as it
// goes, so a failure part-way keeps what was read (storing is idempotent by key).
// It pages backwards from now by max_ts, because ListenBrainz answers newest first.
//
// It returns the newest listen time seen, which is where the next pass starts.
func (c *ListenBrainzClient) ListensSince(since time.Time, store func([]ListenBrainzListen) error) (time.Time, error) {
	newest := since
	maxTS := time.Now().Add(time.Minute).Unix()
	for {
		q := url.Values{"count": {strconv.Itoa(listenBrainzPage)}, "max_ts": {strconv.FormatInt(maxTS, 10)}}
		if !since.IsZero() {
			q.Set("min_ts", strconv.FormatInt(since.Unix(), 10))
		}
		var page lbListensResponse
		if err := c.do("/1/user/"+url.PathEscape(c.Username)+"/listens", q, &page); err != nil {
			return newest, err
		}
		if len(page.Payload.Listens) == 0 {
			return newest, nil
		}
		out := make([]ListenBrainzListen, 0, len(page.Payload.Listens))
		oldest := maxTS
		for _, raw := range page.Payload.Listens {
			if raw.ListenedAt < oldest {
				oldest = raw.ListenedAt
			}
			at := time.Unix(raw.ListenedAt, 0).UTC()
			if !since.IsZero() && !at.After(since) {
				continue
			}
			md := raw.TrackMetadata
			l := ListenBrainzListen{
				ListenedAt:    at,
				MSID:          raw.RecordingMSID,
				Title:         md.TrackName,
				Artist:        md.ArtistName,
				Album:         md.ReleaseName,
				RecordingMBID: md.AdditionalInfo.RecordingMBID,
				ReleaseMBID:   md.AdditionalInfo.ReleaseMBID,
			}
			if m := md.MBIDMapping; m != nil {
				if m.RecordingMBID != "" {
					l.RecordingMBID = m.RecordingMBID
				}
				if m.ReleaseMBID != "" {
					l.ReleaseMBID = m.ReleaseMBID
				}
				for _, a := range m.Artists {
					if a.ArtistMBID != "" {
						l.Artists = append(l.Artists, ListenBrainzArtist{MBID: a.ArtistMBID, Name: a.ArtistCreditName})
					}
				}
				if len(l.Artists) == 0 {
					for _, id := range m.ArtistMBIDs {
						l.Artists = append(l.Artists, ListenBrainzArtist{MBID: id})
					}
				}
			}
			if len(l.Artists) == 0 {
				for _, id := range md.AdditionalInfo.ArtistMBIDs {
					l.Artists = append(l.Artists, ListenBrainzArtist{MBID: id})
				}
			}
			if at.After(newest) {
				newest = at
			}
			out = append(out, l)
		}
		if len(out) > 0 {
			if err := store(out); err != nil {
				return newest, err
			}
		}
		// A short page is the last one; so is a page that reached back to the cursor.
		if len(page.Payload.Listens) < listenBrainzPage || (!since.IsZero() && oldest <= since.Unix()) || oldest >= maxTS {
			return newest, nil
		}
		maxTS = oldest
	}
}

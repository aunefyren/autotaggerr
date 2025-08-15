package modules

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"golang.org/x/text/unicode/norm"
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

func canon(s string) string {
	return strings.ToLower(norm.NFC.String(strings.TrimSpace(s)))
}

func (p *PlexClient) FindArtistKey(sectionID, artistName string) (string, error) {
	var mc models.PlexMediaContainer
	path := fmt.Sprintf("/library/sections/%s/all?type=8&title=%s",
		sectionID, url.QueryEscape(artistName))
	if err := p.get(path, &mc); err != nil {
		return "", err
	}

	want := canon(artistName)
	for _, d := range mc.Directory {
		if d.Type == "artist" && canon(d.Title) == want {
			return d.Key, nil
		}
	}
	return "", fmt.Errorf("artist not found: %s", artistName)
}

func (p *PlexClient) FindAlbumKeyByArtist(artistKey, albumTitle string, year int) (string, error) {
	var mc models.PlexMediaContainer
	path := fmt.Sprintf("/library/metadata/%s/children", url.PathEscape(artistKey))
	if err := p.get(path, &mc); err != nil {
		return "", err
	}

	want := canon(albumTitle)
	for _, d := range mc.Directory {
		if d.Type != "album" {
			continue
		}
		if canon(d.Title) == want && (year == 0 || d.Year == year) {
			return d.Key, nil
		}
	}
	return "", fmt.Errorf("album not found: %s (%d)", albumTitle, year)
}

func (p *PlexClient) RefreshAlbum(albumKey string) error {
	return p.put(fmt.Sprintf("/library/metadata/%s/refresh?force=1", url.PathEscape(albumKey)))
}

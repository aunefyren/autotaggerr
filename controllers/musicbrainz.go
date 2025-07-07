package controllers

import (
	"fmt"
	"io"
	"net/http"
)

func queryMusicBrainzReleaseData(mbid string) ([]byte, error) {
	url := fmt.Sprintf("https://musicbrainz.org/ws/2/release/%s?inc=recordings+labels+artists+genres+tags&fmt=json", mbid)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	// Set User-Agent to comply with MB guidelines
	req.Header.Set("User-Agent", "MyMusicTagger/0.1 (my@email.com)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("MusicBrainz API returned status: %s", resp.Status)
	}

	return io.ReadAll(resp.Body)
}

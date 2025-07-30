package modules

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
)

var releaseCache = []models.MusicBrainzReleaseResponse{}

func GetMusicBrainzRelease(mbid string) (models.MusicBrainzReleaseResponse, error) {
	for _, release := range releaseCache {
		if strings.EqualFold(release.ID, mbid) {
			logger.Log.Debug("returning cached release")
			return release, nil
		}
	}

	release, err := QueryMusicBrainzReleaseData(mbid)
	if err != nil {
		logger.Log.Debug("failed to retrieve release from MB api. error: " + err.Error())
		return release, errors.New("failed to retrieve release from MB api")
	}

	return release, err
}

func QueryMusicBrainzReleaseData(mbid string) (models.MusicBrainzReleaseResponse, error) {
	var apiResponse models.MusicBrainzReleaseResponse

	url := fmt.Sprintf("https://musicbrainz.org/ws/2/release/%s?inc=recordings+labels+artists+genres+tags&fmt=json", mbid)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return apiResponse, err
	}

	// Set User-Agent to comply with MB guidelines
	req.Header.Set("User-Agent", "MyMusicTagger/0.1 (my@email.com)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return apiResponse, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return apiResponse, fmt.Errorf("MusicBrainz API returned status: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return apiResponse, err
	}

	err = json.Unmarshal(body, &apiResponse)
	if err != nil {
		logger.Log.Error("failed to parse Musicbrainz API response. error: " + err.Error())
		return apiResponse, errors.New("failed to parse Musicbrainz API response")
	}

	releaseCache = append(releaseCache, apiResponse)

	return apiResponse, nil
}

func MusicBrainzArtistsArrayToString(artists []models.ArtistCredit) string {
	artistString := ""
	for _, feature := range artists {
		artistString += feature.Name + feature.Joinphrase
	}

	return artistString
}

func MusicBrainzDateStringToDateTime(dateStr string) (time.Time, error) {
	// Go's time layout uses this reference date: "2006-01-02 15:04:05"
	layout := "2006-01-02"
	var parsedTime time.Time

	parsedTime, err := time.Parse(layout, dateStr)
	if err != nil {
		return parsedTime, err
	}

	return parsedTime, nil
}

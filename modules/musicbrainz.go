package modules

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
)

var (
	lastQueryTime                   time.Time
	queryMutex                      sync.Mutex
	rateLimit                       = time.Second
	musicbrainzReleaseCachePath     = "config/mb_releases.json"
	musicbrainzReleaseCacheDuration = 7 * 24 * time.Hour // 1 week
	musicbrainzReleaseCache         = map[string]models.CachedMusicBrainzRelease{}
)

// RateLimit wraps any API function and ensures at least 1s between executions
func RateLimit() error {
	queryMutex.Lock()
	defer queryMutex.Unlock()

	now := time.Now()
	elapsed := now.Sub(lastQueryTime)
	if elapsed < rateLimit {
		time.Sleep(rateLimit - elapsed)
	}

	lastQueryTime = time.Now()
	return nil
}

func GetMusicBrainzRelease(mbID string) (models.MusicBrainzReleaseResponse, error) {
	var release models.MusicBrainzReleaseResponse

	err := musicbrainzLoadCache()
	if err != nil {
		logger.Log.Error("failed to load release cache. error: " + err.Error())
		return release, errors.New("failed to load release cache")
	}

	if cached, ok := musicbrainzReleaseCache[mbID]; ok {
		if time.Since(cached.Timestamp) < musicbrainzReleaseCacheDuration {
			logger.Log.Debug("returning cached release for ID: " + mbID)
			return cached.Release, nil
		}
	}

	configFile, err := files.GetConfig()
	if err != nil {
		logger.Log.Error("failed to get config file. error: " + err.Error())
		return release, errors.New("failed to get config file")
	}

	release, err = QueryMusicBrainzReleaseData(mbID, configFile.AutotaggerrVersion)
	if err != nil {
		logger.Log.Debug("failed to retrieve release from MB api. error: " + err.Error())
		return release, errors.New("failed to retrieve release from MB api")
	}

	return release, err
}

func QueryMusicBrainzReleaseData(mbID string, autotaggerrVersion string) (models.MusicBrainzReleaseResponse, error) {
	var apiResponse models.MusicBrainzReleaseResponse

	// rate limit the request to comply
	err := RateLimit()
	if err != nil {
		logger.Log.Error("failed to rate limit. error: " + err.Error())
		return apiResponse, errors.New("failed to rate limit")
	}

	// do API request
	url := fmt.Sprintf("https://musicbrainz.org/ws/2/release/%s?inc=recordings+labels+artists+genres+tags&fmt=json", mbID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return apiResponse, err
	}

	// set User-Agent to comply with MB guidelines
	req.Header.Set("User-Agent", "Autotaggerr/"+autotaggerrVersion+" (https://github.com/aunefyren/autotaggerr)")

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

	musicbrainzLoadCache()
	musicbrainzReleaseCache[mbID] = models.CachedMusicBrainzRelease{
		Release:   apiResponse,
		Timestamp: time.Now(),
	}
	musicbrainzSaveCache()

	logger.Log.Trace(fmt.Sprintf("api response: %s", apiResponse))

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

func musicbrainzLoadCache() error {
	data, err := os.ReadFile(musicbrainzReleaseCachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No cache yet
		}
		return err
	}

	return json.Unmarshal(data, &musicbrainzReleaseCache)
}

func musicbrainzSaveCache() error {
	data, err := json.MarshalIndent(musicbrainzReleaseCache, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(musicbrainzReleaseCachePath, data, 0644)
}

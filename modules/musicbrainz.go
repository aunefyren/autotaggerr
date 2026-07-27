package modules

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
)

var (
	lastQueryTime time.Time
	queryMutex    sync.Mutex
	rateLimit     = time.Second
	// musicbrainzBaseURL is the API root; a package var so tests can point it at a
	// local httptest server instead of the live service.
	musicbrainzBaseURL              = "https://musicbrainz.org/ws/2"
	musicbrainzReleaseCachePath     = "config/mb_releases.json"
	musicbrainzReleaseCacheDuration = 7 * 24 * time.Hour // 1 week (base TTL)
	musicbrainzReleaseCacheJitter   = 7 * 24 * time.Hour // up to +1 week of jitter (7-14 days total)
	musicbrainzReleaseCache         = map[string]models.CachedMusicBrainzRelease{}
	musicbrainzReleaseCacheMu       sync.RWMutex
)

// musicbrainzCacheExpiry returns a jittered expiry time (base + [0, jitter))
// so entries fetched together during one scan don't all expire at once.
func musicbrainzCacheExpiry(now time.Time) time.Time {
	return now.Add(musicbrainzReleaseCacheDuration + time.Duration(rand.Int63n(int64(musicbrainzReleaseCacheJitter))))
}

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

	musicbrainzReleaseCacheMu.RLock()
	cached, ok := musicbrainzReleaseCache[mbID]
	musicbrainzReleaseCacheMu.RUnlock()
	if ok {
		// A zero ExpiresAt (pre-jitter cache entry) is treated as expired so it
		// gets refreshed once and upgraded to the new jittered format.
		if time.Now().Before(cached.ExpiresAt) {
			logger.Log.Debug("returning cached release for ID: " + mbID)
			return cached.Release, nil
		}
	}

	release, err := QueryMusicBrainzReleaseData(mbID, files.ConfigFile.AutotaggerrVersion)
	if err != nil {
		// propagate the real cause (HTTP status / transport / parse) instead of a
		// generic message, so the file-level log can tell them apart
		return release, err
	}

	return release, nil
}

// readBodySnippet reads a small, bounded portion of an error response body so it
// can be included in an error message without risking a huge/streaming read.
func readBodySnippet(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 512))
	return strings.TrimSpace(string(b))
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
	url := fmt.Sprintf("%s/release/%s?inc=recordings+labels+artists+genres+tags+release-groups+isrcs&fmt=json", musicbrainzBaseURL, mbID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		logger.Log.Error("failed to create new request. error: " + err.Error())
		return apiResponse, errors.New("failed to create new request")
	}

	// set User-Agent to comply with MB guidelines
	req.Header.Set("User-Agent", "Autotaggerr/"+autotaggerrVersion+" (https://github.com/aunefyren/autotaggerr)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// transport failure (DNS, timeout, connection reset) — usually transient
		return apiResponse, fmt.Errorf("MusicBrainz request failed for release %q (transport error, likely transient): %w", mbID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet := readBodySnippet(resp.Body)
		switch resp.StatusCode {
		case http.StatusNotFound, http.StatusGone:
			// The release ID does not exist on MusicBrainz — usually a stale/merged
			// ID that Lidarr is still holding on to.
			return apiResponse, fmt.Errorf("release %q not found on MusicBrainz (HTTP %d) — the Lidarr-assigned MB ID may be stale or merged: %s", mbID, resp.StatusCode, snippet)
		case http.StatusServiceUnavailable, http.StatusTooManyRequests:
			return apiResponse, fmt.Errorf("MusicBrainz throttled/unavailable for release %q (HTTP %d, transient — retry later): %s", mbID, resp.StatusCode, snippet)
		default:
			return apiResponse, fmt.Errorf("MusicBrainz returned HTTP %d for release %q: %s", resp.StatusCode, mbID, snippet)
		}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return apiResponse, fmt.Errorf("failed to read MusicBrainz response body for release %q: %w", mbID, err)
	}

	err = json.Unmarshal(body, &apiResponse)
	if err != nil {
		return apiResponse, fmt.Errorf("failed to parse MusicBrainz response for release %q: %w", mbID, err)
	}

	now := time.Now()
	musicbrainzReleaseCacheMu.Lock()
	musicbrainzReleaseCache[mbID] = models.CachedMusicBrainzRelease{
		Release:   apiResponse,
		Timestamp: now,
		ExpiresAt: musicbrainzCacheExpiry(now),
	}
	musicbrainzReleaseCacheMu.Unlock()

	// Persistence is batched (see cache.go); the in-memory map above is already
	// updated, so no need to write — and reload — the whole file on every fetch.
	markCacheDirty(cacheNameMusicbrainz)

	logger.Log.Trace(fmt.Sprintf("api response: %+v", apiResponse))

	return apiResponse, nil
}

func MusicBrainzArtistsArrayToString(artists []models.ArtistCredit, configFile models.ConfigStruct) string {
	artistString := ""
	for index, feature := range artists {
		logger.Log.Trace("processing featuring artist: " + feature.Artist.Name)

		// choose join phrase based on settings
		joinPhrase := configFile.AutotaggerrCustomArtistDelimiter
		if !configFile.AutotaggerrUseCustomArtistDelimiter {
			joinPhrase = feature.Joinphrase
		} else if index+1 == len(artists) {
			joinPhrase = ""
		} else if len(artists) > 2 && index+1 < len(artists)-1 && configFile.AutotaggerrCustomArtistDelimiterCommas {
			joinPhrase = ", "
		}

		logger.Log.Trace("feature join phrase to use: " + joinPhrase)

		// either use original release artist name or current name
		if configFile.AutotaggerrUseCurrentArtistName {
			artistString += feature.Artist.Name + joinPhrase
		} else {
			artistString += feature.Name + joinPhrase
		}
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

func init() {
	registerCache(cacheNameMusicbrainz, MusicbrainzSaveCache)
}

func MusicbrainzLoadCache() error {
	data, err := os.ReadFile(musicbrainzReleaseCachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No cache yet
		}
		return err
	}

	musicbrainzReleaseCacheMu.Lock()
	defer musicbrainzReleaseCacheMu.Unlock()
	return json.Unmarshal(data, &musicbrainzReleaseCache)
}

func MusicbrainzSaveCache() error {
	musicbrainzReleaseCacheMu.RLock()
	data, err := json.MarshalIndent(musicbrainzReleaseCache, "", "  ")
	musicbrainzReleaseCacheMu.RUnlock()
	if err != nil {
		return err
	}

	return os.WriteFile(musicbrainzReleaseCachePath, data, 0644)
}

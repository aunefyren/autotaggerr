// Package-level AcoustID support: fingerprint a file with fpcalc, ask AcoustID
// what it is, and rank the answers.
//
// The whole feature is detachable at three independent levels, and any one of them
// being off leaves the rest of Autotaggerr behaving exactly as it did before it
// existed:
//
//  1. an enabled `acoustid` DataSource row with an API key — no row, no feature;
//  2. `fpcalc` on PATH — missing means unavailable, logged once, never an error;
//  3. per-library opt-in (Library.UseAcoustID) — off by default.
//
// It identifies *recordings*, so it can never be a second resolution pipeline on
// its own: a recording appears on many releases, and picking the wrong one writes
// the wrong album into a file's tags. It therefore surfaces as a suggestion in the
// manual-attach UI, ranked and confidence-scored, and fails closed below the
// threshold rather than guessing.
package modules

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
)

const (
	acoustidBaseURL = "https://api.acoustid.org/v2"
	// fpcalcTimeout bounds one decode. A long file on slow storage takes a while;
	// a hung subprocess must never hold a request open indefinitely.
	fpcalcTimeout = 2 * time.Minute
	// acoustidRateLimit is this client's own limiter, deliberately separate from
	// the MusicBrainz one: they are different services with different budgets, and
	// sharing a limiter would make each one slower for no reason.
	acoustidRateLimit = 334 * time.Millisecond // ~3 req/s, AcoustID's documented ceiling
)

var (
	acoustidMu       sync.Mutex
	acoustidLastCall time.Time

	fpcalcOnce      sync.Once
	fpcalcPath      string
	fpcalcAvailable bool
)

// FpcalcAvailable reports whether the fingerprinting binary is installed. The
// result is looked up once and the absence logged once — an unavailable optional
// feature is a fact about the deployment, not a per-file error.
func FpcalcAvailable() bool {
	fpcalcOnce.Do(func() {
		path, err := exec.LookPath("fpcalc")
		if err != nil {
			logger.Log.Info("fpcalc not found on PATH; AcoustID fingerprinting is unavailable " +
				"(install chromaprint-tools to enable it)")
			return
		}
		fpcalcPath, fpcalcAvailable = path, true
	})
	return fpcalcAvailable
}

// AcoustIDFingerprint is what fpcalc produces for one file.
type AcoustIDFingerprint struct {
	Duration    int    `json:"duration"`
	Fingerprint string `json:"fingerprint"`
}

// Fingerprint runs fpcalc over a file. It decodes the entire file, so callers must
// go through the cache (LookupFile) rather than calling this per scan.
func Fingerprint(path string) (AcoustIDFingerprint, error) {
	if !FpcalcAvailable() {
		return AcoustIDFingerprint{}, errors.New("fpcalc is not installed, so files cannot be fingerprinted")
	}

	cmd := exec.Command(fpcalcPath, "-json", path)
	done := make(chan struct{})
	var (
		out []byte
		err error
	)
	go func() {
		out, err = cmd.Output()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(fpcalcTimeout):
		_ = cmd.Process.Kill()
		return AcoustIDFingerprint{}, fmt.Errorf("fingerprinting %s timed out", path)
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return AcoustIDFingerprint{}, fmt.Errorf("fpcalc failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return AcoustIDFingerprint{}, fmt.Errorf("fpcalc failed: %w", err)
	}

	var parsed AcoustIDFingerprint
	if err := json.Unmarshal(out, &parsed); err != nil {
		return AcoustIDFingerprint{}, fmt.Errorf("could not read fpcalc output: %w", err)
	}
	if parsed.Fingerprint == "" || parsed.Duration <= 0 {
		return AcoustIDFingerprint{}, fmt.Errorf("fpcalc produced no usable fingerprint for %s", path)
	}
	return parsed, nil
}

// acoustidRateLimit gate. Separate from RateLimit() by design; see the constant.
func acoustidThrottle() {
	acoustidMu.Lock()
	defer acoustidMu.Unlock()
	if elapsed := time.Since(acoustidLastCall); elapsed < acoustidRateLimit {
		time.Sleep(acoustidRateLimit - elapsed)
	}
	acoustidLastCall = time.Now()
}

// AcoustIDCandidate is one identification AcoustID offers for a fingerprint,
// flattened to what a human picking a track actually needs.
//
// Score is AcoustID's own fingerprint confidence (0..1) — how sure it is the audio
// matches this recording. It says nothing about *which release* the file is from,
// which is why PickAcoustIDMatch exists.
type AcoustIDCandidate struct {
	Score         float64 `json:"score"`
	RecordingMBID string  `json:"recording_mb_id"`
	Title         string  `json:"title"`
	Artist        string  `json:"artist"`
	Duration      int     `json:"duration"`

	ReleaseMBID  string `json:"release_mb_id"`
	ReleaseTitle string `json:"release_title"`
	ReleaseYear  int    `json:"release_year"`
	TrackCount   int    `json:"track_count"`
}

// acoustidResponse mirrors /v2/lookup with meta=recordings+releases.
type acoustidResponse struct {
	Status string `json:"status"`
	Error  struct {
		Message string `json:"message"`
	} `json:"error"`
	Results []struct {
		ID         string  `json:"id"`
		Score      float64 `json:"score"`
		Recordings []struct {
			ID       string `json:"id"`
			Title    string `json:"title"`
			Duration int    `json:"duration"`
			Artists  []struct {
				Name string `json:"name"`
			} `json:"artists"`
			Releases []struct {
				ID         string `json:"id"`
				Title      string `json:"title"`
				TrackCount int    `json:"track_count"`
				Date       struct {
					Year int `json:"year"`
				} `json:"date"`
			} `json:"releases"`
		} `json:"recordings"`
	} `json:"results"`
}

// LookupAcoustID asks AcoustID what a fingerprint is. One candidate is produced
// per (recording, release) pair, because that pair — not the recording alone — is
// what a file can actually be attached to.
func LookupAcoustID(apiKey, baseURL string, fp AcoustIDFingerprint) ([]AcoustIDCandidate, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, errors.New("the AcoustID data source has no API key")
	}
	if baseURL = strings.TrimSpace(baseURL); baseURL == "" {
		baseURL = acoustidBaseURL
	}

	acoustidThrottle()

	form := url.Values{}
	form.Set("client", apiKey)
	form.Set("duration", strconv.Itoa(fp.Duration))
	form.Set("fingerprint", fp.Fingerprint)
	form.Set("meta", "recordings+releases")

	// POST, not GET: a fingerprint is a few kilobytes and overflows URL limits.
	resp, err := http.PostForm(strings.TrimRight(baseURL, "/")+"/lookup", form)
	if err != nil {
		return nil, fmt.Errorf("AcoustID request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("AcoustID returned HTTP %d: %s", resp.StatusCode, snippet(body))
	}

	var parsed acoustidResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("could not parse the AcoustID response: %w", err)
	}
	if parsed.Status != "ok" {
		message := parsed.Error.Message
		if message == "" {
			message = parsed.Status
		}
		return nil, fmt.Errorf("AcoustID rejected the lookup: %s", message)
	}
	return flattenAcoustID(parsed), nil
}

// flattenAcoustID turns the nested response into one candidate per
// (recording, release). A recording with no releases still yields a candidate —
// it identifies the song, and the user can pick the release themselves.
func flattenAcoustID(parsed acoustidResponse) []AcoustIDCandidate {
	var out []AcoustIDCandidate
	for _, result := range parsed.Results {
		for _, rec := range result.Recordings {
			if rec.ID == "" {
				continue
			}
			artist := ""
			if len(rec.Artists) > 0 {
				artist = rec.Artists[0].Name
			}
			base := AcoustIDCandidate{
				Score: result.Score, RecordingMBID: rec.ID,
				Title: rec.Title, Artist: artist, Duration: rec.Duration,
			}
			if len(rec.Releases) == 0 {
				out = append(out, base)
				continue
			}
			for _, rel := range rec.Releases {
				candidate := base
				candidate.ReleaseMBID = rel.ID
				candidate.ReleaseTitle = rel.Title
				candidate.ReleaseYear = rel.Date.Year
				candidate.TrackCount = rel.TrackCount
				out = append(out, candidate)
			}
		}
	}
	return out
}

// IdentifyFile fingerprints a file (or reuses the cached fingerprint), looks it up
// at AcoustID, and returns the candidates ranked against the file's own folder.
//
// The cache is keyed by path and invalidated by size/mtime — the same identity
// rule scans use — because a full decode per file is the expensive part. A cached
// lookup costs no subprocess and no network call, which is what makes offering
// this from a UI button reasonable.
func IdentifyFile(path, apiKey, baseURL string, size int64, modTime time.Time) ([]RankedCandidate, error) {
	db := cacheDB
	var cached models.AcoustIDLookup
	fresh := false
	if db != nil {
		if err := db.First(&cached, "path = ?", path).Error; err == nil {
			fresh = cached.Size == size && cached.ModTime != nil && cached.ModTime.Equal(modTime)
		}
	}

	fp := AcoustIDFingerprint{Fingerprint: cached.Fingerprint, Duration: cached.Duration}
	if !fresh || fp.Fingerprint == "" {
		computed, err := Fingerprint(path)
		if err != nil {
			return nil, err
		}
		fp = computed
		cached = models.AcoustIDLookup{
			Path: path, Size: size, ModTime: &modTime,
			Fingerprint: fp.Fingerprint, Duration: fp.Duration,
			FetchedAt: time.Now(),
		}
		fresh = false
	}

	var candidates []AcoustIDCandidate
	if fresh && cached.LookedUpAt != nil {
		if err := json.Unmarshal([]byte(cached.Candidates), &candidates); err != nil {
			candidates = nil
		}
	}
	if candidates == nil {
		looked, err := LookupAcoustID(apiKey, baseURL, fp)
		if err != nil {
			return nil, err
		}
		candidates = looked

		if db != nil {
			payload, _ := json.Marshal(candidates)
			now := time.Now()
			cached.Candidates = string(payload)
			cached.LookedUpAt = &now
			cached.FetchedAt = now
			cached.Size, cached.ModTime = size, &modTime
			cached.Fingerprint, cached.Duration = fp.Fingerprint, fp.Duration
			if err := db.Save(&cached).Error; err != nil {
				logger.Log.Warnf("failed to cache the AcoustID lookup for %s: %s", path, err.Error())
			}
		}
	}

	hint := HintFromPath(path)
	hint.Tracks = AudioFilesInFolder(path)
	return PickAcoustIDMatch(candidates, hint), nil
}

func snippet(body []byte) string {
	const max = 200
	text := strings.TrimSpace(string(body))
	if len(text) > max {
		return text[:max] + "…"
	}
	return text
}

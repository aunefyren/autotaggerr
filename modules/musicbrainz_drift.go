package modules

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/models"
)

// hashRelease returns a stable content hash of a release, used to detect upstream
// changes. It hashes the whole payload, so any change trips it; the tag writer
// still diffs per file, so a change in an irrelevant field just yields no-op writes.
func hashRelease(r models.MusicBrainzReleaseResponse) string {
	b, err := json.Marshal(r)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// MusicbrainzDueForRefresh returns the IDs of cached releases whose TTL has
// elapsed — the set a drift sync should re-check. Bounding refreshes to expired
// entries keeps the sync within the MusicBrainz rate limit.
func MusicbrainzDueForRefresh() []string {
	now := time.Now()
	musicbrainzReleaseCacheMu.RLock()
	defer musicbrainzReleaseCacheMu.RUnlock()

	ids := make([]string, 0)
	for id, cached := range musicbrainzReleaseCache {
		if now.After(cached.ExpiresAt) {
			ids = append(ids, id)
		}
	}
	return ids
}

// RefreshMusicBrainzRelease force-fetches a release from MusicBrainz (updating the
// cache with a fresh copy and TTL) and reports whether its content changed since
// the previously cached version.
func RefreshMusicBrainzRelease(mbID string) (models.MusicBrainzReleaseResponse, bool, error) {
	musicbrainzReleaseCacheMu.RLock()
	previous, hadPrevious := musicbrainzReleaseCache[mbID]
	musicbrainzReleaseCacheMu.RUnlock()

	oldHash := ""
	if hadPrevious {
		oldHash = hashRelease(previous.Release)
	}

	fresh, err := QueryMusicBrainzReleaseData(mbID, files.ConfigFile.AutotaggerrVersion)
	if err != nil {
		return fresh, false, err
	}

	changed := hadPrevious && oldHash != hashRelease(fresh)
	return fresh, changed, nil
}

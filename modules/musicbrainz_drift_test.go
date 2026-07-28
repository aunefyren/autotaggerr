package modules

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

func TestRefreshDetectsUpstreamChange(t *testing.T) {
	var version int32 = 1
	withMockMB(t, func(w http.ResponseWriter, _ *http.Request) {
		title := "OK Computer"
		if atomic.LoadInt32(&version) == 2 {
			title = "OK Computer (Remastered)"
		}
		_ = json.NewEncoder(w).Encode(models.MusicBrainzReleaseResponse{ID: "rel-1", Title: title})
	})

	// Prime the cache.
	if _, err := GetMusicBrainzRelease("rel-1"); err != nil {
		t.Fatalf("prime: %v", err)
	}

	// Re-fetching identical data is not a change.
	if _, changed, err := RefreshMusicBrainzRelease("rel-1"); err != nil || changed {
		t.Errorf("identical refresh: changed=%v err=%v (want false/nil)", changed, err)
	}

	// Upstream edit -> change detected.
	atomic.StoreInt32(&version, 2)
	if _, changed, err := RefreshMusicBrainzRelease("rel-1"); err != nil || !changed {
		t.Errorf("edited refresh: changed=%v err=%v (want true/nil)", changed, err)
	}
}

func TestDueForRefresh(t *testing.T) {
	withMockMB(t, func(http.ResponseWriter, *http.Request) {}) // resets the cache

	musicbrainzReleaseCacheMu.Lock()
	musicbrainzReleaseCache["expired"] = models.CachedMusicBrainzRelease{ExpiresAt: time.Now().Add(-time.Hour)}
	musicbrainzReleaseCache["fresh"] = models.CachedMusicBrainzRelease{ExpiresAt: time.Now().Add(time.Hour)}
	musicbrainzReleaseCacheMu.Unlock()

	due := MusicbrainzDueForRefresh()
	if len(due) != 1 || due[0] != "expired" {
		t.Errorf("due = %v, want [expired]", due)
	}
}

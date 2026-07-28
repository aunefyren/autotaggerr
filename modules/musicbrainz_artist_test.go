package modules

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

func TestGetMusicBrainzArtistReleaseGroups(t *testing.T) {
	withMockMB(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(models.MusicBrainzArtistReleaseGroups{
			Count: 2,
			ReleaseGroups: []models.MusicBrainzArtistReleaseGroup{
				{ID: "rg-1", Title: "Debut", PrimaryType: "Album"},
				{ID: "rg-2", Title: "Live at X", PrimaryType: "Album", SecondaryTypes: []string{"Live"}},
			},
		})
	})

	rgs, err := GetMusicBrainzArtistReleaseGroups("art-1")
	if err != nil {
		t.Fatalf("GetMusicBrainzArtistReleaseGroups: %v", err)
	}
	if len(rgs) != 2 || rgs[0].ID != "rg-1" || rgs[1].SecondaryTypes[0] != "Live" {
		t.Errorf("parsed release-groups = %+v", rgs)
	}
}

func TestCachedRelease(t *testing.T) {
	withMockMB(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(models.MusicBrainzReleaseResponse{ID: "rel-x", Title: "Cached"})
	})

	if _, ok := CachedRelease("rel-x"); ok {
		t.Error("release should not be cached before fetch")
	}
	if _, err := GetMusicBrainzRelease("rel-x"); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	rel, ok := CachedRelease("rel-x")
	if !ok || rel.Title != "Cached" {
		t.Errorf("CachedRelease after fetch = %+v ok=%v", rel, ok)
	}
}

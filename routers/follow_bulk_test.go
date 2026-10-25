package routers

import (
	"net/http"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// Bulk follow writes the settings for every artist Autotaggerr governs, names the ones
// a manager governs instead of following them, and refuses a cutoff that is a typo.
func TestFollowArtistsInBulk(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	api.DB.Create(&models.CollectionArtist{MBID: "native", Name: "Native", ManagedBy: models.ManagedByAutotaggerr})
	api.DB.Create(&models.CollectionArtist{MBID: "lidarr", Name: "Under Lidarr", ManagedBy: models.ManagedByLidarr})

	if w := do(r, "POST", "/api/v1/collection/follow", token, map[string]any{"artist_mb_ids": []string{"native"}, "follow_from_year": 20}); w.Code != http.StatusBadRequest {
		t.Errorf("a typo of a cutoff = %d, want 400", w.Code)
	}
	if w := do(r, "POST", "/api/v1/collection/follow", token, map[string]any{}); w.Code != http.StatusBadRequest {
		t.Errorf("no artists = %d, want 400", w.Code)
	}

	w := do(r, "POST", "/api/v1/collection/follow", token, map[string]any{
		"artist_mb_ids": []string{"native", "lidarr"}, "follow_types": "Album", "follow_from_year": 2024,
	})
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), `"followed":1`) || !strings.Contains(w.Body.String(), "Under Lidarr") {
		t.Fatalf("bulk follow = %d %s", w.Code, w.Body.String())
	}
	var native, lidarr models.CollectionArtist
	api.DB.Where("mb_id = ?", "native").First(&native)
	api.DB.Where("mb_id = ?", "lidarr").First(&lidarr)
	if !native.Monitored || native.FollowTypes != "Album" || native.FollowFromYear != 2024 {
		t.Errorf("native after bulk follow = %+v", native)
	}
	if lidarr.Monitored {
		t.Error("an artist Lidarr governs was followed")
	}
	api.Scan.Wait()
}

func TestCollectionPolicyRoutes(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)
	policy := decodeJSON[map[string]any](t, r, "GET", "/api/v1/collection/policy", token, nil)
	if policy["new_artist_follow"] != models.NewArtistFollowOff {
		t.Errorf("default policy = %v", policy)
	}
	if w := do(r, "PUT", "/api/v1/collection/policy", token, map[string]any{"new_artist_follow": "maybe"}); w.Code != http.StatusBadRequest {
		t.Errorf("an unknown policy = %d, want 400", w.Code)
	}
	saved := decodeJSON[map[string]any](t, r, "PUT", "/api/v1/collection/policy", token, map[string]any{"new_artist_follow": "new", "follow_types": "Album,EP"})
	if saved["new_artist_follow"] != "new" || saved["follow_types"] != "Album,EP" {
		t.Errorf("saved policy = %v", saved)
	}
}

// Changing what an unfollowed artist's follow would want changes what counts as their
// albums, so the catalogue count follows without anything being stored.
func TestFollowSettingsRecountTheCatalogue(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	api.Meta = &fakeMeta{getArtistRGs: func(string) ([]models.MusicBrainzArtistReleaseGroup, bool, error) {
		return []models.MusicBrainzArtistReleaseGroup{{ID: "a", PrimaryType: "Album"}, {ID: "s", PrimaryType: "Single"}}, true, nil
	}}
	api.DB.Create(&models.CollectionArtist{MBID: "art", Name: "Art", ManagedBy: models.ManagedByAutotaggerr})
	body := decodeJSON[map[string]any](t, r, "POST", "/api/v1/artists/art/follow", token, map[string]any{"follow_types": "Album,Single"})
	artist, _ := body["artist"].(map[string]any)
	if artist["catalogue_count"] != float64(2) {
		t.Errorf("catalogue after widening the shape = %v", artist["catalogue_count"])
	}
	var stored int64
	api.DB.Model(&models.CollectionReleaseGroup{}).Count(&stored)
	if stored != 0 {
		t.Errorf("%d albums stored for an artist nobody follows", stored)
	}
}

package routers

import (
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// TestGetArtistExposesIdentityEditable checks the artist detail carries identity_editable
// so the UI can hide the attach / want controls without re-deriving the lidarr||mixed rule.
func TestGetArtistExposesIdentityEditable(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	artistFixture(t, api.DB, "native-art", "Native", models.ManagedByAutotaggerr, false)
	artistFixture(t, api.DB, "lidarr-art", "Lidarr", models.ManagedByLidarr, false)

	type resp struct {
		Artist struct {
			IdentityEditable bool `json:"identity_editable"`
		} `json:"artist"`
	}

	native := decodeJSON[resp](t, r, "GET", "/api/v1/artists/native-art", token, nil)
	if !native.Artist.IdentityEditable {
		t.Error("native artist should be identity_editable")
	}
	lidarr := decodeJSON[resp](t, r, "GET", "/api/v1/artists/lidarr-art", token, nil)
	if lidarr.Artist.IdentityEditable {
		t.Error("lidarr-managed artist must not be identity_editable")
	}
}

// TestListLibraryItemsExposesIdentityEditable checks each item row reports whether its
// file's identity may be set by hand, resolved from the library's manager.
func TestListLibraryItemsExposesIdentityEditable(t *testing.T) {
	type resp struct {
		Items []struct {
			Path             string `json:"path"`
			IdentityEditable bool   `json:"identity_editable"`
		} `json:"items"`
	}

	t.Run("native library is editable", func(t *testing.T) {
		r, api := setupAPI(t)
		token := loginToken(t, r)
		// No managers configured, so the library resolves to the native default.
		lib := models.Library{Name: "N", Path: "/m", Enabled: true}
		if err := api.DB.Create(&lib).Error; err != nil {
			t.Fatalf("library: %v", err)
		}
		if err := api.DB.Create(&models.LibraryItem{LibraryID: lib.ID, Path: "/m/A/Al/01.flac", Status: models.LibraryItemStatusUnmatched}).Error; err != nil {
			t.Fatalf("item: %v", err)
		}
		out := decodeJSON[resp](t, r, "GET", "/api/v1/library-items", token, nil)
		if len(out.Items) != 1 || !out.Items[0].IdentityEditable {
			t.Errorf("native item should be identity_editable, got %+v", out.Items)
		}
	})

	t.Run("lidarr library is not editable", func(t *testing.T) {
		r, api := setupAPI(t)
		token := loginToken(t, r)
		mgr := models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true, LidarrBaseURL: "http://x", LidarrAPIKey: "k"}
		if err := api.DB.Create(&mgr).Error; err != nil {
			t.Fatalf("manager: %v", err)
		}
		lib := models.Library{Name: "L", Path: "/m", Enabled: true, ManagerID: &mgr.ID}
		if err := api.DB.Create(&lib).Error; err != nil {
			t.Fatalf("library: %v", err)
		}
		if err := api.DB.Create(&models.LibraryItem{LibraryID: lib.ID, Path: "/m/A/Al/01.flac", Status: models.LibraryItemStatusUnmatched}).Error; err != nil {
			t.Fatalf("item: %v", err)
		}
		out := decodeJSON[resp](t, r, "GET", "/api/v1/library-items", token, nil)
		if len(out.Items) != 1 || out.Items[0].IdentityEditable {
			t.Errorf("lidarr item must not be identity_editable, got %+v", out.Items)
		}
	})
}

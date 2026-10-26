package routers

import (
	"errors"
	"net/http"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// TestDetachedArtistFilesAreAttachable: detaching an artist takes every one of its
// libraries native, so its files in a Lidarr library may be attached by hand — the
// listing says so, and the gate lets the attach through. A file of an artist Lidarr
// still governs, in the same library, stays refused.
func TestDetachedArtistFilesAreAttachable(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	api.Meta = &fakeMeta{getRelease: func(string) (models.MusicBrainzReleaseResponse, error) {
		return models.MusicBrainzReleaseResponse{}, errors.New("down")
	}}

	managed := lidarrLibrary(t, api.DB)
	detached := models.LibraryItem{LibraryID: managed.LibraryID, Path: "/l/D/E/01.flac", MBReleaseID: "rel-d", Status: models.LibraryItemStatusOK}
	governed := models.LibraryItem{LibraryID: managed.LibraryID, Path: "/l/G/H/01.flac", MBReleaseID: "rel-g", Status: models.LibraryItemStatusOK}
	for _, row := range []any{
		&detached, &governed,
		&models.CollectionArtist{MBID: "art-d", Name: "D", ManagedBy: models.ManagedByAutotaggerr, ManagerDetached: true},
		&models.CollectionArtist{MBID: "art-g", Name: "G", ManagedBy: models.ManagedByLidarr},
		&models.CollectionRelease{MBID: "rel-d", ReleaseGroupMBID: "rg-d", ArtistMBID: "art-d"},
		&models.CollectionRelease{MBID: "rel-g", ReleaseGroupMBID: "rg-g", ArtistMBID: "art-g"},
	} {
		if err := api.DB.Create(row).Error; err != nil {
			t.Fatalf("seed %T: %v", row, err)
		}
	}

	got := decodeJSON[struct {
		Items []struct {
			Path             string `json:"path"`
			IdentityEditable bool   `json:"identity_editable"`
		} `json:"items"`
	}](t, r, "GET", "/api/v1/library-items", token, nil)
	want := map[string]bool{managed.Path: false, detached.Path: true, governed.Path: false}
	if len(got.Items) != len(want) {
		t.Fatalf("items = %+v, want %d", got.Items, len(want))
	}
	for _, it := range got.Items {
		if it.IdentityEditable != want[it.Path] {
			t.Errorf("%s: identity_editable = %v, want %v", it.Path, it.IdentityEditable, want[it.Path])
		}
	}

	body := map[string]any{"mb_release_id": "rel-new", "mb_release_track_id": "t1"}
	w := do(r, "POST", "/api/v1/library-items/"+governed.ID.String()+"/attach", token, body)
	expectStatus(t, "governed", w.Code, w.Body.String(), http.StatusConflict, "managed by Lidarr")
	// Past the gate, the attach goes on to fetch the release — which the fake refuses.
	w = do(r, "POST", "/api/v1/library-items/"+detached.ID.String()+"/attach", token, body)
	expectStatus(t, "detached", w.Code, w.Body.String(), http.StatusBadGateway, "could not load that release")
}

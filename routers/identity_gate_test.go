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

// TestUnidentifiedFileAttachesToADetachedArtist: a file in a Lidarr library with no
// identity has no edition to read an artist from, so the gate reads it from the
// release being attached. A detached artist's release is let through; a release of an
// artist Lidarr governs is not. The listing offers the attach once any artist is
// detached, and only for files without an identity.
func TestUnidentifiedFileAttachesToADetachedArtist(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	releaseOf := func(id, artist string) models.MusicBrainzReleaseResponse {
		var rel models.MusicBrainzReleaseResponse
		rel.ID = id
		rel.ArtistCredit = []models.ArtistCredit{{Name: artist}}
		rel.ArtistCredit[0].Artist.ID = artist
		return rel
	}
	fetched := 0
	api.Meta = &fakeMeta{getRelease: func(id string) (models.MusicBrainzReleaseResponse, error) {
		fetched++
		switch id {
		case "rel-d":
			return releaseOf(id, "art-d"), nil
		case "rel-g":
			return releaseOf(id, "art-g"), nil
		}
		return models.MusicBrainzReleaseResponse{}, errors.New("unknown release")
	}}

	unidentified := lidarrLibrary(t, api.DB)
	listing := func() bool {
		t.Helper()
		got := decodeJSON[struct {
			Items []struct {
				AttachIfDetached bool `json:"attach_if_detached"`
			} `json:"items"`
		}](t, r, "GET", "/api/v1/library-items", token, nil)
		return len(got.Items) == 1 && got.Items[0].AttachIfDetached
	}
	if listing() {
		t.Error("offered an attach with no artist detached")
	}
	for _, row := range []any{
		&models.CollectionArtist{MBID: "art-d", Name: "D", ManagedBy: models.ManagedByAutotaggerr, ManagerDetached: true},
		&models.CollectionArtist{MBID: "art-g", Name: "G", ManagedBy: models.ManagedByLidarr},
	} {
		if err := api.DB.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if !listing() {
		t.Error("an unidentified Lidarr file was not offered for a detached artist")
	}

	url := "/api/v1/library-items/" + unidentified.ID.String() + "/attach"
	w := do(r, "POST", url, token, map[string]any{"mb_release_id": "rel-g", "mb_release_track_id": "t1"})
	expectStatus(t, "governed target", w.Code, w.Body.String(), http.StatusConflict, "managed by Lidarr")
	// Past the gate, the attach validates the track — which this release does not have.
	w = do(r, "POST", url, token, map[string]any{"mb_release_id": "rel-d", "mb_release_track_id": "t1"})
	expectStatus(t, "detached target", w.Code, w.Body.String(), http.StatusBadRequest, "not part of that release")
	if fetched != 2 {
		t.Errorf("release fetched %d times, want once per attach", fetched)
	}
}

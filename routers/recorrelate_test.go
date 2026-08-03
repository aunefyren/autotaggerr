package routers

import (
	"net/http"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
)

// The force re-correlate endpoints resolve their scope before responding, so the
// "unknown" and "nothing owned" answers are immediate and deterministic — no worker run.
func TestRecorrelateReleaseGroupUnknown(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)
	if w := do(r, "POST", "/api/v1/release-groups/nope/recorrelate", token, nil); w.Code != http.StatusNotFound {
		t.Errorf("unknown release-group = %d, want 404: %s", w.Code, w.Body.String())
	}
}

func TestRecorrelateReleaseGroupNothingOwned(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	// The release-group exists but no files are owned of it, so there is nothing to walk.
	if err := api.DB.Create(&models.CollectionReleaseGroup{MBID: "rg-1", ArtistMBID: "art", Title: "Album"}).Error; err != nil {
		t.Fatalf("create rg: %v", err)
	}
	if w := do(r, "POST", "/api/v1/release-groups/rg-1/recorrelate", token, nil); w.Code != http.StatusConflict {
		t.Errorf("release-group with no files = %d, want 409: %s", w.Code, w.Body.String())
	}
}

func TestRecorrelateLibraryUnknown(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)
	if w := do(r, "POST", "/api/v1/libraries/"+uuid.NewString()+"/recorrelate", token, nil); w.Code != http.StatusNotFound {
		t.Errorf("unknown library = %d, want 404: %s", w.Code, w.Body.String())
	}
}

func TestRecorrelateArtistUnknown(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)
	if w := do(r, "POST", "/api/v1/artists/nope/recorrelate", token, nil); w.Code != http.StatusNotFound {
		t.Errorf("unknown artist = %d, want 404: %s", w.Code, w.Body.String())
	}
}

func TestRecorrelateArtistNothingToScan(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	// The artist exists but owns no files, so the scope resolves to nothing to walk.
	if err := api.DB.Create(&models.CollectionArtist{MBID: "artist-1", Name: "Nobody"}).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	if w := do(r, "POST", "/api/v1/artists/artist-1/recorrelate", token, nil); w.Code != http.StatusConflict {
		t.Errorf("artist with no files = %d, want 409: %s", w.Code, w.Body.String())
	}
}

// The success paths enqueue a forced re-walk. setupAPI now waits for the runner on
// cleanup, so the background job cannot outlive the test.
func TestRecorrelateArtistStarts(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	mbid := seedArtistWithFile(t, api, t.TempDir())

	if w := do(r, "POST", "/api/v1/artists/"+mbid+"/recorrelate", token, nil); w.Code != http.StatusAccepted {
		t.Errorf("recorrelate = %d, want 202: %s", w.Code, w.Body.String())
	}
}

func TestRecorrelateLibraryStarts(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	lib := models.Library{Name: "L", Path: t.TempDir(), Enabled: true}
	if err := api.DB.Create(&lib).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	if w := do(r, "POST", "/api/v1/libraries/"+lib.ID.String()+"/recorrelate", token, nil); w.Code != http.StatusAccepted {
		t.Errorf("recorrelate library = %d, want 202: %s", w.Code, w.Body.String())
	}
}

func TestRecorrelateEndpointsRequireAuth(t *testing.T) {
	r, _ := setupAPI(t)
	for _, path := range []string{
		"/api/v1/artists/artist-1/recorrelate",
		"/api/v1/release-groups/rg-1/recorrelate",
		"/api/v1/libraries/" + uuid.NewString() + "/recorrelate",
	} {
		if w := do(r, "POST", path, "", nil); w.Code != http.StatusUnauthorized {
			t.Errorf("%s without token = %d, want 401", path, w.Code)
		}
	}
}

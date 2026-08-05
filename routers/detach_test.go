package routers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// The detach endpoints over the wire. The collection package owns what detaching
// means; these cover the seam — that the flags the UI renders its controls from come
// back on the response, and that a stale page gets a conflict rather than a silent
// no-op.

func seedManagedArtist(t *testing.T, api *API) {
	t.Helper()
	if err := api.DB.Create(&models.CollectionArtist{
		MBID: "art-1", Name: "Band", ManagedBy: models.ManagedByLidarr, Monitored: true,
	}).Error; err != nil {
		t.Fatalf("artist: %v", err)
	}
	if err := api.DB.Create(&models.CollectionDesire{
		ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", ReleaseMBID: "rel-1",
		Source: models.DesireSourceManager,
	}).Error; err != nil {
		t.Fatalf("desire: %v", err)
	}
}

// detachResponse is the shape the artist page reads back.
type detachResponse struct {
	Artist struct {
		ManagedBy        string `json:"managed_by"`
		ManagerDetached  bool   `json:"manager_detached"`
		Detachable       bool   `json:"detachable"`
		FollowGoverns    bool   `json:"follow_governs"`
		IdentityEditable bool   `json:"identity_editable"`
		Monitored        bool   `json:"monitored"`
	} `json:"artist"`
	WantsKept     int  `json:"wants_kept"`
	FollowCleared bool `json:"follow_cleared"`
}

// TestDetachArtistReturnsTheNewAuthority: the page re-renders from this response, so
// every flag its controls depend on has to be in it and has to be consistent — a
// detached artist that still reported follow_governs false would leave the follow
// toggle frozen with nothing on the page able to unfreeze it.
func TestDetachArtistReturnsTheNewAuthority(t *testing.T) {
	r, api := setupAPI(t)
	seedManagedArtist(t, api)
	token := loginToken(t, r)

	w := do(r, "POST", "/api/v1/artists/art-1/detach", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}

	var got detachResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.WantsKept != 1 {
		t.Errorf("wants_kept = %d, want 1", got.WantsKept)
	}
	if !got.FollowCleared {
		t.Errorf("follow_cleared = false, want true")
	}
	if got.Artist.ManagedBy != models.ManagedByAutotaggerr || !got.Artist.ManagerDetached {
		t.Errorf("artist = {managed_by:%q detached:%v}, want autotaggerr/true", got.Artist.ManagedBy, got.Artist.ManagerDetached)
	}
	if !got.Artist.FollowGoverns || !got.Artist.IdentityEditable {
		t.Errorf("gates still closed: follow_governs=%v identity_editable=%v", got.Artist.FollowGoverns, got.Artist.IdentityEditable)
	}
	if got.Artist.Detachable {
		t.Error("detachable = true after detaching, want false so the button becomes Reattach")
	}
	if got.Artist.Monitored {
		t.Error("monitored = true, want following switched off")
	}
}

// TestDetachNativeArtistConflicts: 409, not 400 — the request is well-formed and was
// true of an earlier state of the artist. The page is simply out of date, and the
// status is what tells it to reload rather than to blame the input.
func TestDetachNativeArtistConflicts(t *testing.T) {
	r, api := setupAPI(t)
	seedArtist(t, api)
	token := loginToken(t, r)

	w := do(r, "POST", "/api/v1/artists/art-1/detach", token, nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
	}
}

func TestDetachUnknownArtistIsNotFound(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)

	w := do(r, "POST", "/api/v1/artists/ghost/detach", token, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body.String())
	}
}

// TestReattachArtistClearsTheOverride: DELETE is the undo, and with no libraries to
// re-derive from the artist falls to native — which is the honest answer for an
// artist whose files are nowhere, not a failure.
func TestReattachArtistClearsTheOverride(t *testing.T) {
	r, api := setupAPI(t)
	seedManagedArtist(t, api)
	token := loginToken(t, r)

	if w := do(r, "POST", "/api/v1/artists/art-1/detach", token, nil); w.Code != http.StatusOK {
		t.Fatalf("detach: %d %s", w.Code, w.Body.String())
	}
	w := do(r, "DELETE", "/api/v1/artists/art-1/detach", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}

	var artist models.CollectionArtist
	if err := api.DB.Where("mb_id = ?", "art-1").First(&artist).Error; err != nil {
		t.Fatalf("artist: %v", err)
	}
	if artist.ManagerDetached {
		t.Error("manager_detached = true after reattaching")
	}

	// The kept want stays the user's own — reattaching is deliberately not an inverse.
	var desire models.CollectionDesire
	if err := api.DB.Where("release_group_mb_id = ?", "rg-1").First(&desire).Error; err != nil {
		t.Fatalf("desire: %v", err)
	}
	if desire.Source != models.DesireSourceManual {
		t.Errorf("want source = %q, want manual", desire.Source)
	}
}

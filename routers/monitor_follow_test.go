package routers

import (
	"net/http"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// TestSetArtistMonitoredUnknown: monitoring an artist not in the collection is a 404.
func TestSetArtistMonitoredUnknown(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)
	if w := do(r, "POST", "/api/v1/artists/nope/monitor", token, map[string]any{"monitored": true}); w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body.String())
	}
}

// TestSetArtistMonitoredInvalidBody: a malformed body is a 400.
func TestSetArtistMonitoredInvalidBody(t *testing.T) {
	r, api := setupAPI(t)
	seedArtist(t, api)
	token := loginToken(t, r)
	if w := do(r, "POST", "/api/v1/artists/art-1/monitor", token, "not-an-object"); w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
}

// TestSetArtistMonitoredSyncsWhenEnabled: enabling monitoring syncs the discography
// through the metadata source. With a fake source this runs with zero network.
func TestSetArtistMonitoredSyncsWhenEnabled(t *testing.T) {
	r, api := setupAPI(t)
	seedArtist(t, api)
	token := loginToken(t, r)
	api.Meta = &fakeMeta{} // hermetic SyncArtist: no groups, no error

	w := do(r, "POST", "/api/v1/artists/art-1/monitor", token, map[string]any{"monitored": true})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var artist models.CollectionArtist
	if err := api.DB.Where("mb_id = ?", "art-1").First(&artist).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !artist.Monitored {
		t.Error("artist was not marked monitored")
	}
}

// TestUpdateFollowUpdatesSettings: the follow endpoint persists the settings and, when
// monitoring is on, syncs — again hermetic under a fake source.
func TestUpdateFollowUpdatesSettings(t *testing.T) {
	r, api := setupAPI(t)
	seedArtist(t, api)
	token := loginToken(t, r)
	api.Meta = &fakeMeta{}

	w := do(r, "POST", "/api/v1/artists/art-1/follow", token, map[string]any{
		"monitored": true, "follow_types": "Album,EP", "follow_secondary": true,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var artist models.CollectionArtist
	if err := api.DB.Where("mb_id = ?", "art-1").First(&artist).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if artist.FollowTypes != "Album,EP" || !artist.FollowSecondary || !artist.Monitored {
		t.Errorf("follow settings not saved: %+v", artist)
	}
}

// TestUpdateFollowUnknownArtist: following an unknown artist is a 404.
func TestUpdateFollowUnknownArtist(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)
	if w := do(r, "POST", "/api/v1/artists/nope/follow", token, map[string]any{"monitored": true}); w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body.String())
	}
}

// TestScanCollection: the collection-wide Scan endpoint accepts the request.
func TestScanCollection(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)
	w := do(r, "POST", "/api/v1/scan", token, nil)
	if w.Code != http.StatusOK && w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 200/202: %s", w.Code, w.Body.String())
	}
}

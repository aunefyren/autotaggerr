package routers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aunefyren/autotaggerr/collection"
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

// TestUpdateFollowSavesAndClearsTheYearCutoff: the cutoff round-trips, and zero is a
// real value rather than "unset" — it is how the cutoff is cleared, so a handler that
// treated it as absent would make the control one-way.
func TestUpdateFollowSavesAndClearsTheYearCutoff(t *testing.T) {
	r, api := setupAPI(t)
	seedArtist(t, api)
	token := loginToken(t, r)
	api.Meta = &fakeMeta{}

	post := func(year int) {
		t.Helper()
		w := do(r, "POST", "/api/v1/artists/art-1/follow", token, map[string]any{"follow_from_year": year})
		if w.Code != http.StatusOK {
			t.Fatalf("follow_from_year=%d: status = %d, want 200: %s", year, w.Code, w.Body.String())
		}
	}
	stored := func() int {
		t.Helper()
		var artist models.CollectionArtist
		if err := api.DB.Where("mb_id = ?", "art-1").First(&artist).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		return artist.FollowFromYear
	}

	post(2020)
	if got := stored(); got != 2020 {
		t.Errorf("follow_from_year = %d, want 2020", got)
	}
	post(0)
	if got := stored(); got != 0 {
		t.Errorf("follow_from_year = %d after clearing, want 0", got)
	}
}

// TestUpdateFollowRejectsAnImpossibleYear: a typo in the year would quietly want
// nothing (or everything) for good, and nothing on the page would say why the missing
// list emptied. Rejecting it is the only feedback there can be.
func TestUpdateFollowRejectsAnImpossibleYear(t *testing.T) {
	r, api := setupAPI(t)
	seedArtist(t, api)
	token := loginToken(t, r)
	api.Meta = &fakeMeta{}

	for _, year := range []int{1899, 2201, 20240, -5} {
		w := do(r, "POST", "/api/v1/artists/art-1/follow", token, map[string]any{"follow_from_year": year})
		if w.Code != http.StatusBadRequest {
			t.Errorf("follow_from_year=%d: status = %d, want 400: %s", year, w.Code, w.Body.String())
		}
	}

	// Nothing was written by any of them.
	var artist models.CollectionArtist
	if err := api.DB.Where("mb_id = ?", "art-1").First(&artist).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if artist.FollowFromYear != 0 {
		t.Errorf("a rejected year was stored anyway: %d", artist.FollowFromYear)
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

	// A scan with nothing indexed says so rather than reporting a bare zero, which is
	// what the page shows instead of "Scanned — 0 artists, 0 albums".
	var body struct {
		EmptyReason string `json:"empty_reason"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode scan response: %v", err)
	}
	if body.EmptyReason != collection.ScanEmptyNoFiles {
		t.Errorf("empty_reason = %q, want %q", body.EmptyReason, collection.ScanEmptyNoFiles)
	}
}

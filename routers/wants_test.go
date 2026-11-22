package routers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

func wantBody(artist, rg string, picks ...map[string]any) map[string]any {
	if picks == nil {
		picks = []map[string]any{}
	}
	return map[string]any{
		"artist":        map[string]string{"mb_id": artist, "name": "Band"},
		"release_group": map[string]string{"mb_id": rg, "title": "Album", "primary_type": "Album"},
		"picks":         picks,
	}
}

// TestAddWantOneTrackOfOneEdition: *Add album* with one edition narrowed to one
// track adds the artist and stores exactly that want.
func TestAddWantOneTrackOfOneEdition(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	w := do(r, "POST", "/api/v1/wants", token, wantBody("art-new", "rg-1",
		map[string]any{"release_mb_id": "rel-15", "recording_mb_ids": []string{"rec-3"}}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp struct {
		ArtistAdded bool                      `json:"artist_added"`
		Desires     []models.CollectionDesire `json:"desires"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.ArtistAdded || len(resp.Desires) != 1 || resp.Desires[0].ReleaseMBID != "rel-15" ||
		len(resp.Desires[0].RecordingMBIDs) != 1 || resp.Desires[0].RecordingMBIDs[0] != "rec-3" {
		t.Errorf("resp = %+v", resp)
	}
	var artist models.CollectionArtist
	if err := api.DB.Where("mb_id = ?", "art-new").First(&artist).Error; err != nil || artist.Monitored {
		t.Errorf("artist = %+v (%v), want added and not followed", artist, err)
	}
}

// TestAddWantMixedPicksIs400: "any edition" beside a specific one is refused.
func TestAddWantMixedPicksIs400(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	w := do(r, "POST", "/api/v1/wants", token, wantBody("art-1", "rg-1",
		map[string]any{"release_mb_id": ""}, map[string]any{"release_mb_id": "rel-a"}))
	expectStatus(t, "mixed picks", w.Code, w.Body.String(), http.StatusBadRequest, "any edition")
	var n int64
	api.DB.Model(&models.CollectionArtist{}).Count(&n)
	if n != 0 {
		t.Errorf("artists = %d, want none written", n)
	}
}

// TestAddWantBlockedForLidarrArtist: an artist Lidarr governs takes their wants in
// Lidarr; the add is a 409 and writes nothing.
func TestAddWantBlockedForLidarrArtist(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	artist := artistFixture(t, api.DB, "lidarr-art", "Managed", models.ManagedByLidarr, true)

	w := do(r, "POST", "/api/v1/wants", token, wantBody(artist.MBID, "rg-9"))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
	}
	var n int64
	api.DB.Model(&models.CollectionDesire{}).Where("release_group_mb_id = ?", "rg-9").Count(&n)
	if n != 0 {
		t.Errorf("blocked want was still written (%d)", n)
	}
}

// TestSearchReleaseGroupsPastedRelease: a pasted release link finds the album holding
// it and names the edition, so the picker opens with it ticked.
func TestSearchReleaseGroupsPastedRelease(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	var got string
	api.Meta = &fakeMeta{searchRGs: func(q string) ([]models.MusicBrainzReleaseGroupSearchResult, error) {
		got = q
		return []models.MusicBrainzReleaseGroupSearchResult{{ID: "rg-1", Title: "Album"}}, nil
	}}

	w := do(r, "GET", "/api/v1/search/release-groups?q=https://musicbrainz.org/release/"+testMBID, token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if got != "reid:"+testMBID {
		t.Errorf("query = %q, want a reid search", got)
	}
	var resp releaseGroupSearchResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ReleaseMBID != testMBID || len(resp.ReleaseGroups) != 1 {
		t.Errorf("resp = %+v", resp)
	}
}

// TestSearchesValidateAndReportUpstream: an empty query is a 400 before the source
// is touched, an upstream failure a 502, and a link of the other kind is pointed at
// the other tab.
func TestSearchesValidateAndReportUpstream(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	api.Meta = &fakeMeta{
		searchRGs:  func(string) ([]models.MusicBrainzReleaseGroupSearchResult, error) { return nil, errBoom },
		searchRecs: func(string) ([]models.MusicBrainzRecordingSearchResult, error) { return nil, errBoom },
	}
	for _, path := range []string{"/api/v1/search/release-groups", "/api/v1/search/recordings"} {
		w := do(r, "GET", path+"?q=", token, nil)
		expectStatus(t, path+" empty", w.Code, w.Body.String(), http.StatusBadRequest, "")
		w = do(r, "GET", path+"?q=karma", token, nil)
		expectStatus(t, path+" upstream", w.Code, w.Body.String(), http.StatusBadGateway, "")
	}
	w := do(r, "GET", "/api/v1/search/recordings?q=https://musicbrainz.org/release-group/"+testMBID, token, nil)
	expectStatus(t, "album link under Track", w.Code, w.Body.String(), http.StatusBadRequest, "Album")
}

// TestSearchRecordingsPastedLink: a recording link searches that recording by rid.
func TestSearchRecordingsPastedLink(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	var got string
	api.Meta = &fakeMeta{searchRecs: func(q string) ([]models.MusicBrainzRecordingSearchResult, error) {
		got = q
		return nil, nil
	}}
	w := do(r, "GET", "/api/v1/search/recordings?q=https://musicbrainz.org/recording/"+testMBID, token, nil)
	if w.Code != http.StatusOK || got != "rid:"+testMBID {
		t.Fatalf("status = %d, query = %q: %s", w.Code, got, w.Body.String())
	}
	if w.Body.String() != `{"recordings":[]}` {
		t.Errorf("body = %s, want an empty list, not null", w.Body.String())
	}
}

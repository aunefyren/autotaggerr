package routers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

func seedArtist(t *testing.T, api *API) {
	t.Helper()
	if err := api.DB.Create(&models.CollectionArtist{
		MBID: "art-1", Name: "Band", ManagedBy: models.ManagedByAutotaggerr,
	}).Error; err != nil {
		t.Fatalf("artist: %v", err)
	}
}

// TestSetDesireCarriesRecordings covers the HTTP seam, not just the collection
// package: track selection round-trips through the API body. The first version of
// this endpoint silently dropped recording_mb_ids — the model layer and the UI were
// both correct, and nothing tested the wire between them, so every track click
// stored an unchanged row.
func TestSetDesireCarriesRecordings(t *testing.T) {
	r, api := setupAPI(t)
	seedArtist(t, api)
	token := loginToken(t, r)

	w := do(r, "POST", "/api/v1/artists/art-1/desires", token, map[string]any{
		"release_group_mb_id": "rg-1",
		"release_mb_id":       "",
		"recording_mb_ids":    []string{"rec-a", "rec-b"},
		"title":               "Album",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}

	var stored models.CollectionDesire
	if err := api.DB.Where("release_group_mb_id = ?", "rg-1").First(&stored).Error; err != nil {
		t.Fatalf("desire not stored: %v", err)
	}
	if len(stored.RecordingMBIDs) != 2 {
		t.Fatalf("recordings = %v, want 2 entries", stored.RecordingMBIDs)
	}

	// And the response echoes them, so the UI can trust what it gets back.
	var echoed models.CollectionDesire
	if err := json.Unmarshal(w.Body.Bytes(), &echoed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(echoed.RecordingMBIDs) != 2 {
		t.Errorf("response recordings = %v, want 2", echoed.RecordingMBIDs)
	}
}

// TestSetDesirePerEditionRecordings is the case-5 shape over HTTP: different tracks
// from different editions of one release-group.
func TestSetDesirePerEditionRecordings(t *testing.T) {
	r, api := setupAPI(t)
	seedArtist(t, api)
	token := loginToken(t, r)

	for _, c := range []struct {
		release    string
		recordings []string
	}{
		{"rel-1977", []string{"rec-a", "rec-b"}},
		{"rel-2017", []string{"rec-c"}},
	} {
		w := do(r, "POST", "/api/v1/artists/art-1/desires", token, map[string]any{
			"release_group_mb_id": "rg-1",
			"release_mb_id":       c.release,
			"recording_mb_ids":    c.recordings,
			"title":               "Album",
		})
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status = %d: %s", c.release, w.Code, w.Body.String())
		}
	}

	var desires []models.CollectionDesire
	if err := api.DB.Where("release_group_mb_id = ?", "rg-1").Find(&desires).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(desires) != 2 {
		t.Fatalf("got %d desires, want 2 editions", len(desires))
	}
	for _, d := range desires {
		switch d.ReleaseMBID {
		case "rel-1977":
			if len(d.RecordingMBIDs) != 2 {
				t.Errorf("1977 recordings = %v, want 2", d.RecordingMBIDs)
			}
		case "rel-2017":
			if len(d.RecordingMBIDs) != 1 {
				t.Errorf("2017 recordings = %v, want 1", d.RecordingMBIDs)
			}
		default:
			t.Errorf("unexpected release %q", d.ReleaseMBID)
		}
	}
}

// TestReleaseGroupDetailReportsRecordings: the page reads its state back from this
// endpoint, so a selection that saves but does not surface is just as broken.
func TestReleaseGroupDetailReportsRecordings(t *testing.T) {
	r, api := setupAPI(t)
	seedArtist(t, api)
	token := loginToken(t, r)

	if w := do(r, "POST", "/api/v1/artists/art-1/desires", token, map[string]any{
		"release_group_mb_id": "rg-1",
		"recording_mb_ids":    []string{"rec-a"},
		"title":               "Album",
	}); w.Code != http.StatusOK {
		t.Fatalf("seed desire: %s", w.Body.String())
	}

	w := do(r, "GET", "/api/v1/artists/art-1/release-groups/rg-1", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		ReleaseGroup struct {
			Wanted            bool     `json:"wanted"`
			WantedAnyEdition  bool     `json:"wanted_any_edition"`
			DesiredRecordings []string `json:"desired_recordings"`
		} `json:"release_group"`
		Desires []models.CollectionDesire `json:"desires"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.ReleaseGroup.Wanted || !got.ReleaseGroup.WantedAnyEdition {
		t.Errorf("release group not reported as wanted: %+v", got.ReleaseGroup)
	}
	if len(got.ReleaseGroup.DesiredRecordings) != 1 || len(got.Desires) != 1 {
		t.Errorf("recordings not surfaced: %+v", got)
	}
}

// TestReleaseGroupEditions: the edition picker lists a release-group's releases through
// the metadata source. Previously coverable only against live MusicBrainz.
func TestReleaseGroupEditions(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	api.Meta = &fakeMeta{getRGReleases: func(rgID string) ([]models.MusicBrainzReleaseSearchResult, error) {
		if rgID != "rg-1" {
			t.Errorf("GetReleaseGroupReleases called with %q, want rg-1", rgID)
		}
		return []models.MusicBrainzReleaseSearchResult{{ID: "ed-1"}, {ID: "ed-2"}}, nil
	}}

	w := do(r, "GET", "/api/v1/release-groups/rg-1/releases", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var editions []models.MusicBrainzReleaseSearchResult
	if err := json.Unmarshal(w.Body.Bytes(), &editions); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(editions) != 2 || editions[0].ID != "ed-1" {
		t.Errorf("editions = %+v", editions)
	}
}

// TestReleaseGroupEditionsUpstreamError: a source failure is a 502.
func TestReleaseGroupEditionsUpstreamError(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	api.Meta = &fakeMeta{getRGReleases: func(string) ([]models.MusicBrainzReleaseSearchResult, error) {
		return nil, errBoom
	}}

	w := do(r, "GET", "/api/v1/release-groups/rg-1/releases", token, nil)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", w.Code, w.Body.String())
	}
}

// TestSetDesireBlockedForLidarrArtist: under "Lidarr owns identity" a want is Lidarr's
// to set, so the desire endpoint rejects it with 409 and writes nothing.
func TestSetDesireBlockedForLidarrArtist(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	artist := artistFixture(t, api.DB, "lidarr-art", "Managed", models.ManagedByLidarr, true)

	w := do(r, "POST", "/api/v1/artists/"+artist.MBID+"/desires", token, map[string]any{
		"release_group_mb_id": "rg-1", "title": "Album",
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
	}

	var count int64
	api.DB.Model(&models.CollectionDesire{}).Where("release_group_mb_id = ?", "rg-1").Count(&count)
	if count != 0 {
		t.Errorf("blocked desire was still written (count %d)", count)
	}
}

// TestSetDesireInvalidBody: a malformed body is a 400 before anything is resolved.
func TestSetDesireInvalidBody(t *testing.T) {
	r, api := setupAPI(t)
	seedArtist(t, api)
	token := loginToken(t, r)

	if w := do(r, "POST", "/api/v1/artists/art-1/desires", token, "not-an-object"); w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
}

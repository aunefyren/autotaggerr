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

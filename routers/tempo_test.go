package routers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/aunefyren/autotaggerr/tempo"
)

func TestTempoRoutesWithoutARunner(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/v1/tempo/status"},
		{"POST", "/api/v1/tempo/analyse"},
		{"POST", "/api/v1/tempo/cancel"},
	} {
		if w := do(r, route.method, route.path, token, nil); w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d, want 503 with no runner wired", route.method, route.path, w.Code)
		}
	}
}

func TestTempoRoutesRequireAuth(t *testing.T) {
	r, _ := setupAPI(t)
	if w := do(r, "POST", "/api/v1/tempo/analyse", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated analyse = %d, want 401", w.Code)
	}
}

// A press with nothing to analyse is refused with the reason — and the reason names
// where the switch is.
func TestTempoAnalyseRefusesWithNothingOptedIn(t *testing.T) {
	r, api := setupAPI(t)
	api.Tempo = tempo.NewRunnerForTest(t, api.DB, nil)
	token := loginToken(t, r)

	w := do(r, "POST", "/api/v1/tempo/analyse", token, nil)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "Libraries") {
		t.Fatalf("analyse = %d %s, want 409 naming the Libraries page", w.Code, w.Body.String())
	}
}

// The opt-in is set through the ordinary library edit, and is what makes a press
// queue a pass.
func TestTempoAnalyseQueuesForAnOptedInLibrary(t *testing.T) {
	r, api := setupAPI(t)
	detected := make(chan string, 4)
	api.Tempo = tempo.NewRunnerForTest(t, api.DB, func(_ context.Context, path string) (modules.TempoAnalysis, error) {
		detected <- path
		return modules.TempoAnalysis{BPM: 160, Confidence: 0.9, Beats: 400}, nil
	})
	token := loginToken(t, r)

	library := models.Library{Name: "Music", Path: t.TempDir(), Enabled: true}
	if err := api.DB.Create(&library).Error; err != nil {
		t.Fatal(err)
	}
	w := do(r, "PUT", "/api/v1/libraries/"+library.ID.String(), token, map[string]any{"analyse_tempo": true})
	if w.Code != http.StatusOK {
		t.Fatalf("update library = %d %s", w.Code, w.Body.String())
	}
	var saved models.Library
	api.DB.First(&saved, "id = ?", library.ID)
	if !saved.AnalyseTempo {
		t.Fatal("analyse_tempo was not saved")
	}

	if w := do(r, "POST", "/api/v1/tempo/analyse", token, nil); w.Code != http.StatusAccepted {
		t.Fatalf("analyse = %d %s, want 202", w.Code, w.Body.String())
	}

	w = do(r, "GET", "/api/v1/tempo/status", token, nil)
	var status tempo.Summary
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil || w.Code != http.StatusOK {
		t.Fatalf("status = %d %s", w.Code, w.Body.String())
	}
	if !status.DetectorAvailable || status.Libraries != 1 {
		t.Fatalf("status = %+v, want the detector available and one library opted in", status)
	}

	if w := do(r, "POST", "/api/v1/tempo/cancel", token, nil); w.Code != http.StatusOK {
		t.Fatalf("cancel = %d, want 200", w.Code)
	}
}

// The BPM policy is an ordinary library field: saved when valid, refused when the
// writers would not understand it.
func TestLibraryBPMPolicy(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	w := do(r, "POST", "/api/v1/libraries", token, map[string]any{
		"name": "Running", "path": "/run", "analyse_tempo": true, "bpm_write": models.BPMWriteFillEmpty, "bpm_min_confidence": 0.6,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	var created models.Library
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	var saved models.Library
	api.DB.First(&saved, "id = ?", created.ID)
	if saved.BPMWrite != models.BPMWriteFillEmpty || saved.BPMMinConfidence != 0.6 {
		t.Fatalf("saved library = %+v", saved)
	}

	for _, body := range []map[string]any{
		{"bpm_write": "always"},
		{"bpm_min_confidence": 1.5},
		{"bpm_min_confidence": -0.1},
	} {
		if w := do(r, "PUT", "/api/v1/libraries/"+created.ID.String(), token, body); w.Code != http.StatusBadRequest {
			t.Errorf("update %v = %d, want 400", body, w.Code)
		}
	}
	if w := do(r, "POST", "/api/v1/libraries", token, map[string]any{"name": "Bad", "path": "/bad", "bpm_write": "sometimes"}); w.Code != http.StatusBadRequest {
		t.Errorf("create with an unknown mode = %d, want 400", w.Code)
	}

	if w := do(r, "PUT", "/api/v1/libraries/"+created.ID.String(), token, map[string]any{"bpm_write": models.BPMWriteOff}); w.Code != http.StatusOK {
		t.Fatalf("turning BPM off = %d %s", w.Code, w.Body.String())
	}
}

// The item view's tempo line rests on three facts, and each of the three "no BPM
// tag" answers has to be distinguishable from the response alone.
func TestItemTempoExplainsItself(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	profile := models.TaggerProfile{Name: "Running", WriteTags: true}
	api.DB.Create(&profile)
	library := models.Library{Name: "Music", Path: "/music", Enabled: true, AnalyseTempo: true, TaggerProfileID: &profile.ID,
		BPMWrite: models.BPMWriteFillEmpty, BPMMinConfidence: 0.7}
	api.DB.Create(&library)
	item := models.LibraryItem{LibraryID: library.ID, Path: "/music/a.flac"}
	api.DB.Create(&item)

	read := func() map[string]any {
		t.Helper()
		w := do(r, "GET", "/api/v1/library-items/"+item.ID.String()+"/tempo", token, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("item tempo = %d %s", w.Code, w.Body.String())
		}
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return body
	}

	body := read()
	if body["tempo"] != nil || body["library_analyses"] != true || body["bpm_write"] != models.BPMWriteFillEmpty || body["min_confidence"] != 0.7 {
		t.Fatalf("before analysis: %v", body)
	}

	api.DB.Create(&models.TrackTempo{Path: item.Path, BPM: 160.2, Confidence: 0.9, Beats: 400})
	body = read()
	stored, _ := body["tempo"].(map[string]any)
	if stored == nil || stored["bpm"] != 160.2 {
		t.Fatalf("after analysis: %v", body)
	}

	// A profile that writes no tags writes no BPM, whatever the library's BPM setting says.
	api.DB.Model(&profile).Update("write_tags", false)
	if body = read(); body["bpm_write"] != "" || body["writes_tags"] != false {
		t.Fatalf("bpm_write = %v for a profile that writes no tags, want off", body["bpm_write"])
	}

	if w := do(r, "GET", "/api/v1/library-items/01900000-0000-7000-8000-000000000000/tempo", token, nil); w.Code != http.StatusNotFound {
		t.Fatalf("unknown item = %d, want 404", w.Code)
	}
}

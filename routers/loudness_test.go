package routers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/loudness"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
)

func TestLoudnessRoutesWithoutARunner(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/v1/loudness/status"},
		{"POST", "/api/v1/loudness/analyse"},
		{"POST", "/api/v1/loudness/cancel"},
	} {
		if w := do(r, route.method, route.path, token, nil); w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d, want 503 with no runner wired", route.method, route.path, w.Code)
		}
	}
}

// TestLoudnessAnalyse: refused with the reason while nothing opts in; the opt-in and
// write mode are set through the library edit (and an unknown mode refused); then a
// press queues a pass.
func TestLoudnessAnalyse(t *testing.T) {
	r, api := setupAPI(t)
	api.Loudness = loudness.NewRunnerForTest(t, api.DB, func(_ context.Context, paths []string) (modules.AlbumLoudness, error) {
		out := modules.AlbumLoudness{Album: modules.LoudnessResult{Gain: -7}}
		for range paths {
			out.Tracks = append(out.Tracks, modules.LoudnessResult{Gain: -6})
		}
		return out, nil
	})
	token := loginToken(t, r)

	if w := do(r, "POST", "/api/v1/loudness/analyse", token, nil); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "Libraries") {
		t.Fatalf("analyse with nothing opted in = %d %s", w.Code, w.Body.String())
	}

	library := models.Library{Name: "Music", Path: t.TempDir(), Enabled: true}
	if err := api.DB.Create(&library).Error; err != nil {
		t.Fatal(err)
	}
	if w := do(r, "PUT", "/api/v1/libraries/"+library.ID.String(), token, map[string]any{"replaygain_write": "always"}); w.Code != http.StatusBadRequest {
		t.Errorf("an unknown replaygain_write = %d, want 400", w.Code)
	}
	if w := do(r, "PUT", "/api/v1/libraries/"+library.ID.String(), token, map[string]any{"analyse_loudness": true, "replaygain_write": "fill_empty"}); w.Code != http.StatusOK {
		t.Fatalf("update library = %d %s", w.Code, w.Body.String())
	}
	var saved models.Library
	api.DB.First(&saved, "id = ?", library.ID)
	if !saved.AnalyseLoudness || saved.ReplayGainWrite != models.BPMWriteFillEmpty {
		t.Fatalf("library = %+v, want loudness on and fill_empty", saved)
	}

	if w := do(r, "POST", "/api/v1/loudness/analyse", token, nil); w.Code != http.StatusAccepted {
		t.Fatalf("analyse = %d %s, want 202", w.Code, w.Body.String())
	}
	w := do(r, "GET", "/api/v1/loudness/status", token, nil)
	var status loudness.Summary
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil || !status.AnalyserAvailable || status.Libraries != 1 {
		t.Fatalf("status = %d %s", w.Code, w.Body.String())
	}
	if w := do(r, "POST", "/api/v1/loudness/cancel", token, nil); w.Code != http.StatusOK {
		t.Fatalf("cancel = %d", w.Code)
	}
}

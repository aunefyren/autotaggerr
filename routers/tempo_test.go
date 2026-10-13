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

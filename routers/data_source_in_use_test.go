package routers

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
)

// TestDataSourcesMarkTheMusicBrainzSourceInUse: with a mirror beside the public
// service only one is read, and the page has to say which — the oldest enabled one.
// Deleting it hands the client to the next.
func TestDataSourcesMarkTheMusicBrainzSourceInUse(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	t.Cleanup(func() {
		modules.SetMusicBrainzEndpoint("", "")
		modules.SetMusicBrainzRateLimit(1)
	})

	public := models.DataSource{Name: "Public", Type: models.DataSourceTypeMusicBrainz, BaseURL: modules.DefaultMusicBrainzBaseURL, RateLimit: 1, Enabled: true}
	if err := api.DB.Create(&public).Error; err != nil {
		t.Fatal(err)
	}
	mirror := models.DataSource{Name: "Mirror", Type: models.DataSourceTypeMusicBrainz, BaseURL: "http://mirror.invalid/ws/2", RateLimit: 50, Enabled: true}
	mirror.CreatedAt = public.CreatedAt.Add(time.Second)
	if err := api.DB.Create(&mirror).Error; err != nil {
		t.Fatal(err)
	}

	inUse := func() []string {
		t.Helper()
		w := do(r, http.MethodGet, "/api/v1/data-sources", token, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("list: %d %s", w.Code, w.Body.String())
		}
		var rows []struct {
			Name  string `json:"name"`
			InUse bool   `json:"in_use"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, row := range rows {
			if row.InUse {
				names = append(names, row.Name)
			}
		}
		return names
	}

	if got := inUse(); len(got) != 1 || got[0] != "Public" {
		t.Fatalf("in use = %v, want [Public], the oldest enabled", got)
	}
	if w := do(r, http.MethodDelete, "/api/v1/data-sources/"+public.ID.String(), token, nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if got := inUse(); len(got) != 1 || got[0] != "Mirror" {
		t.Fatalf("in use after delete = %v, want [Mirror]", got)
	}
}

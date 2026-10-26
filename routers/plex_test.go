package routers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
)

// TestPlexSectionsAndTheLibrarysSection: the form lists the server's music sections,
// says when Plex is not configured or not answering, and a library stores its section
// and its opt-out.
func TestPlexSectionsAndTheLibrarysSection(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	read := func() plexSectionsView {
		t.Helper()
		w := do(r, http.MethodGet, "/api/v1/plex/sections", token, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("sections: %d %s", w.Code, w.Body.String())
		}
		var v plexSectionsView
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	if v := read(); v.Configured {
		t.Errorf("no Plex reported as configured: %+v", v)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<MediaContainer><Directory key="4" title="Music" type="artist"><Location path="/data/music"/></Directory></MediaContainer>`))
	}))
	t.Cleanup(srv.Close)
	api.Plex = modules.NewPlexClient(srv.URL, "tok")
	if v := read(); !v.Configured || len(v.Sections) != 1 || v.Sections[0].Key != "4" {
		t.Errorf("sections = %+v", v)
	}

	srv.Close()
	if v := read(); !v.Configured || v.Error == "" {
		t.Errorf("a Plex that does not answer = %+v, want the error in the body", v)
	}

	lib := models.Library{Name: "Music", Path: "/music", Enabled: true}
	if err := api.DB.Create(&lib).Error; err != nil {
		t.Fatal(err)
	}
	if w := do(r, http.MethodPut, "/api/v1/libraries/"+lib.ID.String(), token, map[string]any{
		"plex_section_key": " 4 ", "plex_skip": true,
	}); w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	var got models.Library
	if err := api.DB.First(&got, "id = ?", lib.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.PlexSectionKey != "4" || !got.PlexSkip {
		t.Errorf("library = section %q skip %v", got.PlexSectionKey, got.PlexSkip)
	}
}

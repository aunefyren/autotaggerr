package routers

import (
	"net/http"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// TestArtistListCountsInOneLibrary: read in one library, the list counts what that
// library holds — the way the artist page's library tab does — instead of narrowing
// the rows and leaving every column all-library.
func TestArtistListCountsInOneLibrary(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	sandbox := models.Library{Name: "Sandbox", Path: "/sandbox", Enabled: true}
	music := models.Library{Name: "Music", Path: "/music", Enabled: true}
	for _, row := range []any{&sandbox, &music,
		&models.CollectionArtist{MBID: "art-1", Name: "Artist", ManagedBy: models.ManagedByAutotaggerr},
		// Owned in Music only, complete there.
		&models.CollectionReleaseGroup{MBID: "rg-a", ArtistMBID: "art-1", Title: "A", Owned: true, OwnedTracks: 10, TotalTracks: 10},
		// Owned in both; complete in Music, partial in Sandbox.
		&models.CollectionReleaseGroup{MBID: "rg-b", ArtistMBID: "art-1", Title: "B", Owned: true, OwnedTracks: 8, TotalTracks: 8},
		// Owned nowhere.
		&models.CollectionReleaseGroup{MBID: "rg-c", ArtistMBID: "art-1", Title: "C"},
		&models.CollectionRelease{MBID: "rel-a", ReleaseGroupMBID: "rg-a", ArtistMBID: "art-1", TotalTracks: 10},
		&models.CollectionRelease{MBID: "rel-b", ReleaseGroupMBID: "rg-b", ArtistMBID: "art-1", TotalTracks: 8},
	} {
		if err := api.DB.Create(row).Error; err != nil {
			t.Fatalf("seed %T: %v", row, err)
		}
	}
	for _, c := range []models.CollectionReleaseCopy{
		{ReleaseMBID: "rel-a", LibraryID: music.ID, ReleaseGroupMBID: "rg-a", ArtistMBID: "art-1", OwnedTracks: 10, Files: 10},
		{ReleaseMBID: "rel-b", LibraryID: music.ID, ReleaseGroupMBID: "rg-b", ArtistMBID: "art-1", OwnedTracks: 8, Files: 8},
		{ReleaseMBID: "rel-b", LibraryID: sandbox.ID, ReleaseGroupMBID: "rg-b", ArtistMBID: "art-1", OwnedTracks: 3, Files: 3},
	} {
		if err := api.DB.Create(&c).Error; err != nil {
			t.Fatal(err)
		}
	}

	type row struct {
		MBID     string `json:"mb_id"`
		Owned    int    `json:"owned_count"`
		Complete int    `json:"complete_count"`
		Partial  int    `json:"partial_count"`
		Missing  int    `json:"missing_count"`
	}
	read := func(query string) row {
		t.Helper()
		rows := decodeJSON[[]row](t, r, "GET", "/api/v1/artists"+query, token, nil)
		if len(rows) != 1 {
			t.Fatalf("%s: rows = %+v", query, rows)
		}
		return rows[0]
	}

	if got := read(""); got.Owned != 2 || got.Complete != 2 || got.Missing != 1 {
		t.Errorf("overview = %+v, want 2 owned and complete, 1 missing", got)
	}
	if got := read("?library=" + sandbox.ID.String()); got.Owned != 1 || got.Complete != 0 || got.Partial != 1 || got.Missing != 2 {
		t.Errorf("sandbox = %+v, want 1 owned (partial), 2 missing", got)
	}
	// Served twice from the memo, and still told apart per library.
	if got := read("?library=" + music.ID.String()); got.Owned != 2 || got.Complete != 2 {
		t.Errorf("music = %+v, want 2 owned and complete", got)
	}
	if got := read("?library=" + sandbox.ID.String()); got.Owned != 1 {
		t.Errorf("sandbox from the memo = %+v", got)
	}

	if w := do(r, http.MethodGet, "/api/v1/artists?library=not-a-uuid", token, nil); w.Code != http.StatusBadRequest {
		t.Errorf("a bad library = %d", w.Code)
	}
	if w := do(r, http.MethodGet, "/api/v1/artists?library=01a17982-0000-7000-8000-000000000000", token, nil); w.Code != http.StatusNotFound {
		t.Errorf("an unknown library = %d", w.Code)
	}
}

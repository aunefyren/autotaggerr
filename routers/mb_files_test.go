package routers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type mbFilesResponse struct {
	MBID  string `json:"mb_id"`
	Total int    `json:"total"`
	Files []struct {
		Path    string `json:"path"`
		Library string `json:"library"`
		Status  string `json:"status"`
	} `json:"files"`
}

// seedTwoEditions lays down one artist with one album held as two editions, three files
// in total, in a named library.
func seedTwoEditions(t *testing.T, db *gorm.DB) uuid.UUID {
	t.Helper()
	library := models.Library{Name: "Music", Path: "/music"}
	if err := db.Create(&library).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	rows := []any{
		&models.CollectionArtist{MBID: "artist-1", Name: "Talk Talk"},
		&models.CollectionReleaseGroup{MBID: "group-1", ArtistMBID: "artist-1", Title: "Laughing Stock"},
		&models.CollectionRelease{MBID: "release-1", ReleaseGroupMBID: "group-1", ArtistMBID: "artist-1", Title: "Laughing Stock"},
		&models.CollectionRelease{MBID: "release-2", ReleaseGroupMBID: "group-1", ArtistMBID: "artist-1", Title: "Laughing Stock (2011)"},
		&models.LibraryItem{LibraryID: library.ID, Path: "/music/a.flac", MBReleaseID: "release-1"},
		&models.LibraryItem{LibraryID: library.ID, Path: "/music/b.flac", MBReleaseID: "release-1"},
		&models.LibraryItem{LibraryID: library.ID, Path: "/music/c.flac", MBReleaseID: "release-2"},
	}
	for _, row := range rows {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return library.ID
}

func getMBFiles(t *testing.T, r *gin.Engine, token, mbid string) mbFilesResponse {
	t.Helper()
	w := do(r, "GET", "/api/v1/mb/"+mbid+"/files", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /mb/%s/files = %d: %s", mbid, w.Code, w.Body.String())
	}
	var out mbFilesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// TestMBFilesAcceptsEveryKind: the caller is an Activity detail row that knows only
// what a metadata pass told it, so all three kinds have to answer. Files hang off
// releases, so the artist and release-group forms are only useful if they resolve
// through the collection's editions — which is exactly what could silently stop
// working.
func TestMBFilesAcceptsEveryKind(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	seedTwoEditions(t, api.DB)

	if got := getMBFiles(t, r, token, "release-1"); got.Total != 2 {
		t.Errorf("release: total = %d, want 2", got.Total)
	}
	if got := getMBFiles(t, r, token, "group-1"); got.Total != 3 {
		t.Errorf("release-group: total = %d, want 3 (both editions)", got.Total)
	}
	got := getMBFiles(t, r, token, "artist-1")
	if got.Total != 3 {
		t.Errorf("artist: total = %d, want 3", got.Total)
	}
	if len(got.Files) == 0 || got.Files[0].Library != "Music" {
		t.Errorf("files did not carry their library name: %+v", got.Files)
	}
}

// TestMBFilesUnknownIdentifierIsEmptyNotMissing: "nothing points at it" is the answer,
// not a missing page — a 404 here would read as a broken endpoint on precisely the
// identifier a user is investigating.
func TestMBFilesUnknownIdentifierIsEmptyNotMissing(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)

	got := getMBFiles(t, r, token, uuid.New().String())
	if got.Total != 0 || len(got.Files) != 0 {
		t.Errorf("unknown identifier returned %d file(s)", got.Total)
	}
}

// TestGetEventResolvesEntityRows pins the wiring: the single-event endpoint is where a
// detail row stops being a bare UUID. Resolving in the handler rather than storing it
// on the row is deliberate, so this is the only place it can be verified.
func TestGetEventResolvesEntityRows(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	seedTwoEditions(t, api.DB)

	ev := events.Begin(api.DB, models.EventTypeMirror, "Metadata refresh")
	events.Finish(api.DB, ev, models.EventStatusOK, "1 entity", nil)
	events.AddItems(api.DB, ev, []models.EventItem{{
		Path: "release-1", Kind: models.EventItemKindEntity, Status: models.EventItemStatusGone, Phase: "releases",
	}})

	w := do(r, "GET", "/api/v1/events/"+ev.ID.String(), token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET event = %d: %s", w.Code, w.Body.String())
	}
	var got models.Event
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(got.Items))
	}
	ref := got.Items[0].Related
	if ref == nil {
		t.Fatal("entity row came back unresolved; the modal would show a bare UUID")
	}
	if ref.Name != "Laughing Stock" || ref.Files != 2 {
		t.Errorf("ref = %+v, want the release named with its 2 files", ref)
	}
}

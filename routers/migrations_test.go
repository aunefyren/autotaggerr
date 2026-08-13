package routers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

func seedMigration(t *testing.T, api *API, entity, old, new, status string) models.MusicbrainzMigration {
	t.Helper()
	m := models.MusicbrainzMigration{
		EntityType: entity,
		OldMBID:    old,
		NewMBID:    new,
		Kind:       models.MigrationKindRedirect,
		Status:     status,
	}
	if err := api.DB.Create(&m).Error; err != nil {
		t.Fatalf("create migration: %v", err)
	}
	return m
}

func TestListMigrationsFiltersByStatus(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	seedMigration(t, api, models.MigrationEntityRelease, "rel-old", "rel-new", models.MigrationStatusPending)
	seedMigration(t, api, models.MigrationEntityArtist, "art-old", "art-new", models.MigrationStatusApplied)

	w := do(r, "GET", "/api/v1/migrations", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, body %s", w.Code, w.Body.String())
	}
	var all struct {
		Migrations []models.MusicbrainzMigration `json:"migrations"`
		Pending    int                           `json:"pending"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &all); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(all.Migrations) != 2 {
		t.Errorf("migrations = %d, want 2", len(all.Migrations))
	}
	if all.Pending != 1 {
		t.Errorf("pending count = %d, want 1", all.Pending)
	}

	w = do(r, "GET", "/api/v1/migrations?status=pending", token, nil)
	if err := json.Unmarshal(w.Body.Bytes(), &all); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(all.Migrations) != 1 || all.Migrations[0].OldMBID != "rel-old" {
		t.Errorf("filtered = %+v, want just the pending one", all.Migrations)
	}
}

func TestApproveMigrationAppliesIt(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	lib := models.Library{Name: "L", Path: t.TempDir(), Enabled: true}
	if err := api.DB.Create(&lib).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	if err := api.DB.Create(&models.LibraryItem{
		LibraryID: lib.ID, Path: "/m/a.flac", Status: models.LibraryItemStatusOK, MBReleaseID: "rel-old",
	}).Error; err != nil {
		t.Fatalf("create item: %v", err)
	}
	m := seedMigration(t, api, models.MigrationEntityRelease, "rel-old", "rel-new", models.MigrationStatusPending)

	w := do(r, "POST", "/api/v1/migrations/"+m.ID.String()+"/approve", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, body %s", w.Code, w.Body.String())
	}

	var item models.LibraryItem
	if err := api.DB.First(&item).Error; err != nil {
		t.Fatalf("find item: %v", err)
	}
	if item.MBReleaseID != "rel-new" {
		t.Errorf("item release = %q, want rel-new", item.MBReleaseID)
	}

	// Approving twice is a conflict, not a silent second application.
	w = do(r, "POST", "/api/v1/migrations/"+m.ID.String()+"/approve", token, nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("re-approve code = %d, want 400", w.Code)
	}
}

func TestDismissMigration(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	m := seedMigration(t, api, models.MigrationEntityRelease, "rel-old", "rel-new", models.MigrationStatusPending)

	w := do(r, "POST", "/api/v1/migrations/"+m.ID.String()+"/dismiss", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, body %s", w.Code, w.Body.String())
	}

	var row models.MusicbrainzMigration
	if err := api.DB.First(&row, "id = ?", m.ID).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	if row.Status != models.MigrationStatusDismissed {
		t.Errorf("status = %q, want dismissed", row.Status)
	}
}

func TestMigrationsRequireAuth(t *testing.T) {
	r, _ := setupAPI(t)
	for _, path := range []string{"/api/v1/migrations", "/api/v1/migrations/policy"} {
		if w := do(r, "GET", path, "", nil); w.Code != http.StatusUnauthorized {
			t.Errorf("%s unauthenticated code = %d, want 401", path, w.Code)
		}
	}
}

// The sweep is hours of rate-limited work, so the endpoint must hand back control
// immediately rather than holding the request open until it finishes.
func TestVerifyIdentitiesReturnsImmediately(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)

	w := do(r, "POST", "/api/v1/migrations/verify", token, nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202; body %s", w.Code, w.Body.String())
	}
}

func TestVerifyIdentitiesRequiresAuth(t *testing.T) {
	r, _ := setupAPI(t)
	if w := do(r, "POST", "/api/v1/migrations/verify", "", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("code = %d, want 401", w.Code)
	}
}

func TestMigrationPolicyEndpoint(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)

	w := do(r, "GET", "/api/v1/migrations/policy", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, body %s", w.Code, w.Body.String())
	}
	var policy map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &policy); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, key := range []string{"review_releases", "review_artists", "review_pinned", "review_deletions"} {
		if _, ok := policy[key]; !ok {
			t.Errorf("policy response missing %q", key)
		}
	}
}

// TestApproveBlockedAlbumAsksTheManager: the case a live Lidarr install runs into.
//
// A release-group whose ID no longer resolves but which the manager still lists cannot
// simply be retired — the next sync would put it straight back. Approving used to
// return the reason as a 400 and tell the user to go and press refresh in Lidarr
// themselves, which is the one step Autotaggerr can do for them. It now queues that
// refresh and answers 202, because the manager's command is waited on for minutes.
func TestApproveBlockedAlbumAsksTheManager(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	if err := api.DB.Create(&models.CollectionArtist{MBID: "artist-1", Name: "Some Band"}).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	if err := api.DB.Create(&models.CollectionReleaseGroup{
		MBID: "rg-ghost", ArtistMBID: "artist-1", Title: "Heatstroke", InCatalog: true,
	}).Error; err != nil {
		t.Fatalf("create release-group: %v", err)
	}
	m := models.MusicbrainzMigration{
		EntityType: models.MigrationEntityReleaseGroup,
		OldMBID:    "rg-ghost",
		Kind:       models.MigrationKindDeleted,
		Status:     models.MigrationStatusPending,
		Name:       "Heatstroke",
	}
	if err := api.DB.Create(&m).Error; err != nil {
		t.Fatalf("create migration: %v", err)
	}

	w := do(r, "POST", "/api/v1/migrations/"+m.ID.String()+"/approve", token, nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202; body %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["artist_mbid"] != "artist-1" {
		t.Errorf("artist_mbid = %v, want the artist to be refreshed", body["artist_mbid"])
	}

	// The row is left alone: nothing has been decided yet, and marking it applied or
	// failed here would report an outcome the manager has not given.
	var row models.MusicbrainzMigration
	if err := api.DB.First(&row, "id = ?", m.ID).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	if row.Status != models.MigrationStatusPending {
		t.Errorf("status = %q, want it still pending until the manager answers", row.Status)
	}
	// But it does not look untouched. A press that answers 202 and changes nothing on
	// screen is indistinguishable from a broken button, so the row carries the fact that
	// a refresh is in flight — durably, so a reload still shows it.
	if row.RepairQueuedAt == nil {
		t.Error("the row does not show that a manager refresh is running for it")
	}
	if body["queued"] != true {
		t.Errorf("queued = %v, want the client to be able to tell this from an application", body["queued"])
	}
}

// TestListMigrationsExplainsItself: a review queue of bare IDs and zeroes is not a
// review queue. Every pending row must arrive with what is wrong and what approving
// does, so the UI can render a decision rather than a symptom.
func TestListMigrationsExplainsItself(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	if err := api.DB.Create(&models.CollectionReleaseGroup{
		MBID: "rg-ghost", ArtistMBID: "artist-1", Title: "Heatstroke", InCatalog: true,
	}).Error; err != nil {
		t.Fatalf("create release-group: %v", err)
	}
	if err := api.DB.Create(&models.MusicbrainzMigration{
		EntityType: models.MigrationEntityReleaseGroup,
		OldMBID:    "rg-ghost",
		Kind:       models.MigrationKindDeleted,
		Status:     models.MigrationStatusPending,
		Name:       "Heatstroke",
	}).Error; err != nil {
		t.Fatalf("create migration: %v", err)
	}

	w := do(r, "GET", "/api/v1/migrations?status=pending", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, body %s", w.Code, w.Body.String())
	}
	var body struct {
		Migrations []struct {
			Problem             string `json:"problem"`
			Effect              string `json:"effect"`
			Blocker             string `json:"blocker"`
			FilesOnDisk         int    `json:"files_on_disk"`
			NeedsManagerRefresh bool   `json:"needs_manager_refresh"`
		} `json:"migrations"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Migrations) != 1 {
		t.Fatalf("migrations = %d, want 1", len(body.Migrations))
	}
	row := body.Migrations[0]
	if row.Problem == "" || row.Effect == "" {
		t.Errorf("a pending row must say what is wrong and what approving does, got %+v", row)
	}
	if !row.NeedsManagerRefresh || row.Blocker == "" {
		t.Errorf("the manager's claim on this album must be visible before approving, got %+v", row)
	}
}

// The table can hold thousands of rows and every one of them costs the review queries
// that say what approving it would do, so a page is a page on the server.
func TestListMigrationsPages(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	for _, id := range []string{"rel-1", "rel-2", "rel-3"} {
		seedMigration(t, api, models.MigrationEntityRelease, id, id+"-new", models.MigrationStatusPending)
	}

	w := do(r, "GET", "/api/v1/migrations?status=open&limit=2", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, body %s", w.Code, w.Body.String())
	}
	var page struct {
		Migrations []models.MusicbrainzMigration `json:"migrations"`
		Total      int                           `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The total is what makes the pager honest: the page itself cannot say whether
	// there are three more rows or three hundred.
	if len(page.Migrations) != 2 || page.Total != 3 {
		t.Errorf("page/total = %d/%d, want 2/3", len(page.Migrations), page.Total)
	}

	w = do(r, "GET", "/api/v1/migrations?status=open&limit=2&offset=2", token, nil)
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page.Migrations) != 1 {
		t.Errorf("second page = %d rows, want 1", len(page.Migrations))
	}
}

// A decision nobody recorded is indistinguishable afterwards from the nightly pass
// having made it, which is exactly what the feed exists to settle.
func TestDecidingAMigrationLandsInActivity(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	m := seedMigration(t, api, models.MigrationEntityRelease, "rel-old", "rel-new", models.MigrationStatusPending)
	m.Name = "Kid A"
	if err := api.DB.Save(&m).Error; err != nil {
		t.Fatalf("save: %v", err)
	}

	if w := do(r, "POST", "/api/v1/migrations/"+m.ID.String()+"/dismiss", token, nil); w.Code != http.StatusOK {
		t.Fatalf("dismiss code = %d, body %s", w.Code, w.Body.String())
	}

	var ev models.Event
	if err := api.DB.Where("type = ?", models.EventTypeMigration).First(&ev).Error; err != nil {
		t.Fatalf("no activity was recorded for the decision: %v", err)
	}
	if !strings.Contains(ev.Summary, "Kid A") {
		t.Errorf("summary = %q, want it to name what was decided", ev.Summary)
	}

	// And the entity row beneath it, so the feed can resolve the name, the artist and
	// the files the identifier stands for.
	var items []models.EventItem
	if err := api.DB.Where("event_id = ?", ev.ID).Find(&items).Error; err != nil {
		t.Fatalf("load items: %v", err)
	}
	if len(items) != 1 || items[0].Path != "rel-old" || items[0].Status != models.EventItemStatusDismissed {
		t.Errorf("detail rows = %+v, want one dismissed entity row for rel-old", items)
	}
}

// "Show me the files behind this identifier" — the link a migration row offers, asked
// of a page the user can act on rather than of a read-only detail row.
func TestListItemsFiltersByMBID(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	lib := models.Library{Name: "L", Path: t.TempDir(), Enabled: true}
	if err := api.DB.Create(&lib).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	for path, release := range map[string]string{"/m/a.flac": "rel-1", "/m/b.flac": "rel-2"} {
		if err := api.DB.Create(&models.LibraryItem{
			LibraryID: lib.ID, Path: path, Status: models.LibraryItemStatusOK, MBReleaseID: release,
		}).Error; err != nil {
			t.Fatalf("create item: %v", err)
		}
	}
	// An album reaches its files only through its editions, which is the resolution the
	// filter shares with GET /mb/:mbid/files.
	if err := api.DB.Create(&models.CollectionRelease{MBID: "rel-1", ReleaseGroupMBID: "rg-1"}).Error; err != nil {
		t.Fatalf("create edition: %v", err)
	}

	var page struct {
		Items []models.LibraryItem `json:"items"`
		Total int                  `json:"total"`
	}
	w := do(r, "GET", "/api/v1/library-items?mbid=rg-1", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, body %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].MBReleaseID != "rel-1" {
		t.Errorf("filtered items = %+v (total %d), want just the album's file", page.Items, page.Total)
	}

	// An identifier nothing points at answers with nothing, rather than quietly
	// dropping the filter and showing the whole library under one album's name.
	w = do(r, "GET", "/api/v1/library-items?mbid=nobody", token, nil)
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.Total != 0 {
		t.Errorf("unknown identifier returned %d items, want none", page.Total)
	}
}

package routers

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// seedBulkFixtures puts a three-track release in the cache and three numbered
// files from one folder in the index — the folder-shaped workflow bulk attach
// exists for. Writing is off, so these tests are about the decision that gets
// persisted, not the tag write.
func seedBulkFixtures(t *testing.T, db *gorm.DB) []models.LibraryItem {
	t.Helper()
	modules.SetDB(db)
	t.Cleanup(func() { modules.SetDB(nil) })

	release := models.MusicBrainzReleaseResponse{
		ID:    "rel-bulk",
		Title: "Rumours",
		Media: []models.MusicBrainzMedia{{
			Position: 1,
			Tracks: []models.Track{
				{ID: "t1", Title: "Second Hand News", Position: 1, Number: "1", Recording: recordingWithID("rec-1")},
				{ID: "t2", Title: "Dreams", Position: 2, Number: "2", Recording: recordingWithID("rec-2")},
				{ID: "t3", Title: "Never Going Back Again", Position: 3, Number: "3", Recording: recordingWithID("rec-3")},
			},
		}},
	}
	payload, _ := json.Marshal(release)
	if err := db.Create(&models.MusicbrainzReleaseCache{
		MBID: "rel-bulk", Payload: string(payload),
		FetchedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	if err := modules.MusicbrainzLoadCache(); err != nil {
		t.Fatalf("load cache: %v", err)
	}

	profile := models.TaggerProfile{Name: "bulk-no-write", WriteTags: false}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("profile: %v", err)
	}
	lib := models.Library{Name: "BulkL", Path: "/m", Enabled: true, TaggerProfileID: &profile.ID}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}

	// Deliberately out of track order on disk order, to prove the mapping is by
	// number rather than by insertion.
	var items []models.LibraryItem
	for _, name := range []string{"02 Dreams.flac", "01 Second Hand News.flac", "03 Never Going Back Again.flac"} {
		item := models.LibraryItem{
			LibraryID: lib.ID, Path: "/m/Fleetwood Mac/Rumours (1977)/" + name,
			Status: models.LibraryItemStatusUnmatched,
		}
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("item: %v", err)
		}
		items = append(items, item)
	}
	return items
}

func itemIDs(items []models.LibraryItem) []uuid.UUID {
	ids := make([]uuid.UUID, len(items))
	for i, item := range items {
		ids[i] = item.ID
	}
	return ids
}

type previewResponse struct {
	Mappings []struct {
		ItemID           uuid.UUID `json:"item_id"`
		Path             string    `json:"path"`
		MBReleaseTrackID string    `json:"mb_release_track_id"`
		TrackTitle       string    `json:"track_title"`
		How              string    `json:"how"`
	} `json:"mappings"`
	Tracks []modules.ReleaseTrack `json:"tracks"`
}

// TestBulkPreviewProposesWithoutWriting is the guard that makes bulk attach safe:
// the preview is a proposal only. Nothing may be persisted until the user has
// reviewed it and called attach.
func TestBulkPreviewProposesWithoutWriting(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	items := seedBulkFixtures(t, api.DB)

	w := do(r, "POST", "/api/v1/attach/preview", token, map[string]any{
		"mb_release_id": "rel-bulk", "item_ids": itemIDs(items),
	})
	if w.Code != http.StatusOK {
		t.Fatalf("preview = %d: %s", w.Code, w.Body.String())
	}
	var got previewResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Mappings) != 3 || len(got.Tracks) != 3 {
		t.Fatalf("mappings/tracks = %d/%d, want 3/3", len(got.Mappings), len(got.Tracks))
	}
	// "02 Dreams.flac" was first in the request; the proposal follows the filename
	// number, not the request order.
	if got.Mappings[0].MBReleaseTrackID != "t2" || got.Mappings[0].How != modules.MapByNumber {
		t.Errorf("first mapping = %+v", got.Mappings[0])
	}
	if got.Mappings[1].MBReleaseTrackID != "t1" {
		t.Errorf("second mapping = %+v", got.Mappings[1])
	}

	for _, item := range items {
		var stored models.LibraryItem
		_ = api.DB.First(&stored, "id = ?", item.ID).Error
		if stored.MBReleaseID != "" || stored.Pinned {
			t.Fatalf("preview persisted a correlation: %+v", stored)
		}
	}
}

// TestBulkAttachAppliesReviewedMapping: the reviewed pairing is what gets written,
// including the derived recording ID.
func TestBulkAttachAppliesReviewedMapping(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	items := seedBulkFixtures(t, api.DB)

	w := do(r, "POST", "/api/v1/attach/bulk", token, map[string]any{
		"mb_release_id": "rel-bulk",
		"mappings": []map[string]any{
			{"item_id": items[0].ID, "mb_release_track_id": "t2"},
			{"item_id": items[1].ID, "mb_release_track_id": "t1"},
			{"item_id": items[2].ID, "mb_release_track_id": "t3"},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("bulk attach = %d: %s", w.Code, w.Body.String())
	}

	want := map[uuid.UUID][2]string{
		items[0].ID: {"t2", "rec-2"},
		items[1].ID: {"t1", "rec-1"},
		items[2].ID: {"t3", "rec-3"},
	}
	for id, pair := range want {
		var stored models.LibraryItem
		if err := api.DB.First(&stored, "id = ?", id).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if stored.MBReleaseTrackID != pair[0] || stored.MBRecordingID != pair[1] {
			t.Errorf("%s -> track %q rec %q, want %v", stored.Path, stored.MBReleaseTrackID, stored.MBRecordingID, pair)
		}
		if !stored.Pinned || stored.CorrelationSource != models.CorrelationSourceManual {
			t.Errorf("%s not pinned as manual: %+v", stored.Path, stored)
		}
		if stored.Status != models.LibraryItemStatusOK {
			t.Errorf("%s status = %q, want ok", stored.Path, stored.Status)
		}
	}
}

// TestBulkAttachRejectsWholeBatchOnBadTrack: the user reviewed the mapping as a
// whole, so a pairing that is not on the release must reject the batch rather than
// leave half an album attached.
func TestBulkAttachRejectsWholeBatchOnBadTrack(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	items := seedBulkFixtures(t, api.DB)

	w := do(r, "POST", "/api/v1/attach/bulk", token, map[string]any{
		"mb_release_id": "rel-bulk",
		"mappings": []map[string]any{
			{"item_id": items[0].ID, "mb_release_track_id": "t2"},
			{"item_id": items[1].ID, "mb_release_track_id": "track-from-another-album"},
		},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}

	// The valid pairing in the same batch must not have been applied.
	var stored models.LibraryItem
	_ = api.DB.First(&stored, "id = ?", items[0].ID).Error
	if stored.MBReleaseID != "" || stored.Pinned {
		t.Errorf("a rejected batch was partially applied: %+v", stored)
	}
}

// TestBulkAttachSkipsUnmappedFiles: leaving one file unidentified in the review
// step must not block attaching the rest of the album.
func TestBulkAttachSkipsUnmappedFiles(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	items := seedBulkFixtures(t, api.DB)

	w := do(r, "POST", "/api/v1/attach/bulk", token, map[string]any{
		"mb_release_id": "rel-bulk",
		"mappings": []map[string]any{
			{"item_id": items[0].ID, "mb_release_track_id": "t2"},
			{"item_id": items[1].ID, "mb_release_track_id": ""},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var body struct {
		Attached int `json:"attached"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Attached != 1 {
		t.Errorf("attached = %d, want 1", body.Attached)
	}

	var skipped models.LibraryItem
	_ = api.DB.First(&skipped, "id = ?", items[1].ID).Error
	if skipped.MBReleaseID != "" || skipped.Pinned {
		t.Errorf("a skipped file was attached anyway: %+v", skipped)
	}
}

// TestBulkAttachRejectsUnknownItem: an ID that is not in the index is a bug in the
// caller, not a file to silently drop.
func TestBulkAttachRejectsUnknownItem(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	items := seedBulkFixtures(t, api.DB)

	w := do(r, "POST", "/api/v1/attach/bulk", token, map[string]any{
		"mb_release_id": "rel-bulk",
		"mappings": []map[string]any{
			{"item_id": items[0].ID, "mb_release_track_id": "t1"},
			{"item_id": uuid.New(), "mb_release_track_id": "t2"},
		},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	var stored models.LibraryItem
	_ = api.DB.First(&stored, "id = ?", items[0].ID).Error
	if stored.Pinned {
		t.Errorf("batch with an unknown item was partially applied: %+v", stored)
	}
}

func TestBulkAttachRequiresReleaseAndFiles(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	items := seedBulkFixtures(t, api.DB)

	for _, body := range []map[string]any{
		{"mappings": []map[string]any{{"item_id": items[0].ID, "mb_release_track_id": "t1"}}},
		{"mb_release_id": "rel-bulk", "mappings": []map[string]any{}},
	} {
		if w := do(r, "POST", "/api/v1/attach/bulk", token, body); w.Code != http.StatusBadRequest {
			t.Errorf("body %v: status = %d, want 400", body, w.Code)
		}
	}
}

func TestBulkAttachRequiresAuth(t *testing.T) {
	r, api := setupAPI(t)
	items := seedBulkFixtures(t, api.DB)

	if w := do(r, "POST", "/api/v1/attach/bulk", "", map[string]any{
		"mb_release_id": "rel-bulk",
		"mappings":      []map[string]any{{"item_id": items[0].ID, "mb_release_track_id": "t1"}},
	}); w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

package routers

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// seedAttachFixtures puts a release in the MusicBrainz cache and an unmatched file
// in the index. Seeding the cache means the attach path never reaches the network.
func seedAttachFixtures(t *testing.T, db *gorm.DB) models.LibraryItem {
	t.Helper()
	modules.SetDB(db)
	t.Cleanup(func() { modules.SetDB(nil) })

	release := models.MusicBrainzReleaseResponse{
		ID:    "rel-1",
		Title: "Saturday Night Fever",
		Media: []models.MusicBrainzMedia{{
			Position: 1,
			Tracks: []models.Track{
				{ID: "t1", Title: "Stayin' Alive", Position: 1, Recording: recordingWithID("rec-1")},
				{ID: "t2", Title: "How Deep Is Your Love", Position: 2, Recording: recordingWithID("rec-2")},
			},
		}},
	}
	payload, _ := json.Marshal(release)
	if err := db.Create(&models.MusicbrainzReleaseCache{
		MBID: "rel-1", Payload: string(payload),
		FetchedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	if err := modules.MusicbrainzLoadCache(); err != nil {
		t.Fatalf("load cache: %v", err)
	}

	// Tag writing off: these tests are about what the attach decision persists, and
	// the fixture file does not exist on disk. The write path is covered separately
	// by TestAttachKeepsCorrelationWhenTaggingFails.
	profile := models.TaggerProfile{Name: "no-write", WriteTags: false}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("profile: %v", err)
	}
	lib := models.Library{Name: "L", Path: "/m", Enabled: true, TaggerProfileID: &profile.ID}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	item := models.LibraryItem{
		LibraryID: lib.ID, Path: "/m/Artist/Album/01.flac",
		Status: models.LibraryItemStatusUnmatched,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("item: %v", err)
	}
	return item
}

// recordingWithID builds the anonymous Recording struct models.Track embeds.
func recordingWithID(id string) (r struct {
	Genres []struct {
		Disambiguation string `json:"disambiguation"`
		ID             string `json:"id"`
		Name           string `json:"name"`
		Count          int    `json:"count"`
	} `json:"genres"`
	ISRCs            []string              `json:"isrcs"`
	FirstReleaseDate string                `json:"first-release-date"`
	Disambiguation   string                `json:"disambiguation"`
	ArtistCredit     []models.ArtistCredit `json:"artist-credit"`
	Video            bool                  `json:"video"`
	Length           int                   `json:"length"`
	Title            string                `json:"title"`
	ID               string                `json:"id"`
	Tags             []struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	} `json:"tags"`
}) {
	r.ID = id
	return r
}

// TestAttachPinsAndRecordsManualSource: attaching records the correlation, marks it
// manual, and pins it — the pin is what stops the next scan from undoing the
// decision via automatic resolution.
func TestAttachPinsAndRecordsManualSource(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	item := seedAttachFixtures(t, api.DB)

	w := do(r, "POST", "/api/v1/library-items/"+item.ID.String()+"/attach", token, map[string]string{
		"mb_release_id": "rel-1", "mb_release_track_id": "t2",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("attach = %d: %s", w.Code, w.Body.String())
	}

	var stored models.LibraryItem
	if err := api.DB.First(&stored, "id = ?", item.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.MBReleaseID != "rel-1" || stored.MBReleaseTrackID != "t2" {
		t.Errorf("correlation = %+v", stored)
	}
	// The recording ID must be derived from the release, not taken from the caller.
	if stored.MBRecordingID != "rec-2" {
		t.Errorf("recording id = %q, want rec-2", stored.MBRecordingID)
	}
	if !stored.Pinned {
		t.Error("attach did not pin the item")
	}
	if stored.CorrelationSource != models.CorrelationSourceManual {
		t.Errorf("source = %q, want manual", stored.CorrelationSource)
	}
	if stored.Status != models.LibraryItemStatusOK {
		t.Errorf("status = %q, want ok", stored.Status)
	}
}

// TestAttachRejectsTrackFromAnotherRelease is the guard that matters: the request
// body is not trusted, because a bad ID would be written into the file's tags.
func TestAttachRejectsTrackFromAnotherRelease(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	item := seedAttachFixtures(t, api.DB)

	w := do(r, "POST", "/api/v1/library-items/"+item.ID.String()+"/attach", token, map[string]string{
		"mb_release_id": "rel-1", "mb_release_track_id": "track-from-some-other-album",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}

	var stored models.LibraryItem
	_ = api.DB.First(&stored, "id = ?", item.ID).Error
	if stored.MBReleaseID != "" || stored.Pinned {
		t.Errorf("rejected attach still modified the item: %+v", stored)
	}
}

func TestAttachRequiresBothIDs(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	item := seedAttachFixtures(t, api.DB)

	for _, body := range []map[string]string{
		{"mb_release_id": "rel-1"},
		{"mb_release_track_id": "t1"},
		{},
	} {
		w := do(r, "POST", "/api/v1/library-items/"+item.ID.String()+"/attach", token, body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("body %v: status = %d, want 400", body, w.Code)
		}
	}
}

// TestDetachUnpins: detaching hands the file back to automatic resolution but keeps
// the correlation, since the already-written tags still describe that release.
func TestDetachUnpins(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	item := seedAttachFixtures(t, api.DB)

	if w := do(r, "POST", "/api/v1/library-items/"+item.ID.String()+"/attach", token, map[string]string{
		"mb_release_id": "rel-1", "mb_release_track_id": "t1",
	}); w.Code != http.StatusOK {
		t.Fatalf("attach = %d: %s", w.Code, w.Body.String())
	}
	if w := do(r, "DELETE", "/api/v1/library-items/"+item.ID.String()+"/attach", token, nil); w.Code != http.StatusOK {
		t.Fatalf("detach = %d: %s", w.Code, w.Body.String())
	}

	var stored models.LibraryItem
	_ = api.DB.First(&stored, "id = ?", item.ID).Error
	if stored.Pinned {
		t.Error("detach did not clear the pin")
	}
	if stored.MBReleaseID != "rel-1" {
		t.Errorf("detach discarded the correlation: %+v", stored)
	}
}

func TestAttachRequiresAuth(t *testing.T) {
	r, api := setupAPI(t)
	item := seedAttachFixtures(t, api.DB)

	if w := do(r, "POST", "/api/v1/library-items/"+item.ID.String()+"/attach", "", map[string]string{
		"mb_release_id": "rel-1", "mb_release_track_id": "t1",
	}); w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// TestReleaseTracksEndpoint serves the picker's tracklist from the cache.
func TestReleaseTracksEndpoint(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	seedAttachFixtures(t, api.DB)

	w := do(r, "GET", "/api/v1/releases/rel-1/tracks", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Tracks []modules.ReleaseTrack `json:"tracks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Tracks) != 2 || got.Tracks[0].Title != "Stayin' Alive" {
		t.Errorf("tracks = %+v", got.Tracks)
	}
}

// TestAttachKeepsCorrelationWhenTaggingFails: if the tag write fails (unreadable
// file, missing external binary), the correlation is still a real decision the user
// made, so it must survive — reported as 202 with a warning rather than lost.
func TestAttachKeepsCorrelationWhenTaggingFails(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	item := seedAttachFixtures(t, api.DB)

	// Re-enable writing for this library; the fixture path does not exist, so the
	// tagger will fail.
	if err := api.DB.Model(&models.TaggerProfile{}).Where("name = ?", "no-write").
		Update("write_tags", true).Error; err != nil {
		t.Fatalf("enable writes: %v", err)
	}

	w := do(r, "POST", "/api/v1/library-items/"+item.ID.String()+"/attach", token, map[string]string{
		"mb_release_id": "rel-1", "mb_release_track_id": "t1",
	})
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", w.Code, w.Body.String())
	}

	var stored models.LibraryItem
	_ = api.DB.First(&stored, "id = ?", item.ID).Error
	if stored.MBReleaseID != "rel-1" || !stored.Pinned {
		t.Errorf("correlation was lost after a tagging failure: %+v", stored)
	}
}

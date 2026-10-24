package routers

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
)

// heldPin turns the attach fixture into a pinned file whose pin is held: a real file
// on disk (so its audio can be hashed) carrying a hand-chosen identity.
func heldPin(t *testing.T, db *gorm.DB, item models.LibraryItem) models.LibraryItem {
	t.Helper()
	path := filepath.Join(t.TempDir(), "01.flac")
	if err := os.WriteFile(path, []byte("replaced audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&item).Updates(map[string]any{
		"path": path, "mb_release_id": "rel-1", "mb_release_track_id": "t1", "mb_recording_id": "rec-1",
		"pinned": true, "pin_held": true, "pinned_audio_hash": "old-audio",
		"status": models.LibraryItemStatusError, "error": "held for review",
	}).Error; err != nil {
		t.Fatalf("hold pin: %v", err)
	}
	item.Path = path
	return item
}

func reloadItem(t *testing.T, db *gorm.DB, item models.LibraryItem) models.LibraryItem {
	t.Helper()
	var stored models.LibraryItem
	if err := db.First(&stored, "id = ?", item.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	return stored
}

// Keeping a held pin confirms it for the audio the file holds now: the hold clears,
// the hash moves to the new audio, the identity stays the one chosen by hand.
func TestConfirmPinKeepsItForTheNewAudio(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	item := heldPin(t, api.DB, seedAttachFixtures(t, api.DB))

	w := do(r, "POST", "/api/v1/library-items/"+item.ID.String()+"/pin/confirm", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("confirm = %d: %s", w.Code, w.Body.String())
	}
	stored := reloadItem(t, api.DB, item)
	if stored.PinHeld || !stored.Pinned {
		t.Errorf("pinned=%t held=%t, want pinned and not held", stored.Pinned, stored.PinHeld)
	}
	if stored.PinnedAudioHash == "" || stored.PinnedAudioHash == "old-audio" {
		t.Errorf("hash = %q, want the new audio's", stored.PinnedAudioHash)
	}
	if stored.MBReleaseTrackID != "t1" || stored.Status != models.LibraryItemStatusOK || stored.Error != "" {
		t.Errorf("item = %+v, want the pin's identity, status ok, no error", stored)
	}
}

// Only a held pin can be confirmed.
func TestConfirmPinRefusesAFileWithNothingHeld(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	item := seedAttachFixtures(t, api.DB)

	if w := do(r, "POST", "/api/v1/library-items/"+item.ID.String()+"/pin/confirm", token, nil); w.Code != http.StatusConflict {
		t.Fatalf("confirm = %d, want 409", w.Code)
	}
}

// Releasing a held pin hands the file back to automatic resolution; the hold and the
// hash go with the pin, and the failed status stays so the next run resolves it.
func TestDetachReleasesAHeldPin(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	item := heldPin(t, api.DB, seedAttachFixtures(t, api.DB))

	if w := do(r, "DELETE", "/api/v1/library-items/"+item.ID.String()+"/attach", token, nil); w.Code != http.StatusOK {
		t.Fatalf("detach = %d: %s", w.Code, w.Body.String())
	}
	stored := reloadItem(t, api.DB, item)
	if stored.Pinned || stored.PinHeld || stored.PinnedAudioHash != "" {
		t.Errorf("pinned=%t held=%t hash=%q, want all cleared", stored.Pinned, stored.PinHeld, stored.PinnedAudioHash)
	}
	if stored.Status == models.LibraryItemStatusOK {
		t.Error("status reset to ok; the next run would skip the file instead of resolving it")
	}
}

// Attaching records the audio the pin was made for, and settles a hold.
func TestAttachRecordsThePinnedAudio(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	item := heldPin(t, api.DB, seedAttachFixtures(t, api.DB))

	if w := do(r, "POST", "/api/v1/library-items/"+item.ID.String()+"/attach", token, map[string]string{
		"mb_release_id": "rel-1", "mb_release_track_id": "t2",
	}); w.Code != http.StatusOK {
		t.Fatalf("attach = %d: %s", w.Code, w.Body.String())
	}
	stored := reloadItem(t, api.DB, item)
	if stored.PinHeld || stored.PinnedAudioHash == "" || stored.PinnedAudioHash == "old-audio" {
		t.Errorf("held=%t hash=%q, want settled with the new audio's hash", stored.PinHeld, stored.PinnedAudioHash)
	}
}

// The review list is a filter on the items listing.
func TestListLibraryItemsFiltersHeldPins(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	item := heldPin(t, api.DB, seedAttachFixtures(t, api.DB))
	if err := api.DB.Create(&models.LibraryItem{LibraryID: item.LibraryID, Path: "/m/other.flac", Status: models.LibraryItemStatusOK}).Error; err != nil {
		t.Fatal(err)
	}

	w := do(r, "GET", "/api/v1/library-items?pin_held=true", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", w.Code, w.Body.String())
	}
	var page struct {
		Total int `json:"total"`
		Items []struct {
			ID      string `json:"id"`
			PinHeld bool   `json:"pin_held"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != item.ID.String() || !page.Items[0].PinHeld {
		t.Errorf("page = %+v, want only the held pin", page)
	}
}

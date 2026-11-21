package process

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// TestCheckDuplicates: with nothing to check the press is refused; with candidates
// the job hashes them, records an event, and the duplicate reads as identical.
func TestCheckDuplicates(t *testing.T) {
	db := newTestDB(t)
	r := NewRunner(db, models.ConfigStruct{AutotaggerrVersion: "test"})
	t.Cleanup(r.Wait)

	if err := r.CheckDuplicates(); err != ErrNoDuplicatesToCheck {
		t.Fatalf("empty library: err = %v, want ErrNoDuplicatesToCheck", err)
	}

	lib := models.Library{Name: "music", Path: "/music", Enabled: true}
	db.Create(&lib)
	db.Create(&models.CollectionReleaseGroup{MBID: "rg", ArtistMBID: "a"})
	db.Create(&models.CollectionRelease{MBID: "rel-a", ReleaseGroupMBID: "rg"})
	db.Create(&models.CollectionRelease{MBID: "rel-b", ReleaseGroupMBID: "rg"})
	dir := t.TempDir()
	for name, release := range map[string]string{"a.flac": "rel-a", "b.flac": "rel-b"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("audio"), 0o644); err != nil {
			t.Fatal(err)
		}
		info, _ := os.Stat(p)
		mt := info.ModTime()
		db.Create(&models.LibraryItem{LibraryID: lib.ID, Path: p, Size: info.Size(), ModTime: &mt,
			MBReleaseID: release, MBRecordingID: "rec", Status: models.LibraryItemStatusOK})
	}

	previous := hashAudio
	hashAudio = func(string) (string, error) { return "same", nil }
	t.Cleanup(func() { hashAudio = previous })

	if err := r.CheckDuplicates(); err != nil {
		t.Fatalf("CheckDuplicates: %v", err)
	}
	r.waitIdle(t)

	var hashed int64
	db.Model(&models.LibraryItem{}).Where("audio_hash = ?", "same").Count(&hashed)
	if hashed != 2 {
		t.Errorf("%d files hashed, want 2", hashed)
	}
	var event models.Event
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if db.Where("type = ?", models.EventTypeDuplicateCheck).Limit(1).Find(&event).RowsAffected > 0 && event.Status != models.EventStatusRunning {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if event.Status != models.EventStatusOK || event.Summary == "" {
		t.Errorf("event = %q %q, want ok with a summary", event.Status, event.Summary)
	}
	if err := r.CheckDuplicates(); err != ErrNoDuplicatesToCheck {
		t.Errorf("after checking: err = %v, want nothing left to check", err)
	}
}

package scan

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// requireAudioTools skips the test when the audio tools are absent (they are
// installed in CI). Real audio is needed because the write path goes through
// metaflac.
func requireAudioTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "metaflac"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%q not on PATH; skipping", tool)
		}
	}
}

// synthFlacAt creates a short silent FLAC at path, making its directory first.
func synthFlacAt(t *testing.T, path string) string {
	t.Helper()
	requireAudioTools(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono", "-t", "0.1", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg synth failed: %v\n%s", err, out)
	}
	return path
}

// synthFlac creates a silent FLAC in a temp dir of its own.
func synthFlac(t *testing.T) string {
	t.Helper()
	return synthFlacAt(t, filepath.Join(t.TempDir(), "01 track.flac"))
}

// seedReleaseCache puts a release in the MusicBrainz cache so TagResolvedFile resolves
// it without a network call.
func seedReleaseCache(t *testing.T, db *gorm.DB, release models.MusicBrainzReleaseResponse) {
	t.Helper()
	modules.SetDB(db)
	t.Cleanup(func() { modules.SetDB(nil) })
	payload, _ := json.Marshal(release)
	if err := db.Create(&models.MusicbrainzReleaseCache{
		MBID: release.ID, Payload: string(payload),
		FetchedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	if err := modules.MusicbrainzLoadCache(); err != nil {
		t.Fatalf("load cache: %v", err)
	}
}

// TestRetagItemWritesTags drives the interactive re-tag through its full write path: a
// real FLAC, a write-enabled tagger profile, and a cached release the item correlates
// to. This covers retagItem's tag write, the DB update after it, and RetagItems'
// result aggregation — hermetically, because the release is served from the cache.
func TestRetagItemWritesTags(t *testing.T) {
	path := synthFlac(t)
	db := newTestDB(t)

	release := models.MusicBrainzReleaseResponse{
		ID: "rel-1", Title: "Album",
		ArtistCredit: []models.ArtistCredit{{Name: "Band", Artist: models.Artist{ID: "art-1", Name: "Band"}}},
		ReleaseGroup: models.ReleaseGroup{ID: "rg-1", Title: "Album", PrimaryType: "Album"},
		Media: []models.MusicBrainzMedia{{
			Position: 1,
			Tracks:   []models.Track{{ID: "trk-1", Title: "Song", Position: 1, Number: "1"}},
		}},
	}
	seedReleaseCache(t, db, release)

	profile := models.TaggerProfile{Name: "Write", WriteTags: true}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("create profile: %v", err)
	}
	lib := models.Library{Name: "L", Path: filepath.Dir(path), Enabled: true, TaggerProfileID: &profile.ID}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	item := models.LibraryItem{
		LibraryID: lib.ID, Path: path,
		MBReleaseID: "rel-1", MBReleaseTrackID: "trk-1", MBRecordingID: "rec-1",
		Status: models.LibraryItemStatusOK,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("create item: %v", err)
	}

	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})
	results, err := r.RetagItems([]uuid.UUID{item.ID})
	if err != nil {
		t.Fatalf("RetagItems: %v", err)
	}
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("result = %#v, want one success", results)
	}
	// A freshly synthesized FLAC has no MB tags, so writing the resolved ones is a
	// change: at least one tag written.
	if results[0].Written == 0 {
		t.Errorf("expected tags to be written to an untagged file, got 0")
	}

	// The item is stamped with the processed version after a successful re-tag.
	var reloaded models.LibraryItem
	if err := db.First(&reloaded, "id = ?", item.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.ProcessedVersion != "test" {
		t.Errorf("processed version = %q, want test", reloaded.ProcessedVersion)
	}
}

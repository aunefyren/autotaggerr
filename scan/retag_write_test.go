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

// retagFixture seeds the world a re-tag needs — a real FLAC, a cached release, a
// write-enabled profile and a library — and returns the item, ready to be re-tagged.
// The caller adjusts the item before it is created via mutate.
func retagFixture(t *testing.T, mutate func(*models.LibraryItem)) (*gorm.DB, models.LibraryItem) {
	t.Helper()
	path := synthFlac(t)
	db := newTestDB(t)

	seedReleaseCache(t, db, models.MusicBrainzReleaseResponse{
		ID: "rel-1", Title: "Album",
		ArtistCredit: []models.ArtistCredit{{Name: "Band", Artist: models.Artist{ID: "art-1", Name: "Band"}}},
		ReleaseGroup: models.ReleaseGroup{ID: "rg-1", Title: "Album", PrimaryType: "Album"},
		Media: []models.MusicBrainzMedia{{
			Position: 1,
			Tracks:   []models.Track{{ID: "trk-1", Title: "Song", Position: 1, Number: "1"}},
		}},
	})

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
	if mutate != nil {
		mutate(&item)
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("create item: %v", err)
	}
	return db, item
}

// TestRetagClearsStaleError covers the repair case: a file that failed during a scan
// and is then fixed by a re-tag must stop reporting the old failure. The re-tag path
// used to update only the timestamps and the processed version, so the row kept its
// error, its date and its transient flag long after the cause was gone — and the
// Items page kept calling a healthy file broken.
func TestRetagClearsStaleError(t *testing.T) {
	failedAt := time.Now().Add(-24 * time.Hour)
	db, item := retagFixture(t, func(i *models.LibraryItem) {
		i.Status = models.LibraryItemStatusError
		i.Error = "failed to get MB release data: service unavailable"
		i.LastErrorAt = &failedAt
		i.LastErrorTransient = true
	})

	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})
	results, err := r.RetagItems([]uuid.UUID{item.ID})
	if err != nil {
		t.Fatalf("RetagItems: %v", err)
	}
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("result = %#v, want one success", results)
	}

	var reloaded models.LibraryItem
	if err := db.First(&reloaded, "id = ?", item.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Status != models.LibraryItemStatusOK {
		t.Errorf("status = %q, want %q", reloaded.Status, models.LibraryItemStatusOK)
	}
	if reloaded.Error != "" {
		t.Errorf("error = %q, want it cleared", reloaded.Error)
	}
	if reloaded.LastErrorAt != nil {
		t.Errorf("last_error_at = %v, want nil", reloaded.LastErrorAt)
	}
	if reloaded.LastErrorTransient {
		t.Error("last_error_transient still set on a repaired file")
	}
}

// TestRetagRecordsFailure is the other half: a re-tag that fails must say so. The
// correlation points at a track the cached release does not contain, which is the
// hermetic version of a real disagreement between the manager's release and its
// track mapping — no network, no MusicBrainz stub.
func TestRetagRecordsFailure(t *testing.T) {
	db, item := retagFixture(t, func(i *models.LibraryItem) {
		i.MBReleaseTrackID = "trk-missing"
		i.ProcessedVersion = "test"
	})

	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "next"})
	results, err := r.RetagItems([]uuid.UUID{item.ID})
	if err != nil {
		t.Fatalf("RetagItems: %v", err)
	}
	if len(results) != 1 || results[0].Err == nil {
		t.Fatalf("result = %#v, want one failure", results)
	}

	var reloaded models.LibraryItem
	if err := db.First(&reloaded, "id = ?", item.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Status != models.LibraryItemStatusError {
		t.Errorf("status = %q, want %q", reloaded.Status, models.LibraryItemStatusError)
	}
	if reloaded.Error == "" {
		t.Error("no error text recorded for a failed re-tag")
	}
	if reloaded.LastErrorAt == nil {
		t.Error("last_error_at not dated on a failed re-tag")
	}
	// The version must stay stale so the next processing run re-attempts the file
	// for free, exactly as it does after a failed scan.
	if reloaded.ProcessedVersion != "test" {
		t.Errorf("processed version = %q, want it left at test", reloaded.ProcessedVersion)
	}
}

// TestRetagLeavesUnmatchedAlone guards the guard: an unmatched file has no
// correlation to rewrite from, and attempting the write anyway would relabel
// "the manager does not know this file" as an error.
func TestRetagLeavesUnmatchedAlone(t *testing.T) {
	db, item := retagFixture(t, func(i *models.LibraryItem) {
		i.Status = models.LibraryItemStatusUnmatched
		i.MBReleaseID = ""
		i.MBReleaseTrackID = ""
	})

	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})
	results, err := r.RetagItems([]uuid.UUID{item.ID})
	if err != nil {
		t.Fatalf("RetagItems: %v", err)
	}
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("result = %#v, want one no-op", results)
	}

	var reloaded models.LibraryItem
	if err := db.First(&reloaded, "id = ?", item.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Status != models.LibraryItemStatusUnmatched {
		t.Errorf("status = %q, want it left %q", reloaded.Status, models.LibraryItemStatusUnmatched)
	}
}

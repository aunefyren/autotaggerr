package components

import (
	"os"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/quality"
)

// TestScanMeasuresQuality: a scan records each file's quality, and a file skipped as
// unchanged is still measured when its row has none yet — a library indexed before
// quality existed — without being re-processed.
func TestScanMeasuresQuality(t *testing.T) {
	db, lib, path := scanFixture(t)
	var item models.LibraryItem
	db.Where("path = ?", path).First(&item)
	if item.Quality != string(quality.FLAC) || item.BitDepth != 16 || item.SampleRate == 0 || !qualityCurrent(item) {
		t.Fatalf("first scan: %+v", item)
	}

	// Forget the measurement, and poison the correlation so a re-process would show.
	if err := db.Model(&models.LibraryItem{}).Where("path = ?", path).Updates(map[string]any{
		"quality": "", "bit_depth": 0, "quality_size": 0, "quality_mod_time": nil, "mb_release_id": "SENTINEL",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := ScanLibrary(db, lib, nil, nil, "v1", 1); err != nil {
		t.Fatal(err)
	}
	db.Where("path = ?", path).First(&item)
	if item.MBReleaseID != "SENTINEL" {
		t.Fatal("the file was re-processed; it should have been skipped")
	}
	if item.Quality != string(quality.FLAC) || item.BitDepth != 16 || !qualityCurrent(item) {
		t.Errorf("a skipped file was not measured: %+v", item)
	}
}

// TestMeasureQualityUnreadable: audio that cannot be read is recorded as Unknown at
// its size and mtime, so the next run does not read it again.
func TestMeasureQualityUnreadable(t *testing.T) {
	path := t.TempDir() + "/broken.flac"
	if err := os.WriteFile(path, []byte("not flac"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	mod := fi.ModTime()
	item := models.LibraryItem{Path: path, Size: fi.Size(), ModTime: &mod}
	if !measureQuality(&item, path) || item.Quality != string(quality.Unknown) || !qualityCurrent(item) {
		t.Fatalf("unreadable: %+v", item)
	}
	if measureQuality(&item, path) {
		t.Error("a current measurement was taken again")
	}
	item.Size++
	if qualityCurrent(item) {
		t.Error("a size change should make the measurement stale")
	}
}

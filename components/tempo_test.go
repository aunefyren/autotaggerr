package components

import (
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// A tempo analysed after the file was last processed is the one reason an otherwise
// unchanged file must not be skipped: analysis never touches the file, so nothing
// else would ever bring it back to the tagger.
func TestTempoPending(t *testing.T) {
	db := testDB(t)
	library := models.Library{Name: "Test", Path: "/music"}
	if err := db.Create(&library).Error; err != nil {
		t.Fatal(err)
	}
	scanned := time.Now().Add(-time.Hour)
	item := models.LibraryItem{LibraryID: library.ID, Path: "/music/a.flac", LastScannedAt: &scanned}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}

	if tempoPending(db, item.Path) {
		t.Fatal("pending with no stored tempo")
	}

	row := models.TrackTempo{Path: item.Path, BPM: 128, Confidence: 0.9, AnalysedAt: scanned.Add(-time.Minute)}
	db.Create(&row)
	if tempoPending(db, item.Path) {
		t.Fatal("pending for a tempo the last scan already saw")
	}

	db.Model(&row).Update("analysed_at", scanned.Add(time.Minute))
	if !tempoPending(db, item.Path) {
		t.Fatal("not pending for a tempo analysed after the last scan")
	}

	// Processing the file re-stamps LastScannedAt, so the run after that skips it.
	recordItem(db, library.ID, item.Path, correlated(), true, "v-test", models.ManagerTypeAutotaggerr, nil)
	if tempoPending(db, item.Path) {
		t.Fatal("still pending after the file was processed")
	}

	// No measurable tempo has nothing to write, so it never holds a file back.
	db.Model(&row).Updates(map[string]any{"bpm": 0, "analysed_at": time.Now().Add(time.Hour)})
	if tempoPending(db, item.Path) {
		t.Fatal("pending for a tempo of zero")
	}

	if tempoPending(nil, item.Path) {
		t.Fatal("pending with no database")
	}
}

// The BPM policy is the library's, merged into what the writers read, and a floor of
// zero means the default — a row predating the column behaves like a new one.
func TestTaggerSettingsCarryTheLibraryBPMPolicy(t *testing.T) {
	profile := models.TaggerProfile{WriteTags: true}
	settings := NewTagger(profile).ForLibrary(models.Library{BPMWrite: models.BPMWriteFillEmpty, BPMMinConfidence: 0.7}).Settings()
	if settings.BPMWrite != models.BPMWriteFillEmpty || settings.BPMMinConfidence != 0.7 {
		t.Fatalf("settings = %+v", settings)
	}
	if got := NewTagger(profile).ForLibrary(models.Library{}).Settings().BPMMinConfidence; got != models.DefaultBPMMinConfidence {
		t.Errorf("unset floor = %v, want the default", got)
	}
	if NewTagger(profile).WritesBPM() {
		t.Error("a tagger without a library writes BPM")
	}
	if NewTagger(models.TaggerProfile{}).ForLibrary(models.Library{BPMWrite: models.BPMWriteOverwrite}).WritesBPM() {
		t.Error("a profile that writes no tags writes BPM")
	}
}

// TestLoudnessPending: a loudness measured after the file was last processed brings
// an otherwise unchanged file back to the tagger, once; a failed one does not.
func TestLoudnessPending(t *testing.T) {
	db := testDB(t)
	library := models.Library{Name: "Test", Path: "/music"}
	if err := db.Create(&library).Error; err != nil {
		t.Fatal(err)
	}
	scanned := time.Now().Add(-time.Hour)
	item := models.LibraryItem{LibraryID: library.ID, Path: "/music/a.flac", LastScannedAt: &scanned}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	if loudnessPending(db, item.Path) {
		t.Fatal("pending with nothing stored")
	}
	row := models.TrackLoudness{Path: item.Path, TrackGain: -6, AnalysedAt: scanned.Add(time.Minute)}
	db.Create(&row)
	if !loudnessPending(db, item.Path) {
		t.Fatal("not pending for a loudness measured after the last scan")
	}
	recordItem(db, library.ID, item.Path, correlated(), true, "v-test", models.ManagerTypeAutotaggerr, nil)
	if loudnessPending(db, item.Path) {
		t.Fatal("still pending after the file was processed")
	}
	db.Model(&row).Updates(map[string]any{"error": "rsgain failed", "analysed_at": time.Now().Add(time.Hour)})
	if loudnessPending(db, item.Path) {
		t.Fatal("pending for a failed analysis")
	}
	if loudnessPending(nil, item.Path) {
		t.Fatal("pending with no database")
	}
}

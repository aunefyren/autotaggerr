package components

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
)

// The pin tests write plain files shaped "<tags>|<audio>" and hash only the part after
// the bar, which is all AudioPayloadHash promises: a tag write changes the bytes and
// the size, and leaves the hash alone; a replaced file changes both.
func fakeAudioHash(t *testing.T) {
	t.Helper()
	orig := audioPayloadHash
	audioPayloadHash = func(path string) (string, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		_, audio, _ := strings.Cut(string(b), "|")
		return "hash:" + audio, nil
	}
	t.Cleanup(func() { audioPayloadHash = orig })
}

func writeFile(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// seedPin indexes path as a pinned file, as it was when last processed: the size and
// mtime are the file's at the moment of the call.
func seedPin(t *testing.T, db *gorm.DB, path, hash string) (models.Library, models.LibraryItem) {
	t.Helper()
	library := models.Library{Name: "Test", Path: filepath.Dir(path)}
	if err := db.Create(&library).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	mod := fi.ModTime()
	item := models.LibraryItem{
		LibraryID: library.ID, Path: path, Size: fi.Size(), ModTime: &mod,
		MBReleaseID: "rel-pinned", MBReleaseTrackID: "trk-pinned", MBRecordingID: "rec-pinned",
		CorrelationSource: models.CorrelationSourceManual, Pinned: true, PinnedAudioHash: hash,
		Status: models.LibraryItemStatusOK, ProcessedVersion: "v-test",
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("create pinned item: %v", err)
	}
	return library, item
}

func reload(t *testing.T, db *gorm.DB, path string) models.LibraryItem {
	t.Helper()
	var item models.LibraryItem
	if err := db.Where("path = ?", path).First(&item).Error; err != nil {
		t.Fatalf("load item: %v", err)
	}
	return item
}

func processPinned(t *testing.T, db *gorm.DB, library models.Library, path string) (*spyManager, error) {
	t.Helper()
	mgr := &spyManager{}
	tagger := NewTagger(models.TaggerProfile{WriteTags: false})
	_, _, err := ProcessFile(db, library, mgr, tagger, nil, nil, nil, path, filepath.Dir(path), "v-test")
	return mgr, err
}

// The case the hold exists for: the audio under a pin was replaced. The pin is neither
// applied nor dropped — the file waits for review, keeps its identity on the row, and
// the manager is still not consulted.
func TestProcessFileHoldsAPinWhoseAudioWasReplaced(t *testing.T) {
	fakeAudioHash(t)
	db := testDB(t)
	path := filepath.Join(t.TempDir(), "01 track.flac")
	writeFile(t, path, "tags|audio-a", time.Now().Add(-time.Hour))
	library, _ := seedPin(t, db, path, "hash:audio-a")

	writeFile(t, path, "tags|audio-b-upgraded", time.Now())
	mgr, err := processPinned(t, db, library, path)

	if !errors.Is(err, ErrPinHeld) {
		t.Fatalf("ProcessFile error = %v, want ErrPinHeld", err)
	}
	if mgr.calls.Load() != 0 {
		t.Error("the manager was consulted for a held pin")
	}
	item := reload(t, db, path)
	if !item.PinHeld || !item.Pinned {
		t.Errorf("pinned=%t held=%t, want both", item.Pinned, item.PinHeld)
	}
	if item.MBReleaseID != "rel-pinned" {
		t.Errorf("identity = %q, want the pin's kept while held", item.MBReleaseID)
	}
	if item.Status != models.LibraryItemStatusError || !strings.Contains(item.Error, "held for review") {
		t.Errorf("status=%q error=%q, want the hold recorded", item.Status, item.Error)
	}

	// Held, it is passed over by the next run and left out of every tag write.
	if !shouldSkip(db, path, "v-test", models.ManagerTypeAutotaggerr, "") {
		t.Error("a held pin was not skipped")
	}
	var taggable int64
	db.Model(&models.LibraryItem{}).Scopes(models.TaggableItems).Count(&taggable)
	if taggable != 0 {
		t.Errorf("%d held item(s) still taggable, want 0", taggable)
	}
}

// Our own tag write changes the bytes too. The audio did not change, so the pin applies.
func TestProcessFileAppliesAPinAfterATagWrite(t *testing.T) {
	fakeAudioHash(t)
	db := testDB(t)
	path := filepath.Join(t.TempDir(), "01 track.flac")
	writeFile(t, path, "tags|audio-a", time.Now().Add(-time.Hour))
	library, _ := seedPin(t, db, path, "hash:audio-a")

	writeFile(t, path, "longer rewritten tags|audio-a", time.Now())
	if _, err := processPinned(t, db, library, path); err != nil {
		t.Fatalf("ProcessFile: %v", err)
	}
	if item := reload(t, db, path); item.PinHeld || item.MBReleaseID != "rel-pinned" {
		t.Errorf("held=%t release=%q, want the pin applied", item.PinHeld, item.MBReleaseID)
	}
}

// A pin made before hashes were recorded adopts the audio it finds. Nothing recorded
// what the audio used to be, so there is nothing to hold it against.
func TestProcessFileAdoptsTheAudioOfAnUnhashedPin(t *testing.T) {
	fakeAudioHash(t)
	db := testDB(t)
	path := filepath.Join(t.TempDir(), "01 track.flac")
	writeFile(t, path, "tags|audio-a", time.Now().Add(-time.Hour))
	library, _ := seedPin(t, db, path, "")

	writeFile(t, path, "tags|audio-b", time.Now())
	if _, err := processPinned(t, db, library, path); err != nil {
		t.Fatalf("ProcessFile: %v", err)
	}
	if item := reload(t, db, path); item.PinHeld || item.PinnedAudioHash != "hash:audio-b" {
		t.Errorf("held=%t hash=%q, want adopted hash:audio-b", item.PinHeld, item.PinnedAudioHash)
	}
}

// The backfill hashes only pins whose file is still what the index recorded — the only
// ones whose current audio is known to be the audio the pin was made for.
func TestBackfillPinnedAudioHashes(t *testing.T) {
	fakeAudioHash(t)
	db := testDB(t)
	dir := t.TempDir()
	same := filepath.Join(dir, "01 same.flac")
	moved := filepath.Join(dir, "02 moved.flac")
	writeFile(t, same, "tags|audio-same", time.Now().Add(-time.Hour))
	writeFile(t, moved, "tags|audio-old", time.Now().Add(-time.Hour))
	seedPin(t, db, same, "")
	library := models.Library{}
	db.First(&library)
	fi, _ := os.Stat(moved)
	mod := fi.ModTime()
	if err := db.Create(&models.LibraryItem{LibraryID: library.ID, Path: moved, Size: fi.Size(), ModTime: &mod,
		MBReleaseID: "rel", Pinned: true, Status: models.LibraryItemStatusOK}).Error; err != nil {
		t.Fatal(err)
	}
	writeFile(t, moved, "tags|audio-replaced-since", time.Now())

	if n := BackfillPinnedAudioHashes(db); n != 1 {
		t.Errorf("backfilled %d, want 1", n)
	}
	if got := reload(t, db, same).PinnedAudioHash; got != "hash:audio-same" {
		t.Errorf("unchanged pin hash = %q", got)
	}
	if got := reload(t, db, moved).PinnedAudioHash; got != "" {
		t.Errorf("changed pin hash = %q, want left empty", got)
	}
	if n := BackfillPinnedAudioHashes(db); n != 0 {
		t.Errorf("second backfill filled %d, want 0 (idempotent)", n)
	}
}

// freshSpy is a manager with a cache: it records which of its two doors a file came in.
type freshSpy struct {
	plain, fresh atomic.Int32
}

func (m *freshSpy) Correlate(string, string) (models.Correlation, error) {
	m.plain.Add(1)
	return models.Correlation{MBReleaseID: "rel", MBReleaseTrackID: "trk"}, nil
}
func (m *freshSpy) CorrelateFresh(string, string) (models.Correlation, error) {
	m.fresh.Add(1)
	return models.Correlation{MBReleaseID: "rel", MBReleaseTrackID: "trk"}, nil
}
func (m *freshSpy) HealthCheck() (bool, error) { return true, nil }
func (m *freshSpy) Type() string               { return models.ManagerTypeLidarr }

// A file the index has not seen in its current state is resolved fresh; one the index
// recorded exactly (re-processed for another reason — a version bump) uses the cache.
func TestProcessFileResolvesChangedFilesFresh(t *testing.T) {
	db := testDB(t)
	dir := t.TempDir()
	library := models.Library{Name: "Test", Path: dir}
	if err := db.Create(&library).Error; err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "01 track.flac")
	writeFile(t, path, "audio", time.Now().Add(-time.Hour))
	tagger := NewTagger(models.TaggerProfile{WriteTags: false})
	mgr := &freshSpy{}

	// Not indexed yet: fresh.
	if _, _, err := ProcessFile(db, library, mgr, tagger, nil, nil, nil, path, dir, "v1"); err != nil {
		t.Fatal(err)
	}
	// Indexed as it is now, re-processed under a new version: cached answers are fine.
	if _, _, err := ProcessFile(db, library, mgr, tagger, nil, nil, nil, path, dir, "v2"); err != nil {
		t.Fatal(err)
	}
	// Replaced on disk: fresh again.
	writeFile(t, path, "different audio", time.Now())
	if _, _, err := ProcessFile(db, library, mgr, tagger, nil, nil, nil, path, dir, "v2"); err != nil {
		t.Fatal(err)
	}

	if mgr.fresh.Load() != 2 || mgr.plain.Load() != 1 {
		t.Errorf("fresh=%d plain=%d, want 2 and 1", mgr.fresh.Load(), mgr.plain.Load())
	}
}

func withReplacedPinPolicy(t *testing.T, policy string) {
	t.Helper()
	SetReplacedPinPolicy(policy)
	t.Cleanup(func() { SetReplacedPinPolicy("") })
}

// Keep: the pin is confirmed for the new audio and applied, and the run says so.
func TestReplacedPinPolicyKeep(t *testing.T) {
	fakeAudioHash(t)
	withReplacedPinPolicy(t, models.ReplacedPinKeep)
	db := testDB(t)
	path := filepath.Join(t.TempDir(), "01 track.flac")
	writeFile(t, path, "tags|audio-a", time.Now().Add(-time.Hour))
	library, _ := seedPin(t, db, path, "hash:audio-a")
	writeFile(t, path, "tags|audio-b", time.Now())

	mgr := &spyManager{}
	detail := NewDetailCollector(10)
	if _, _, err := ProcessFile(db, library, mgr, NewTagger(models.TaggerProfile{}), nil, nil, detail, path, filepath.Dir(path), "v-test"); err != nil {
		t.Fatalf("ProcessFile: %v", err)
	}
	item := reload(t, db, path)
	if item.PinHeld || !item.Pinned || item.PinnedAudioHash != "hash:audio-b" || item.MBReleaseID != "rel-pinned" {
		t.Errorf("item = held %t pinned %t hash %q release %q; want the pin kept for audio-b",
			item.PinHeld, item.Pinned, item.PinnedAudioHash, item.MBReleaseID)
	}
	if mgr.calls.Load() != 0 {
		t.Error("the manager was consulted for a kept pin")
	}
	if w := ReplacedPinWarning(detail); !strings.Contains(w, "1 pinned file(s) had new audio and kept their pin") || !strings.Contains(w, path) {
		t.Errorf("warning = %q", w)
	}
}

// Release: the pin is dropped and the file identified by its manager in the same pass.
func TestReplacedPinPolicyRelease(t *testing.T) {
	fakeAudioHash(t)
	withReplacedPinPolicy(t, models.ReplacedPinRelease)
	db := testDB(t)
	path := filepath.Join(t.TempDir(), "01 track.flac")
	writeFile(t, path, "tags|audio-a", time.Now().Add(-time.Hour))
	library, _ := seedPin(t, db, path, "hash:audio-a")
	writeFile(t, path, "tags|audio-b", time.Now())

	mgr := &spyManager{}
	detail := NewDetailCollector(10)
	if _, _, err := ProcessFile(db, library, mgr, NewTagger(models.TaggerProfile{}), nil, nil, detail, path, filepath.Dir(path), "v-test"); err != nil {
		t.Fatalf("ProcessFile: %v", err)
	}
	item := reload(t, db, path)
	if item.Pinned || item.PinHeld || item.PinnedAudioHash != "" {
		t.Errorf("pinned=%t held=%t hash=%q, want all cleared", item.Pinned, item.PinHeld, item.PinnedAudioHash)
	}
	if mgr.calls.Load() != 1 || item.MBReleaseID != "rel-auto" {
		t.Errorf("manager calls=%d release=%q, want the manager's answer", mgr.calls.Load(), item.MBReleaseID)
	}
	if w := ReplacedPinWarning(detail); !strings.Contains(w, "released to their manager") {
		t.Errorf("warning = %q", w)
	}
}

// Switching to an automatic policy settles the pins already held: they are no longer
// skipped, and the next pass decides them.
func TestReplacedPinPolicySettlesPinsAlreadyHeld(t *testing.T) {
	fakeAudioHash(t)
	db := testDB(t)
	path := filepath.Join(t.TempDir(), "01 track.flac")
	writeFile(t, path, "tags|audio-a", time.Now().Add(-time.Hour))
	library, _ := seedPin(t, db, path, "hash:audio-a")
	writeFile(t, path, "tags|audio-b", time.Now())

	if _, err := processPinned(t, db, library, path); !errors.Is(err, ErrPinHeld) {
		t.Fatalf("under review: err = %v, want held", err)
	}
	if !shouldSkip(db, path, "v-test", models.ManagerTypeAutotaggerr, "") {
		t.Fatal("a held pin was not skipped under review")
	}

	withReplacedPinPolicy(t, models.ReplacedPinKeep)
	if shouldSkip(db, path, "v-test", models.ManagerTypeAutotaggerr, "") {
		t.Fatal("a held pin is still skipped under keep")
	}
	if _, err := processPinned(t, db, library, path); err != nil {
		t.Fatalf("under keep: %v", err)
	}
	if item := reload(t, db, path); item.PinHeld || item.PinnedAudioHash != "hash:audio-b" || item.Status != models.LibraryItemStatusOK {
		t.Errorf("held=%t hash=%q status=%q, want settled", item.PinHeld, item.PinnedAudioHash, item.Status)
	}
}

// Keeping needs the new audio's hash; a file whose audio cannot be read is held instead.
func TestReplacedPinPolicyKeepHoldsWhenTheAudioCannotBeRead(t *testing.T) {
	fakeAudioHash(t)
	withReplacedPinPolicy(t, models.ReplacedPinKeep)
	db := testDB(t)
	path := filepath.Join(t.TempDir(), "01 track.flac")
	writeFile(t, path, "tags|audio-a", time.Now().Add(-time.Hour))
	library, _ := seedPin(t, db, path, "hash:audio-a")
	if err := db.Model(&models.LibraryItem{}).Where("path = ?", path).Update("pin_held", true).Error; err != nil {
		t.Fatal(err)
	}
	audioPayloadHash = func(string) (string, error) { return "", errors.New("unreadable") }

	if _, err := processPinned(t, db, library, path); !errors.Is(err, ErrPinHeld) {
		t.Fatalf("err = %v, want the pin held", err)
	}
}

func TestReplacedPinWarningIsQuietWhenNothingHappened(t *testing.T) {
	if w := ReplacedPinWarning(NewDetailCollector(10)); w != "" {
		t.Errorf("warning = %q, want none", w)
	}
	d := NewDetailCollector(10)
	for i := 0; i < maxReplacedPinPaths+2; i++ {
		d.AddReplacedPin("/m/f", true)
	}
	if w := ReplacedPinWarning(d); !strings.Contains(w, "12 pinned file(s)") || !strings.Contains(w, "and 2 more") {
		t.Errorf("warning = %q", w)
	}
}

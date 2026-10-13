package tempo

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/database"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func init() {
	logger.Log = logrus.New()
	logger.Log.SetOutput(io.Discard)
}

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := database.Connect(models.DatabaseConfig{
		Type: "sqlite",
		DSN:  filepath.Join(t.TempDir(), "tempo.db"),
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return db
}

// fake stands in for the detector and the hash. The hash is the file's *content*, so
// a test changes the audio by rewriting the file and changes only its identity by
// touching it — the same distinction the real hash draws between frames and tags.
type fake struct {
	mu      sync.Mutex
	calls   []string
	bpm     map[string]float64 // by content; absent means 128
	failFor map[string]error   // by content
	block   chan struct{}      // when set, every detect waits on it
}

func (f *fake) detect(ctx context.Context, path string) (modules.TempoAnalysis, error) {
	f.mu.Lock()
	f.calls = append(f.calls, path)
	block := f.block
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return modules.TempoAnalysis{}, ctx.Err()
		}
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return modules.TempoAnalysis{}, err
	}
	if err := f.failFor[string(content)]; err != nil {
		return modules.TempoAnalysis{Beats: 3}, err
	}
	bpm, ok := f.bpm[string(content)]
	if !ok {
		bpm = 128
	}
	return modules.TempoAnalysis{BPM: bpm, Confidence: 0.9, Beats: 400}, nil
}

func (f *fake) hash(path string) (string, error) {
	content, err := os.ReadFile(path)
	return "h:" + string(content), err
}

func (f *fake) detected() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fake) reset() {
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
}

func newTestRunner(t *testing.T, db *gorm.DB) (*Runner, *fake) {
	t.Helper()
	f := &fake{bpm: map[string]float64{}, failFor: map[string]error{}}
	r := newRunner(db, nil, models.ConfigStruct{})
	r.detect = f.detect
	r.hash = f.hash
	r.available = func() bool { return true }
	r.concurrency = func() int { return 2 }
	r.pollEvery = 5 * time.Millisecond
	return r, f
}

type fixture struct {
	db      *gorm.DB
	dir     string
	library models.Library
}

func newFixture(t *testing.T, analyse bool) fixture {
	t.Helper()
	db := testDB(t)
	dir := t.TempDir()
	library := models.Library{Name: "Music", Path: dir, Enabled: true, AnalyseTempo: analyse}
	if err := db.Create(&library).Error; err != nil {
		t.Fatal(err)
	}
	return fixture{db: db, dir: dir, library: library}
}

// add writes a file whose content stands for its audio, and indexes it.
func (fx fixture) add(t *testing.T, name, audio string) string {
	t.Helper()
	path := filepath.Join(fx.dir, name)
	if err := os.WriteFile(path, []byte(audio), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fx.db.Create(&models.LibraryItem{LibraryID: fx.library.ID, Path: path}).Error; err != nil {
		t.Fatal(err)
	}
	return path
}

func stored(t *testing.T, db *gorm.DB, path string) models.TrackTempo {
	t.Helper()
	var row models.TrackTempo
	if err := db.First(&row, "path = ?", path).Error; err != nil {
		t.Fatalf("no stored tempo for %s: %v", filepath.Base(path), err)
	}
	return row
}

func lastEvent(t *testing.T, db *gorm.DB) models.Event {
	t.Helper()
	var ev models.Event
	if err := db.Where("type = ?", models.EventTypeTempo).Order("started_at desc").First(&ev).Error; err != nil {
		t.Fatalf("no tempo event: %v", err)
	}
	return ev
}

func stat(ev models.Event, label string) int {
	for _, s := range ev.Stats {
		if s.Label == label {
			return s.Value
		}
	}
	return -1
}

func TestPassAnalysesAndStoresRawResults(t *testing.T) {
	fx := newFixture(t, true)
	r, f := newTestRunner(t, fx.db)
	fast := fx.add(t, "fast.flac", "a")
	hiphop := fx.add(t, "hiphop.mp3", "b")
	fx.add(t, "cover.jpg", "not audio")
	f.bpm["a"] = 160
	f.bpm["b"] = 176.4 // a double-time read of ~88: stored as reported, never corrected

	r.run()

	if got := len(f.detected()); got != 2 {
		t.Fatalf("detector ran %d times, want 2 (the image is not audio)", got)
	}
	row := stored(t, fx.db, fast)
	if row.BPM != 160 || row.Confidence != 0.9 || row.Detector != modules.TempoDetector ||
		row.DetectorVersion != modules.TempoDetectorVersion() || row.AudioHash != "h:a" || row.ModTime == nil {
		t.Fatalf("stored row = %+v", row)
	}
	if got := stored(t, fx.db, hiphop).BPM; got != 176.4 {
		t.Fatalf("BPM = %v, want the raw 176.4", got)
	}

	ev := lastEvent(t, fx.db)
	if ev.Status != models.EventStatusOK || stat(ev, "Analysed") != 2 || stat(ev, "Files checked") != 2 {
		t.Fatalf("event = %s %q %+v", ev.Status, ev.Summary, ev.Stats)
	}
}

func TestLibrariesThatDidNotOptInAreLeftAlone(t *testing.T) {
	fx := newFixture(t, false)
	r, f := newTestRunner(t, fx.db)
	fx.add(t, "a.flac", "a")

	if err := r.Check(); !errors.Is(err, ErrNothingOptedIn) {
		t.Fatalf("Check = %v, want ErrNothingOptedIn", err)
	}
	r.run()
	if len(f.detected()) != 0 {
		t.Fatal("a library that did not opt in was analysed")
	}
	var count int64
	fx.db.Model(&models.Event{}).Where("type = ?", models.EventTypeTempo).Count(&count)
	if count != 0 {
		t.Fatal("a pass with nothing to do recorded an event")
	}

	// A disabled library is not analysed even when it opted in.
	fx.db.Model(&fx.library).Updates(map[string]any{"analyse_tempo": true, "enabled": false})
	if err := r.Check(); !errors.Is(err, ErrNothingOptedIn) {
		t.Fatalf("Check = %v for a disabled library, want ErrNothingOptedIn", err)
	}
}

func TestCheckReportsAMissingDetector(t *testing.T) {
	fx := newFixture(t, true)
	r, _ := newTestRunner(t, fx.db)
	r.available = func() bool { return false }
	if err := r.Check(); !errors.Is(err, ErrDetectorMissing) {
		t.Fatalf("Check = %v, want ErrDetectorMissing", err)
	}
	if r.Status().DetectorAvailable {
		t.Fatal("status claims the detector is available")
	}
}

func TestUnchangedFilesAreNeverReanalysed(t *testing.T) {
	fx := newFixture(t, true)
	r, f := newTestRunner(t, fx.db)
	fx.add(t, "a.flac", "a")
	r.run()
	f.reset()

	r.run()
	if n := len(f.detected()); n != 0 {
		t.Fatalf("detector ran %d times on an unchanged library", n)
	}
	if got := stat(lastEvent(t, fx.db), "Already analysed"); got != 1 {
		t.Fatalf("Already analysed = %d, want 1", got)
	}
}

// The BPM tag write changes size and mtime. The audio did not change, so the result
// is re-stamped, not recomputed — without this every write would cost a re-analysis.
func TestATagWriteIsRestampedNotReanalysed(t *testing.T) {
	fx := newFixture(t, true)
	r, f := newTestRunner(t, fx.db)
	path := fx.add(t, "a.flac", "a")
	r.run()
	f.reset()

	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	r.run()

	if n := len(f.detected()); n != 0 {
		t.Fatalf("detector ran %d times after a change that left the audio alone", n)
	}
	if row := stored(t, fx.db, path); !row.ModTime.Equal(later) {
		t.Fatalf("mod_time = %v, want it re-stamped to %v", row.ModTime, later)
	}
	if got := stat(lastEvent(t, fx.db), "Tags changed, audio did not"); got != 1 {
		t.Fatalf("restamped = %d, want 1", got)
	}

	// And the pass after that sees the new stamp as fresh.
	r.run()
	if n := len(f.detected()); n != 0 {
		t.Fatal("a re-stamped file was analysed on the following pass")
	}
}

func TestNewAudioIsReanalysed(t *testing.T) {
	fx := newFixture(t, true)
	r, f := newTestRunner(t, fx.db)
	path := fx.add(t, "a.flac", "a")
	r.run()
	f.reset()

	f.bpm["re-encoded"] = 92
	if err := os.WriteFile(path, []byte("re-encoded"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.run()
	if n := len(f.detected()); n != 1 {
		t.Fatalf("detector ran %d times after the audio changed, want 1", n)
	}
	if got := stored(t, fx.db, path).BPM; got != 92 {
		t.Fatalf("BPM = %v, want the new audio's 92", got)
	}
}

func TestAMovedFileKeepsItsResult(t *testing.T) {
	fx := newFixture(t, true)
	r, f := newTestRunner(t, fx.db)
	old := fx.add(t, "old.flac", "a")
	f.bpm["a"] = 101
	r.run()
	f.reset()

	// The scan moved the file: the old index row is gone, a new one points at the
	// same audio under a new name.
	moved := filepath.Join(fx.dir, "new.flac")
	if err := os.Rename(old, moved); err != nil {
		t.Fatal(err)
	}
	fx.db.Where("path = ?", old).Delete(&models.LibraryItem{})
	fx.db.Create(&models.LibraryItem{LibraryID: fx.library.ID, Path: moved})

	r.run()
	if n := len(f.detected()); n != 0 {
		t.Fatalf("detector ran %d times for a file that only moved", n)
	}
	if got := stored(t, fx.db, moved).BPM; got != 101 {
		t.Fatalf("moved file BPM = %v, want the carried 101", got)
	}
	var count int64
	fx.db.Model(&models.TrackTempo{}).Where("path = ?", old).Count(&count)
	if count != 0 {
		t.Fatal("the old path's result was not pruned after a completed pass")
	}
}

// A result from another revision of the analysis is not current, whatever the file
// looks like — bumping the revision is how a changed reduction redoes the library.
func TestAnotherDetectorVersionIsReanalysed(t *testing.T) {
	fx := newFixture(t, true)
	r, f := newTestRunner(t, fx.db)
	path := fx.add(t, "a.flac", "a")
	r.run()
	fx.db.Model(&models.TrackTempo{}).Where("path = ?", path).Update("detector_version", "default/r0")
	f.reset()

	r.run()
	if n := len(f.detected()); n != 1 {
		t.Fatalf("detector ran %d times for a stale version, want 1", n)
	}
	if got := stored(t, fx.db, path).DetectorVersion; got != modules.TempoDetectorVersion() {
		t.Fatalf("version = %q after re-analysis", got)
	}
}

// No measurable tempo is an answer, not an error, and is not retried every night.
// A real failure is listed in the event, and is not retried either until the file
// changes.
func TestFailuresAreRecordedAndNotRetriedUntilTheFileChanges(t *testing.T) {
	fx := newFixture(t, true)
	r, f := newTestRunner(t, fx.db)
	silent := fx.add(t, "skit.flac", "silence")
	broken := fx.add(t, "broken.flac", "garbage")
	f.failFor["silence"] = modules.ErrTooFewBeats
	f.failFor["garbage"] = errors.New("aubiotrack failed: could not open input file")

	r.run()
	ev := lastEvent(t, fx.db)
	if stat(ev, "No measurable tempo") != 1 || stat(ev, "Failed") != 1 {
		t.Fatalf("stats = %+v", ev.Stats)
	}
	var items []models.EventItem
	fx.db.Where("event_id = ?", ev.ID).Find(&items)
	if len(items) != 1 || items[0].Path != broken || !strings.Contains(items[0].Error, "could not open") {
		t.Fatalf("detail rows = %+v, want only the real failure", items)
	}
	if row := stored(t, fx.db, silent); row.BPM != 0 || row.Error == "" {
		t.Fatalf("no-tempo row = %+v, want BPM 0 with the reason", row)
	}

	f.reset()
	r.run()
	if n := len(f.detected()); n != 0 {
		t.Fatalf("detector re-ran %d unchanged failures", n)
	}
}

// A file the index still holds but the disk does not is the scan's to prune.
func TestAGoneFileIsSkippedNotFailed(t *testing.T) {
	fx := newFixture(t, true)
	r, f := newTestRunner(t, fx.db)
	path := fx.add(t, "a.flac", "a")
	os.Remove(path)

	r.run()
	if len(f.detected()) != 0 || stat(lastEvent(t, fx.db), "Failed") != 0 {
		t.Fatal("a missing file was analysed or counted as a failure")
	}
}

// A pass pauses while files are being written and resumes when they are done.
func TestPassYieldsToFileWritingWork(t *testing.T) {
	fx := newFixture(t, true)
	r, f := newTestRunner(t, fx.db)
	fx.add(t, "a.flac", "a")

	var writing atomic.Bool
	writing.Store(true)
	r.yieldTo = writing.Load

	done := make(chan struct{})
	go func() { r.run(); close(done) }()

	waitFor(t, func() bool { return r.Status().Paused })
	if len(f.detected()) != 0 {
		t.Fatal("analysed while files were being written")
	}
	writing.Store(false)
	<-done
	if len(f.detected()) != 1 {
		t.Fatal("the pass did not resume once file work finished")
	}
}

// Cancelling stores nothing for the file in flight, and the next pass resumes.
func TestCancelStopsAndTheNextPassResumes(t *testing.T) {
	fx := newFixture(t, true)
	r, f := newTestRunner(t, fx.db)
	r.concurrency = func() int { return 1 }
	first := fx.add(t, "a.flac", "a")
	fx.add(t, "b.flac", "b")
	f.block = make(chan struct{})

	done := make(chan struct{})
	go func() { r.run(); close(done) }()
	waitFor(t, func() bool { return len(f.detected()) == 1 })
	r.Cancel()
	<-done

	var count int64
	fx.db.Model(&models.TrackTempo{}).Count(&count)
	if count != 0 {
		t.Fatalf("%d results stored by a pass cancelled mid-decode", count)
	}
	if ev := lastEvent(t, fx.db); !strings.HasPrefix(ev.Summary, "stopped early") {
		t.Fatalf("summary = %q, want it to say the pass stopped early", ev.Summary)
	}

	f.block = nil
	f.reset()
	r.run()
	if n := len(f.detected()); n != 2 {
		t.Fatalf("resumed pass analysed %d files, want both", n)
	}
	stored(t, fx.db, first)
}

func TestScheduledDoesNothingWithoutWork(t *testing.T) {
	fx := newFixture(t, false)
	r, _ := newTestRunner(t, fx.db)
	r.Scheduled()
	if r.takeQueued() {
		t.Fatal("the schedule queued a pass with no library opted in")
	}

	fx.db.Model(&fx.library).Update("analyse_tempo", true)
	r.Scheduled()
	if !r.takeQueued() {
		t.Fatal("the schedule did not queue a pass once a library opted in")
	}
}

func TestStatusCountsStoredTempos(t *testing.T) {
	fx := newFixture(t, true)
	r, f := newTestRunner(t, fx.db)
	fx.add(t, "a.flac", "a")
	fx.add(t, "b.flac", "silence")
	f.failFor["silence"] = modules.ErrTooFewBeats
	r.run()

	status := r.Status()
	if status.Stored != 1 || status.Libraries != 1 || status.Running || status.Done != 2 {
		t.Fatalf("status = %+v", status)
	}
}

// The worker drains queued requests; two presses during one pass queue one more.
func TestWorkerRunsQueuedPasses(t *testing.T) {
	fx := newFixture(t, true)
	r, f := newTestRunner(t, fx.db)
	fx.add(t, "a.flac", "a")
	go r.worker()

	r.AnalyseAll()
	waitFor(t, func() bool { return len(f.detected()) == 1 && !r.Running() })
	waitFor(t, func() bool {
		var count int64
		fx.db.Model(&models.Event{}).Where("type = ? AND status = ?", models.EventTypeTempo, models.EventStatusOK).Count(&count)
		return count == 1
	})
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for condition")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

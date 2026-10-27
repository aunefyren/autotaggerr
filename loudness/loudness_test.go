package loudness

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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
	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "loudness.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return db
}

// fake stands in for rsgain and the audio hash. A file's content stands for its audio:
// a test changes the audio by rewriting the file and only its identity by touching it.
// Every track measures -6 dB; the album gain is -6 minus the number of tracks, so a
// test can tell which membership an album was measured with.
type fake struct {
	mu    sync.Mutex
	calls [][]string
	fail  error
}

func (f *fake) analyse(_ context.Context, paths []string) (modules.AlbumLoudness, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), paths...))
	fail := f.fail
	f.mu.Unlock()
	if fail != nil {
		return modules.AlbumLoudness{}, fail
	}
	out := modules.AlbumLoudness{Album: modules.LoudnessResult{Loudness: -12, Gain: -6 - float64(len(paths)), Peak: 0.9}}
	for range paths {
		out.Tracks = append(out.Tracks, modules.LoudnessResult{Loudness: -12, Gain: -6, Peak: 0.5})
	}
	return out, nil
}

func (f *fake) hash(path string) (string, error) {
	content, err := os.ReadFile(path)
	return "h:" + string(content), err
}

func (f *fake) measured() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.calls...)
}

type fixture struct {
	db      *gorm.DB
	dir     string
	library models.Library
	runner  *Runner
	fake    *fake
}

func newFixture(t *testing.T, analyse bool) fixture {
	t.Helper()
	db := testDB(t)
	dir := t.TempDir()
	library := models.Library{Name: "Music", Path: dir, Enabled: true, AnalyseLoudness: analyse}
	if err := db.Create(&library).Error; err != nil {
		t.Fatal(err)
	}
	f := &fake{}
	r := newRunner(db, nil, models.ConfigStruct{})
	r.analyse, r.hash, r.available = f.analyse, f.hash, func() bool { return true }
	r.pollEvery = 5 * time.Millisecond
	return fixture{db: db, dir: dir, library: library, runner: r, fake: f}
}

// add writes a file whose content stands for its audio and indexes it under a release.
func (fx fixture) add(t *testing.T, release, name, audio string) string {
	t.Helper()
	path := filepath.Join(fx.dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(audio), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fx.db.Create(&models.LibraryItem{LibraryID: fx.library.ID, Path: path, MBReleaseID: release}).Error; err != nil {
		t.Fatal(err)
	}
	return path
}

func (fx fixture) stored(t *testing.T, path string) models.TrackLoudness {
	t.Helper()
	var row models.TrackLoudness
	if err := fx.db.First(&row, "path = ?", path).Error; err != nil {
		t.Fatalf("no stored loudness for %s: %v", filepath.Base(path), err)
	}
	return row
}

func (fx fixture) lastEvent(t *testing.T) models.Event {
	t.Helper()
	var ev models.Event
	if err := fx.db.Where("type = ?", models.EventTypeLoudness).Order("started_at desc").First(&ev).Error; err != nil {
		t.Fatalf("no loudness event: %v", err)
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

// TestPassMeasuresEachAlbumTogether: files are measured as albums — one rsgain call per
// release, with every member — and each row carries its track gain and the album's.
// Two discs' "01.flac" are told apart by path, not name.
func TestPassMeasuresEachAlbumTogether(t *testing.T) {
	fx := newFixture(t, true)
	a1 := fx.add(t, "rel-a", "A/CD1/01.flac", "a1")
	a2 := fx.add(t, "rel-a", "A/CD2/01.flac", "a2")
	b1 := fx.add(t, "rel-b", "B/01.mp3", "b1")

	fx.runner.run()

	calls := fx.fake.measured()
	if len(calls) != 2 {
		t.Fatalf("rsgain calls = %v, want one per album", calls)
	}
	for _, call := range calls {
		sort.Strings(call)
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i][0] < calls[j][0] })
	if strings.Join(calls[0], ",") != a1+","+a2 || strings.Join(calls[1], ",") != b1 {
		t.Errorf("albums measured = %v", calls)
	}
	if row := fx.stored(t, a2); row.TrackGain != -6 || row.AlbumGain != -8 || row.AlbumPeak != 0.9 || row.Error != "" {
		t.Errorf("stored = %+v, want the track's gain and the two-track album's", row)
	}
	if ev := fx.lastEvent(t); stat(ev, "Albums checked") != 2 || stat(ev, "Measured") != 2 {
		t.Errorf("event stats = %+v", ev.Stats)
	}
}

// TestUnchangedAlbumsAreNotRemeasured, and a tag write — same audio, new size and
// mtime — is re-stamped, not re-measured.
func TestUnchangedAlbumsAreNotRemeasured(t *testing.T) {
	fx := newFixture(t, true)
	path := fx.add(t, "rel-a", "A/01.flac", "audio")
	fx.add(t, "rel-a", "A/02.flac", "audio2")
	fx.runner.run()

	fx.runner.run()
	if got := len(fx.fake.measured()); got != 1 {
		t.Fatalf("rsgain calls after an unchanged pass = %d, want 1", got)
	}

	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	fx.runner.run()
	if got := len(fx.fake.measured()); got != 1 {
		t.Errorf("rsgain calls after a tags-only change = %d, want still 1", got)
	}
	if row := fx.stored(t, path); !row.ModTime.Equal(later) {
		t.Errorf("mod time = %v, want re-stamped to %v", row.ModTime, later)
	}
	if ev := fx.lastEvent(t); stat(ev, "Tags changed, audio did not") != 1 {
		t.Errorf("event stats = %+v", ev.Stats)
	}
}

// TestAChangedAlbumIsRemeasuredWhole: new audio in one track, or a track added, makes
// every member's album gain stale, so the whole album is measured again.
func TestAChangedAlbumIsRemeasuredWhole(t *testing.T) {
	fx := newFixture(t, true)
	first := fx.add(t, "rel-a", "A/01.flac", "one")
	fx.add(t, "rel-a", "A/02.flac", "two")
	fx.runner.run()

	if err := os.WriteFile(first, []byte("one, remastered"), 0o644); err != nil {
		t.Fatal(err)
	}
	fx.runner.run()
	if calls := fx.fake.measured(); len(calls) != 2 || len(calls[1]) != 2 {
		t.Fatalf("after new audio, rsgain calls = %v, want the whole album again", calls)
	}

	third := fx.add(t, "rel-a", "A/03.flac", "three")
	fx.runner.run()
	calls := fx.fake.measured()
	if len(calls) != 3 || len(calls[2]) != 3 {
		t.Fatalf("after a track was added, rsgain calls = %v, want the three-track album", calls)
	}
	if row := fx.stored(t, first); row.AlbumGain != -9 {
		t.Errorf("first track's album gain = %v, want the three-track album's -9", row.AlbumGain)
	}
	if row := fx.stored(t, third); row.AlbumGain != -9 {
		t.Errorf("new track's album gain = %v", row.AlbumGain)
	}
}

// TestFailuresAreStoredAndNotRetriedUntilTheAlbumChanges.
func TestFailuresAreStoredAndNotRetriedUntilTheAlbumChanges(t *testing.T) {
	fx := newFixture(t, true)
	path := fx.add(t, "rel-a", "A/01.flac", "corrupt")
	fx.fake.fail = errors.New("rsgain failed: could not decode")
	fx.runner.run()

	if row := fx.stored(t, path); row.Error == "" {
		t.Fatal("a failed album stored no error")
	}
	if ev := fx.lastEvent(t); stat(ev, "Failed") != 1 {
		t.Errorf("event stats = %+v", ev.Stats)
	}
	fx.fake.fail = nil
	fx.runner.run()
	if got := len(fx.fake.measured()); got != 1 {
		t.Errorf("rsgain calls = %d, want a failed, unchanged album left alone", got)
	}
	if err := os.WriteFile(path, []byte("fixed"), 0o644); err != nil {
		t.Fatal(err)
	}
	fx.runner.run()
	if row := fx.stored(t, path); row.Error != "" || len(fx.fake.measured()) != 2 {
		t.Errorf("after the file changed: row %+v, calls %d", row, len(fx.fake.measured()))
	}
}

// TestLibrariesThatDidNotOptInAreLeftAlone, and Check says why a pass would do nothing.
func TestLibrariesThatDidNotOptInAreLeftAlone(t *testing.T) {
	fx := newFixture(t, false)
	fx.add(t, "rel-a", "A/01.flac", "audio")
	if err := fx.runner.Check(); !errors.Is(err, ErrNothingOptedIn) {
		t.Errorf("Check = %v, want ErrNothingOptedIn", err)
	}
	fx.runner.run()
	if len(fx.fake.measured()) != 0 {
		t.Error("a library that did not opt in was measured")
	}
	fx.runner.available = func() bool { return false }
	if err := fx.runner.Check(); !errors.Is(err, ErrAnalyserMissing) {
		t.Errorf("Check without rsgain = %v, want ErrAnalyserMissing", err)
	}
}

// TestUnidentifiedFilesAreGroupedByFolder: a file with no release yet is measured with
// the others in its folder, which is what its album is until something names it.
func TestUnidentifiedFilesAreGroupedByFolder(t *testing.T) {
	fx := newFixture(t, true)
	fx.add(t, "", "Loose/01.flac", "one")
	fx.add(t, "", "Loose/02.flac", "two")
	fx.add(t, "", "Other/01.flac", "three")
	fx.runner.run()
	calls := fx.fake.measured()
	sort.Slice(calls, func(i, j int) bool { return len(calls[i]) > len(calls[j]) })
	if len(calls) != 2 || len(calls[0]) != 2 || len(calls[1]) != 1 {
		t.Errorf("rsgain calls = %v, want one per folder", calls)
	}
}

// TestPassYieldsToFileWritingWork and cancelling mid-pause stores nothing more.
func TestPassYieldsToFileWritingWork(t *testing.T) {
	fx := newFixture(t, true)
	fx.add(t, "rel-a", "A/01.flac", "audio")
	var writing sync.Mutex
	busy := true
	fx.runner.yieldTo = func() bool { writing.Lock(); defer writing.Unlock(); return busy }

	done := make(chan struct{})
	go func() { fx.runner.run(); close(done) }()
	time.Sleep(30 * time.Millisecond)
	if len(fx.fake.measured()) != 0 {
		t.Fatal("measured while files were being written")
	}
	if !fx.runner.Status().Paused {
		t.Error("status does not say the pass is paused")
	}
	writing.Lock()
	busy = false
	writing.Unlock()
	<-done
	if len(fx.fake.measured()) != 1 {
		t.Error("the pass did not resume once file work finished")
	}
}

// TestOrphansArePruned: a row whose file left the index goes after a completed pass.
func TestOrphansArePruned(t *testing.T) {
	fx := newFixture(t, true)
	path := fx.add(t, "rel-a", "A/01.flac", "audio")
	fx.runner.run()
	fx.db.Where("path = ?", path).Delete(&models.LibraryItem{})
	fx.runner.run()
	var count int64
	fx.db.Model(&models.TrackLoudness{}).Count(&count)
	if count != 0 {
		t.Errorf("rows after the file left the index = %d", count)
	}
}

// TestScheduledDoesNothingWithoutWork, and once a library opts in it queues a pass.
func TestScheduledDoesNothingWithoutWork(t *testing.T) {
	fx := newFixture(t, false)
	fx.runner.Scheduled()
	if fx.runner.takeQueued() {
		t.Fatal("the schedule queued a pass with no library opted in")
	}
	fx.db.Model(&fx.library).Update("analyse_loudness", true)
	fx.runner.Scheduled()
	if !fx.runner.takeQueued() {
		t.Fatal("the schedule did not queue a pass once a library opted in")
	}
}

// TestWorkerRunsQueuedPassesAndCancelStops: AnalyseAll reaches the worker; Cancel
// during a pause stops the pass, says so, and the next pass resumes.
func TestWorkerRunsQueuedPassesAndCancelStops(t *testing.T) {
	fx := newFixture(t, true)
	fx.add(t, "rel-a", "A/01.flac", "audio")
	fx.runner.yieldTo = func() bool { return true }
	go fx.runner.worker()

	fx.runner.AnalyseAll()
	waitFor(t, func() bool { return fx.runner.Running() && fx.runner.Status().Paused })
	fx.runner.Cancel()
	waitFor(t, func() bool { return !fx.runner.Running() })
	if ev := fx.lastEvent(t); !strings.HasPrefix(ev.Summary, "stopped early") || len(fx.fake.measured()) != 0 {
		t.Fatalf("cancelled pass: summary %q, measured %v", ev.Summary, fx.fake.measured())
	}

	fx.runner.yieldTo = nil
	fx.runner.AnalyseAll()
	waitFor(t, func() bool { return len(fx.fake.measured()) == 1 && !fx.runner.Running() })
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

package components

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// answerManager answers Correlate per path: a release by default, unmatched or a
// failure for the paths named. It records every path it was asked about, because what
// discovery must *not* ask the manager is half of what these tests pin.
type answerManager struct {
	mu        sync.Mutex
	asked     []string
	unmatched map[string]bool
	fail      map[string]bool
}

func (m *answerManager) Correlate(filePath, rootDir string) (models.Correlation, error) {
	m.mu.Lock()
	m.asked = append(m.asked, filePath)
	m.mu.Unlock()
	switch {
	case m.unmatched[filePath]:
		return models.Correlation{}, modules.ErrUnmatched
	case m.fail[filePath]:
		return models.Correlation{}, errors.New("lidarr is down")
	}
	return models.Correlation{
		MBReleaseID: "rel-new", MBReleaseTrackID: "trk-new", MBRecordingID: "rec-new",
		Source: models.CorrelationSourceLidarr,
	}, nil
}

func (m *answerManager) HealthCheck() (bool, error) { return true, nil }
func (m *answerManager) Type() string               { return models.ManagerTypeLidarr }

func (m *answerManager) wasAsked(path string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.asked {
		if p == path {
			return true
		}
	}
	return false
}

// discoverFixture is a library root with an artist folder, and a database holding it.
func discoverFixture(t *testing.T) (*gorm.DB, models.Library) {
	t.Helper()
	db := testDB(t)
	root := t.TempDir()
	library := models.Library{Name: "Music", Path: root, Enabled: true}
	if err := db.Create(&library).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	return db, library
}

// writeAudio writes a file with the given content and pins its mtime, so two files
// can be made to share — or not share — the size and second a move is matched on.
func writeAudio(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// indexRow records a row as a previous Process would have: correlated, tagged by this
// version, and carrying the size and mtime the file had.
func indexRow(t *testing.T, db *gorm.DB, library models.Library, path string, size int64, mtime time.Time, mutate func(*models.LibraryItem)) models.LibraryItem {
	t.Helper()
	mt := mtime
	item := models.LibraryItem{
		LibraryID: library.ID, Path: path, Size: size, ModTime: &mt,
		MBReleaseID: "rel-old", MBReleaseTrackID: "trk-old", MBRecordingID: "rec-old",
		CorrelationSource: models.CorrelationSourceLidarr, CorrelatedByManager: models.ManagerTypeLidarr,
		Status: models.LibraryItemStatusOK, ProcessedVersion: "v1",
	}
	if mutate != nil {
		mutate(&item)
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("create item: %v", err)
	}
	return item
}

func itemAt(t *testing.T, db *gorm.DB, path string) (models.LibraryItem, bool) {
	t.Helper()
	var item models.LibraryItem
	err := db.Where("path = ?", path).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return item, false
	} else if err != nil {
		t.Fatalf("load item %q: %v", path, err)
	}
	return item, true
}

// The production case: a manager renamed an album folder after a tag write. The row
// follows the file — identity, pin and processed version intact — and the manager is
// not asked, because nothing about the file changed but where it is.
func TestDiscoverCarriesAMovedFilesIdentity(t *testing.T) {
	db, library := discoverFixture(t)
	mtime := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	oldPath := filepath.Join(library.Path, "Artist", "Live (2009)", "01 Intro.flac")
	newPath := filepath.Join(library.Path, "Artist", "Live (2010)", "01 Intro.flac")
	writeAudio(t, newPath, "same bytes", mtime)
	row := indexRow(t, db, library, oldPath, int64(len("same bytes")), mtime, func(i *models.LibraryItem) {
		i.Pinned = true
	})

	manager := &answerManager{}
	stats, err := discoverWith(db, library, manager, nil, nil, 2)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	if stats.Walked != 1 || stats.Moved != 1 || stats.Resolved != 0 {
		t.Errorf("stats = %+v, want 1 walked, 1 moved, 0 resolved", stats)
	}
	if manager.wasAsked(newPath) {
		t.Error("a moved file was re-resolved; its identity should have been carried")
	}
	if _, ok := itemAt(t, db, oldPath); ok {
		t.Error("the old path still has a row")
	}
	got, ok := itemAt(t, db, newPath)
	if !ok {
		t.Fatal("no row at the new path")
	}
	if got.ID != row.ID || got.MBReleaseID != "rel-old" || !got.Pinned || got.ProcessedVersion != "v1" {
		t.Errorf("carried row = %+v, want the original row moved whole", got)
	}
}

// Two vanished rows and two new files sharing a size and second cannot be paired
// without guessing, so neither is carried: both files go to the manager instead, and
// the vanished rows are left for the scan's prune.
func TestDiscoverRefusesAnAmbiguousMove(t *testing.T) {
	db, library := discoverFixture(t)
	mtime := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	album := filepath.Join(library.Path, "Artist", "Album (2010)")
	newA, newB := filepath.Join(album, "01 A.flac"), filepath.Join(album, "02 B.flac")
	writeAudio(t, newA, "xxxx", mtime)
	writeAudio(t, newB, "yyyy", mtime)
	oldA := filepath.Join(library.Path, "Artist", "Album (2009)", "01 A.flac")
	oldB := filepath.Join(library.Path, "Artist", "Album (2009)", "02 B.flac")
	indexRow(t, db, library, oldA, 4, mtime, nil)
	indexRow(t, db, library, oldB, 4, mtime, nil)

	manager := &answerManager{}
	stats, err := discoverWith(db, library, manager, nil, nil, 1)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if stats.Moved != 0 || stats.Resolved != 2 {
		t.Errorf("stats = %+v, want nothing carried and both resolved", stats)
	}
	if !manager.wasAsked(newA) || !manager.wasAsked(newB) {
		t.Error("ambiguous files should have been resolved by the manager")
	}
	if _, ok := itemAt(t, db, oldA); !ok {
		t.Error("discovery deleted a vanished row; pruning is the scan's job")
	}
}

// A file the index has never seen gets an identity and nothing else: no processed
// version, so the next Process tags it instead of skipping it as unchanged.
func TestDiscoverResolvesANewFileWithoutMarkingItTagged(t *testing.T) {
	db, library := discoverFixture(t)
	path := filepath.Join(library.Path, "Artist", "Album (2020)", "01 New.flac")
	writeAudio(t, path, "new", time.Now())

	stats, err := discoverWith(db, library, &answerManager{}, nil, nil, 1)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if stats.Resolved != 1 {
		t.Errorf("stats = %+v, want 1 resolved", stats)
	}
	got, ok := itemAt(t, db, path)
	if !ok {
		t.Fatal("no row for the new file")
	}
	if got.MBReleaseID != "rel-new" || got.Status != models.LibraryItemStatusOK {
		t.Errorf("row = %+v, want the manager's identity, status ok", got)
	}
	if got.ProcessedVersion != "" || got.LastTaggedAt != nil {
		t.Errorf("row claims to have been tagged: version %q, last tagged %v", got.ProcessedVersion, got.LastTaggedAt)
	}
	if shouldSkip(db, path, "v1", models.ManagerTypeLidarr) {
		t.Error("the next Process would skip a file discovery never tagged")
	}
}

// An unmatched row is asked again — the manager may know it now — while a pinned row
// and a healthy one are left alone. The manager's answers land as their own states.
func TestDiscoverAsksAgainOnlyAboutFilesWithoutIdentity(t *testing.T) {
	db, library := discoverFixture(t)
	album := filepath.Join(library.Path, "Artist", "Album (2020)")
	now := time.Now()
	paths := map[string]string{}
	for _, name := range []string{"healthy", "unmatched", "pinned", "blank", "stillUnknown", "failing"} {
		p := filepath.Join(album, name+".flac")
		writeAudio(t, p, name, now)
		paths[name] = p
	}
	size := func(name string) int64 { return int64(len(name)) }
	indexRow(t, db, library, paths["healthy"], size("healthy"), now, nil)
	indexRow(t, db, library, paths["unmatched"], size("unmatched"), now, func(i *models.LibraryItem) {
		i.Status = models.LibraryItemStatusUnmatched
	})
	indexRow(t, db, library, paths["pinned"], size("pinned"), now, func(i *models.LibraryItem) {
		i.Pinned, i.MBReleaseID = true, ""
	})
	indexRow(t, db, library, paths["blank"], size("blank"), now, func(i *models.LibraryItem) {
		i.MBReleaseID, i.Status = "", models.LibraryItemStatusError
	})

	manager := &answerManager{
		unmatched: map[string]bool{paths["stillUnknown"]: true},
		fail:      map[string]bool{paths["failing"]: true},
	}
	detail := NewDetailCollector(10)
	stats, err := discoverWith(db, library, manager, nil, detail, 3)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	for _, name := range []string{"healthy", "pinned"} {
		if manager.wasAsked(paths[name]) {
			t.Errorf("%s file was re-resolved", name)
		}
	}
	for _, name := range []string{"unmatched", "blank", "stillUnknown", "failing"} {
		if !manager.wasAsked(paths[name]) {
			t.Errorf("%s file was not resolved", name)
		}
	}
	if stats.Resolved != 2 || stats.Unmatched != 1 || len(stats.Failed) != 1 {
		t.Errorf("stats = %+v, want 2 resolved, 1 unmatched, 1 failed", stats)
	}
	if got, _ := itemAt(t, db, paths["unmatched"]); got.Status != models.LibraryItemStatusOK || got.MBReleaseID != "rel-new" {
		t.Errorf("formerly unmatched row = %+v, want matched now", got)
	}
	if got, _ := itemAt(t, db, paths["stillUnknown"]); got.Status != models.LibraryItemStatusUnmatched {
		t.Errorf("unknown file status = %q, want unmatched", got.Status)
	}
	if got, _ := itemAt(t, db, paths["failing"]); got.Status != models.LibraryItemStatusError || got.Error == "" {
		t.Errorf("failing file = %+v, want an error recorded", got)
	}
	if _, failed := detail.Totals(); failed != 1 {
		t.Errorf("detail recorded %d failures, want 1", failed)
	}
}

// A narrowed pass touches only its folders: a new file elsewhere in the library is
// neither walked nor resolved, and a vanished row elsewhere is not a move candidate.
func TestDiscoverStaysInsideItsRoots(t *testing.T) {
	db, library := discoverFixture(t)
	mtime := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	inside := filepath.Join(library.Path, "Artist", "Album (2020)", "01.flac")
	outside := filepath.Join(library.Path, "Other", "Album (2020)", "01.flac")
	writeAudio(t, inside, "in", mtime)
	writeAudio(t, outside, "out", mtime)
	// Same bytes as the outside file, vanished from outside the scope.
	indexRow(t, db, library, filepath.Join(library.Path, "Other", "Old", "01.flac"), 3, mtime, nil)

	manager := &answerManager{}
	stats, err := discoverWith(db, library, manager, []string{filepath.Join(library.Path, "Artist")}, nil, 1)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if stats.Walked != 1 || stats.Moved != 0 || stats.Resolved != 1 {
		t.Errorf("stats = %+v, want only the in-scope file", stats)
	}
	if manager.wasAsked(outside) {
		t.Error("a file outside the scope was resolved")
	}
	if _, ok := itemAt(t, db, outside); ok {
		t.Error("a file outside the scope was indexed")
	}
}

// An unmounted library is refused rather than walked as empty.
func TestDiscoverRefusesAMissingLibraryRoot(t *testing.T) {
	db := testDB(t)
	library := models.Library{Name: "Gone", Path: filepath.Join(t.TempDir(), "unmounted")}
	if err := db.Create(&library).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverLibraryRoots(db, library, nil, nil, 1); err == nil {
		t.Error("discovery of a missing library root should fail")
	}
}

// Non-audio files are not the pipeline's, and a scope root that has gone walks as empty.
func TestWalkAudioFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.flac", "b.MP3", "cover.jpg", "notes.txt"} {
		writeAudio(t, filepath.Join(dir, "x", name), name, time.Now())
	}
	got, err := walkAudioFiles([]string{dir, filepath.Join(dir, "missing")})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("walked %v, want the two audio files", got)
	}
}

// A row that moved without an identity is carried and then asked about, rather than
// arriving at its new path as unmatched as it left.
func TestDiscoverResolvesAMovedRowThatHadNoIdentity(t *testing.T) {
	db, library := discoverFixture(t)
	mtime := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	oldPath := filepath.Join(library.Path, "Artist", "Old", "01.flac")
	newPath := filepath.Join(library.Path, "Artist", "New", "01.flac")
	writeAudio(t, newPath, "bytes", mtime)
	row := indexRow(t, db, library, oldPath, 5, mtime, func(i *models.LibraryItem) {
		i.MBReleaseID, i.Status = "", models.LibraryItemStatusUnmatched
	})

	manager := &answerManager{}
	stats, err := discoverWith(db, library, manager, nil, nil, 1)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if stats.Moved != 1 || stats.Resolved != 1 || !manager.wasAsked(newPath) {
		t.Errorf("stats = %+v, asked new path = %v; want carried and then resolved", stats, manager.wasAsked(newPath))
	}
	got, _ := itemAt(t, db, newPath)
	if got.ID != row.ID || got.MBReleaseID != "rel-new" || got.Status != models.LibraryItemStatusOK {
		t.Errorf("row = %+v, want the same row, now matched", got)
	}
}

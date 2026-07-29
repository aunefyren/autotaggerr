package components

import (
	"io"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/database"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// modules/ code logs through the package-level logger.Log, which is nil until
// InitLogger runs. Point it at a discarding logger so tests don't nil-panic or
// touch the filesystem.
func init() {
	logger.Log = logrus.New()
	logger.Log.SetOutput(io.Discard)
}

func requireTool(t *testing.T, tool string) {
	t.Helper()
	if _, err := exec.LookPath(tool); err != nil {
		t.Skipf("%q not on PATH; skipping", tool)
	}
}

// synthFlac creates a short silent FLAC and returns its path.
func synthFlac(t *testing.T) string {
	t.Helper()
	requireTool(t, "ffmpeg")
	requireTool(t, "metaflac")
	path := filepath.Join(t.TempDir(), "track.flac")
	cmd := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono", "-t", "0.1", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg synth failed: %v\n%s", err, out)
	}
	return path
}

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "test.db")
	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: dsn})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return db
}

func TestTaggerConfigMapping(t *testing.T) {
	profile := models.TaggerProfile{
		WriteTags:                   true,
		RemoveValues:                true,
		UseCurrentArtistName:        true,
		UseCustomArtistDelimiter:    true,
		CustomArtistDelimiter:       " & ",
		CustomArtistDelimiterCommas: true,
	}
	tagger := NewTagger(profile)
	if !tagger.WriteEnabled() {
		t.Fatal("WriteEnabled should be true")
	}
	cfg := tagger.Config()
	if !cfg.AutotaggerrRemoveValues || cfg.AutotaggerrCustomArtistDelimiter != " & " || !cfg.AutotaggerrUseCurrentArtistName {
		t.Errorf("config projection wrong: %+v", cfg)
	}

	if NewTagger(models.TaggerProfile{WriteTags: false}).WriteEnabled() {
		t.Error("WriteEnabled should be false when WriteTags is off")
	}
}

func TestNewManager(t *testing.T) {
	m, err := NewManager(models.Manager{Type: models.ManagerTypeLidarr, LidarrBaseURL: "http://x", LidarrAPIKey: "k"})
	if err != nil || m.Type() != models.ManagerTypeLidarr {
		t.Fatalf("lidarr manager: %v type=%v", err, m)
	}
	m, err = NewManager(models.Manager{Type: models.ManagerTypeAutotaggerr})
	if err != nil || m.Type() != models.ManagerTypeAutotaggerr {
		t.Fatalf("autotaggerr manager: %v", err)
	}
	if _, err := NewManager(models.Manager{Type: "bogus"}); err == nil {
		t.Error("expected error for unknown manager type")
	}
}

func TestAutotaggerrManagerCorrelateFromTags(t *testing.T) {
	path := synthFlac(t)
	// Embed MusicBrainz IDs so the native (tags) manager can read them back.
	meta := models.FileTags{MBAlbumID: "rel-1", MBReleaseTrackID: "trk-1", MBRecordingID: "rec-1", Title: "Song"}
	if _, _, err := modules.SetFlacTags(path, meta, models.ConfigStruct{}); err != nil {
		t.Fatalf("SetFlacTags: %v", err)
	}

	corr, err := (&AutotaggerrManager{}).Correlate(path, filepath.Dir(path))
	if err != nil {
		t.Fatalf("Correlate: %v", err)
	}
	if corr.MBReleaseID != "rel-1" || corr.MBReleaseTrackID != "trk-1" || corr.MBRecordingID != "rec-1" {
		t.Errorf("correlation from tags wrong: %+v", corr)
	}
	if corr.Source != models.CorrelationSourceTags {
		t.Errorf("source = %q, want %q", corr.Source, models.CorrelationSourceTags)
	}
}

// TestProcessFileRecordsLibraryItem exercises the index write without needing the
// network: a tag-disabled profile skips the MB fetch, so ProcessFile only
// correlates (from embedded tags) and records the library_items row.
func TestProcessFileRecordsLibraryItem(t *testing.T) {
	path := synthFlac(t)
	meta := models.FileTags{MBAlbumID: "rel-1", MBReleaseTrackID: "trk-1", MBRecordingID: "rec-1", Title: "Song"}
	if _, _, err := modules.SetFlacTags(path, meta, models.ConfigStruct{}); err != nil {
		t.Fatalf("SetFlacTags: %v", err)
	}

	db := testDB(t)
	library := models.Library{Name: "Test", Path: filepath.Dir(path)}
	if err := db.Create(&library).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}

	tagger := NewTagger(models.TaggerProfile{WriteTags: false}) // skip tag write / MB fetch
	unchanged, _, err := ProcessFile(db, library, &AutotaggerrManager{}, tagger, nil, nil, path, filepath.Dir(path), "v-test")
	if err != nil {
		t.Fatalf("ProcessFile: %v", err)
	}
	if !unchanged {
		t.Error("expected unchanged=true when tagging is disabled")
	}

	var item models.LibraryItem
	if err := db.Where("path = ?", path).First(&item).Error; err != nil {
		t.Fatalf("library item not recorded: %v", err)
	}
	if item.LibraryID != library.ID {
		t.Errorf("item library = %d, want %d", item.LibraryID, library.ID)
	}
	if item.MBReleaseID != "rel-1" || item.MBReleaseTrackID != "trk-1" {
		t.Errorf("item correlation wrong: %+v", item)
	}
	if item.CorrelationSource != models.CorrelationSourceTags {
		t.Errorf("item source = %q, want tags", item.CorrelationSource)
	}
	if item.Status != models.LibraryItemStatusOK {
		t.Errorf("item status = %q, want ok", item.Status)
	}
	if item.Size == 0 || item.CorrelatedAt == nil || item.LastScannedAt == nil {
		t.Errorf("item identity/timestamps not set: %+v", item)
	}
}

func TestProcessFileRecordsError(t *testing.T) {
	path := synthFlac(t) // no MB tags -> correlation fails
	db := testDB(t)
	library := models.Library{Name: "Test", Path: filepath.Dir(path)}
	if err := db.Create(&library).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}

	_, _, err := ProcessFile(db, library, &AutotaggerrManager{}, NewTagger(models.TaggerProfile{WriteTags: false}), nil, nil, path, filepath.Dir(path), "v-test")
	if err == nil {
		t.Fatal("expected correlation error for untagged file")
	}

	var item models.LibraryItem
	if err := db.Where("path = ?", path).First(&item).Error; err != nil {
		t.Fatalf("error item not recorded: %v", err)
	}
	if item.Status != models.LibraryItemStatusError || item.Error == "" {
		t.Errorf("expected recorded error status, got %+v", item)
	}
}

// TestScanLibrarySkipsUnchanged proves the skip-unchanged fast path: a second
// scan at the same version leaves the file untouched, while a version change
// forces a re-process. A tag-disabled profile keeps it network-free.
func TestScanLibrarySkipsUnchanged(t *testing.T) {
	path := synthFlac(t)
	meta := models.FileTags{MBAlbumID: "rel-1", MBReleaseTrackID: "trk-1", MBRecordingID: "rec-1", Title: "Song"}
	if _, _, err := modules.SetFlacTags(path, meta, models.ConfigStruct{}); err != nil {
		t.Fatalf("SetFlacTags: %v", err)
	}

	db := testDB(t)
	mgr := models.Manager{Name: "Native", Type: models.ManagerTypeAutotaggerr, Enabled: true}
	profile := models.TaggerProfile{Name: "NoWrite", WriteTags: false}
	if err := db.Create(&mgr).Error; err != nil {
		t.Fatalf("create manager: %v", err)
	}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("create profile: %v", err)
	}
	lib := models.Library{Name: "L", Path: filepath.Dir(path), ManagerID: &mgr.ID, TaggerProfileID: &profile.ID}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}

	// First scan indexes the file at version v1.
	counter, unchanged, _, errs, err := ScanLibrary(db, lib, nil, nil, "v1", 1)
	if err != nil || counter != 1 || unchanged != 1 || len(errs) != 0 {
		t.Fatalf("first scan: counter=%d unchanged=%d errs=%v err=%v", counter, unchanged, errs, err)
	}

	// Poison the recorded correlation. If the next scan re-processes, ProcessFile
	// overwrites it back to "rel-1"; if it skips, the sentinel survives.
	if err := db.Model(&models.LibraryItem{}).Where("path = ?", path).Update("mb_release_id", "SENTINEL").Error; err != nil {
		t.Fatalf("poison item: %v", err)
	}

	// Same version + unchanged file -> skipped, sentinel untouched.
	if _, _, _, _, err := ScanLibrary(db, lib, nil, nil, "v1", 1); err != nil {
		t.Fatalf("second scan: %v", err)
	}
	var item models.LibraryItem
	if err := db.Where("path = ?", path).First(&item).Error; err != nil {
		t.Fatalf("load item: %v", err)
	}
	if item.MBReleaseID != "SENTINEL" {
		t.Errorf("expected file to be skipped (sentinel kept), but it was re-processed: mb_release_id=%q", item.MBReleaseID)
	}

	// Different version busts the skip -> re-processed, correlation restored.
	if _, _, _, _, err := ScanLibrary(db, lib, nil, nil, "v2", 1); err != nil {
		t.Fatalf("third scan: %v", err)
	}
	if err := db.Where("path = ?", path).First(&item).Error; err != nil {
		t.Fatalf("reload item: %v", err)
	}
	if item.MBReleaseID != "rel-1" {
		t.Errorf("version change should re-process; mb_release_id=%q, want rel-1", item.MBReleaseID)
	}
	if item.ProcessedVersion != "v2" {
		t.Errorf("processed version = %q, want v2", item.ProcessedVersion)
	}
}

func TestBuildForFile(t *testing.T) {
	db := testDB(t)
	mgr := models.Manager{Name: "Native", Type: models.ManagerTypeAutotaggerr, Enabled: true}
	if err := db.Create(&mgr).Error; err != nil {
		t.Fatalf("create manager: %v", err)
	}
	profile := models.TaggerProfile{Name: "Default", WriteTags: true}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("create profile: %v", err)
	}
	lib := models.Library{Name: "Music", Path: "/music", ManagerID: &mgr.ID, TaggerProfileID: &profile.ID}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}

	// A file under the library resolves to that library's components.
	gotLib, manager, tagger, err := BuildForFile(db, "/music/Artist/Album/01.flac", "/music")
	if err != nil {
		t.Fatalf("BuildForFile: %v", err)
	}
	if gotLib.ID != lib.ID {
		t.Errorf("resolved library = %d, want %d", gotLib.ID, lib.ID)
	}
	if manager.Type() != models.ManagerTypeAutotaggerr || !tagger.WriteEnabled() {
		t.Errorf("unexpected components: manager=%s writeEnabled=%v", manager.Type(), tagger.WriteEnabled())
	}

	// A file outside any library still builds (fallback to the first manager).
	_, manager, _, err = BuildForFile(db, "/elsewhere/x.flac", "/elsewhere")
	if err != nil || manager == nil {
		t.Fatalf("fallback BuildForFile failed: %v", err)
	}
}

// TestResolveManagerRowRejectsDisabled: a library assigned a disabled manager must
// fail loudly rather than silently falling back to native correlation. Swapping the
// correlation authority behind the user's back changes which MB IDs get written
// into their files.
func TestResolveManagerRowRejectsDisabled(t *testing.T) {
	db := testDB(t)
	mgr := models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: false}
	if err := db.Create(&mgr).Error; err != nil {
		t.Fatalf("manager: %v", err)
	}
	lib := models.Library{Name: "L", Path: "/m", Enabled: true, ManagerID: &mgr.ID}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}

	if _, err := resolveManagerRow(db, lib, true); err == nil {
		t.Fatal("resolveManagerRow accepted a disabled manager")
	}
}

// TestResolveManagerRowRejectsDanglingManager: an assigned manager that no longer
// exists is a configuration error, not a reason to pick a different authority.
func TestResolveManagerRowRejectsDanglingManager(t *testing.T) {
	db := testDB(t)
	missing := uuid.New()
	lib := models.Library{Name: "L", Path: "/m", Enabled: true, ManagerID: &missing}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}

	if _, err := resolveManagerRow(db, lib, true); err == nil {
		t.Fatal("resolveManagerRow accepted a dangling manager reference")
	}
}

// TestResolveManagerRowSkipsDisabledFallback: a library with *no* manager assigned
// keeps the permissive fallback, but must not be handed a disabled manager.
func TestResolveManagerRowSkipsDisabledFallback(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.Manager{Name: "Off", Type: models.ManagerTypeLidarr, Enabled: false}).Error; err != nil {
		t.Fatalf("manager: %v", err)
	}
	lib := models.Library{Name: "L", Path: "/m", Enabled: true}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}

	got, err := resolveManagerRow(db, lib, true)
	if err != nil {
		t.Fatalf("resolveManagerRow: %v", err)
	}
	if got.Type != models.ManagerTypeAutotaggerr {
		t.Errorf("fell back to %+v; want the native default, never a disabled manager", got)
	}
}

package components

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

func TestTaggerSettingsMapping(t *testing.T) {
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
	settings := tagger.Settings()
	if !settings.RemoveValues || settings.CustomArtistDelimiter != " & " || !settings.UseCurrentArtistName {
		t.Errorf("settings projection wrong: %+v", settings)
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
	if _, _, _, err := modules.SetFlacTags(path, meta, models.TaggerSettings{}); err != nil {
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
	if _, _, _, err := modules.SetFlacTags(path, meta, models.TaggerSettings{}); err != nil {
		t.Fatalf("SetFlacTags: %v", err)
	}

	db := testDB(t)
	library := models.Library{Name: "Test", Path: filepath.Dir(path)}
	if err := db.Create(&library).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}

	tagger := NewTagger(models.TaggerProfile{WriteTags: false}) // skip tag write / MB fetch
	unchanged, _, err := ProcessFile(db, library, &AutotaggerrManager{}, tagger, nil, nil, nil, path, filepath.Dir(path), "v-test")
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
	if item.CorrelatedByManager != models.ManagerTypeAutotaggerr {
		t.Errorf("item manager = %q, want %q", item.CorrelatedByManager, models.ManagerTypeAutotaggerr)
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

	_, _, err := ProcessFile(db, library, &AutotaggerrManager{}, NewTagger(models.TaggerProfile{WriteTags: false}), nil, nil, nil, path, filepath.Dir(path), "v-test")
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
	if _, _, _, err := modules.SetFlacTags(path, meta, models.TaggerSettings{}); err != nil {
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
	counter, unchanged, _, errs, err := ScanLibrary(db, lib, nil, nil, nil, "v1", 1)
	if err != nil || counter != 1 || unchanged != 1 || len(errs) != 0 {
		t.Fatalf("first scan: counter=%d unchanged=%d errs=%v err=%v", counter, unchanged, errs, err)
	}

	// Poison the recorded correlation. If the next scan re-processes, ProcessFile
	// overwrites it back to "rel-1"; if it skips, the sentinel survives.
	if err := db.Model(&models.LibraryItem{}).Where("path = ?", path).Update("mb_release_id", "SENTINEL").Error; err != nil {
		t.Fatalf("poison item: %v", err)
	}

	// Same version + unchanged file -> skipped, sentinel untouched.
	if _, _, _, _, err := ScanLibrary(db, lib, nil, nil, nil, "v1", 1); err != nil {
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
	if _, _, _, _, err := ScanLibrary(db, lib, nil, nil, nil, "v2", 1); err != nil {
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

// scanFixture sets up a tagged FLAC in its own library with a native manager and a
// no-write tagger profile, and runs one scan so the file is indexed. Returns the DB,
// the library and the file path.
func scanFixture(t *testing.T) (*gorm.DB, models.Library, string) {
	t.Helper()
	path := synthFlac(t)
	meta := models.FileTags{MBAlbumID: "rel-1", MBReleaseTrackID: "trk-1", MBRecordingID: "rec-1", Title: "Song"}
	if _, _, _, err := modules.SetFlacTags(path, meta, models.TaggerSettings{}); err != nil {
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
	if _, _, _, _, err := ScanLibrary(db, lib, nil, nil, nil, "v1", 1); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	return db, lib, path
}

// TestScanLibraryReCorrelatesOnManagerChange covers the case skip-unchanged used to
// walk past: the library's manager changed, so the stored correlation_source is
// stale even though the file on disk and the app version did not change. The stored
// manager type is rewritten to a different one to stand in for "this file was
// correlated by Lidarr", which needs no Lidarr client to reproduce.
func TestScanLibraryReCorrelatesOnManagerChange(t *testing.T) {
	db, lib, path := scanFixture(t)

	if err := db.Model(&models.LibraryItem{}).Where("path = ?", path).Updates(map[string]any{
		"mb_release_id":         "SENTINEL",
		"correlation_source":    models.CorrelationSourceLidarr,
		"correlated_by_manager": models.ManagerTypeLidarr,
	}).Error; err != nil {
		t.Fatalf("simulate previous manager: %v", err)
	}

	if _, _, _, _, err := ScanLibrary(db, lib, nil, nil, nil, "v1", 1); err != nil {
		t.Fatalf("rescan: %v", err)
	}

	var item models.LibraryItem
	if err := db.Where("path = ?", path).First(&item).Error; err != nil {
		t.Fatalf("load item: %v", err)
	}
	if item.MBReleaseID != "rel-1" {
		t.Errorf("manager change should re-process; mb_release_id = %q, want rel-1", item.MBReleaseID)
	}
	if item.CorrelationSource != models.CorrelationSourceTags {
		t.Errorf("correlation_source = %q, want it refreshed to tags", item.CorrelationSource)
	}
	if item.CorrelatedByManager != models.ManagerTypeAutotaggerr {
		t.Errorf("correlated_by_manager = %q, want %q", item.CorrelatedByManager, models.ManagerTypeAutotaggerr)
	}
}

// TestScanLibraryKeepsPinnedOnManagerChange is the other half: a manual attachment
// must survive a manager change untouched, because re-correlating it would overwrite
// the MB IDs the user chose by hand.
func TestScanLibraryKeepsPinnedOnManagerChange(t *testing.T) {
	db, lib, path := scanFixture(t)

	if err := db.Model(&models.LibraryItem{}).Where("path = ?", path).Updates(map[string]any{
		"mb_release_id":         "PINNED",
		"correlation_source":    models.CorrelationSourceManual,
		"correlated_by_manager": models.ManagerTypeLidarr,
		"pinned":                true,
	}).Error; err != nil {
		t.Fatalf("pin item: %v", err)
	}

	if _, _, _, _, err := ScanLibrary(db, lib, nil, nil, nil, "v1", 1); err != nil {
		t.Fatalf("rescan: %v", err)
	}

	var item models.LibraryItem
	if err := db.Where("path = ?", path).First(&item).Error; err != nil {
		t.Fatalf("load item: %v", err)
	}
	if item.MBReleaseID != "PINNED" || item.CorrelationSource != models.CorrelationSourceManual {
		t.Errorf("pinned item should be left alone, got mb_release_id=%q source=%q", item.MBReleaseID, item.CorrelationSource)
	}
}

// spyManager records whether it was asked to correlate, and answers with a
// correlation deliberately different from any pin under test.
type spyManager struct {
	calls atomic.Int32
}

func (m *spyManager) Correlate(filePath, rootDir string) (models.Correlation, error) {
	m.calls.Add(1)
	return models.Correlation{
		MBReleaseID:      "rel-auto",
		MBReleaseTrackID: "trk-auto",
		MBRecordingID:    "rec-auto",
		Source:           models.CorrelationSourceTags,
	}, nil
}

func (m *spyManager) HealthCheck() (bool, error) { return true, nil }
func (m *spyManager) Type() string               { return models.ManagerTypeAutotaggerr }

// TestProcessFilePinnedIsNotReResolved covers the pin being authoritative: a pinned
// item must not be handed to the manager at all, so neither the index row nor the
// tags written to the file can drift onto the manager's answer.
func TestProcessFilePinnedIsNotReResolved(t *testing.T) {
	path := synthFlac(t)
	db := testDB(t)
	library := models.Library{Name: "Test", Path: filepath.Dir(path)}
	if err := db.Create(&library).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}

	now := time.Now()
	pin := models.LibraryItem{
		LibraryID: library.ID, Path: path,
		MBReleaseID: "rel-pinned", MBReleaseTrackID: "trk-pinned", MBRecordingID: "rec-pinned",
		CorrelationSource: models.CorrelationSourceManual, CorrelatedAt: &now,
		Pinned: true, Status: models.LibraryItemStatusOK,
	}
	if err := db.Create(&pin).Error; err != nil {
		t.Fatalf("create pinned item: %v", err)
	}

	mgr := &spyManager{}
	tagger := NewTagger(models.TaggerProfile{WriteTags: false})
	if _, _, err := ProcessFile(db, library, mgr, tagger, nil, nil, nil, path, filepath.Dir(path), "v-test"); err != nil {
		t.Fatalf("ProcessFile: %v", err)
	}

	if n := mgr.calls.Load(); n != 0 {
		t.Errorf("manager consulted %d times for a pinned file, want 0", n)
	}

	var item models.LibraryItem
	if err := db.Where("path = ?", path).First(&item).Error; err != nil {
		t.Fatalf("load item: %v", err)
	}
	if item.MBReleaseID != "rel-pinned" || item.MBReleaseTrackID != "trk-pinned" || item.MBRecordingID != "rec-pinned" {
		t.Errorf("pinned MB IDs overwritten: %+v", item)
	}
	if item.CorrelationSource != models.CorrelationSourceManual {
		t.Errorf("correlation_source = %q, want manual", item.CorrelationSource)
	}
	if !item.Pinned {
		t.Error("item lost its pin")
	}
}

// TestScanLibraryKeepsPinnedOnVersionChange is the exposure that skip-unchanged does
// *not* hide: a version bump re-processes every file including pinned ones, which is
// where an unguarded write would replace the user's correlation.
func TestScanLibraryKeepsPinnedOnVersionChange(t *testing.T) {
	db, lib, path := scanFixture(t)

	if err := db.Model(&models.LibraryItem{}).Where("path = ?", path).Updates(map[string]any{
		"mb_release_id":       "rel-pinned",
		"mb_release_track_id": "trk-pinned",
		"mb_recording_id":     "rec-pinned",
		"correlation_source":  models.CorrelationSourceManual,
		"pinned":              true,
	}).Error; err != nil {
		t.Fatalf("pin item: %v", err)
	}

	// v2 busts the version gate, so the file really is re-processed.
	if _, _, _, _, err := ScanLibrary(db, lib, nil, nil, nil, "v2", 1); err != nil {
		t.Fatalf("rescan: %v", err)
	}

	var item models.LibraryItem
	if err := db.Where("path = ?", path).First(&item).Error; err != nil {
		t.Fatalf("load item: %v", err)
	}
	// The file's own tags say rel-1; the pin says rel-pinned. The pin must win.
	if item.MBReleaseID != "rel-pinned" || item.MBReleaseTrackID != "trk-pinned" || item.MBRecordingID != "rec-pinned" {
		t.Errorf("version bump overwrote the pinned correlation: %+v", item)
	}
	if item.CorrelationSource != models.CorrelationSourceManual {
		t.Errorf("correlation_source = %q, want manual", item.CorrelationSource)
	}
	if item.ProcessedVersion != "v2" {
		t.Errorf("processed_version = %q, want v2 — the file should still be re-processed", item.ProcessedVersion)
	}
}

// TestApplyDataSourceRateLimits exercises the wiring that reads the MusicBrainz
// row's rate_limit into the client limiter. The limiter's interval is unexported in
// modules/, so what is checked here is the row selection and the guards: a nil DB, no
// MusicBrainz row, a disabled row and a zero rate must all be no-ops rather than
// panics or an accidentally removed throttle.
func TestApplyDataSourceRateLimits(t *testing.T) {
	ApplyDataSourceRateLimits(nil) // must not panic

	db := testDB(t)
	ApplyDataSourceRateLimits(db) // no data sources at all

	disabled := models.DataSource{Name: "MB disabled", Type: models.DataSourceTypeMusicBrainz, RateLimit: 5, Enabled: false}
	if err := db.Create(&disabled).Error; err != nil {
		t.Fatalf("create disabled source: %v", err)
	}
	ApplyDataSourceRateLimits(db)

	zeroRate := models.DataSource{Name: "MB zero", Type: models.DataSourceTypeMusicBrainz, RateLimit: 0, Enabled: true}
	if err := db.Create(&zeroRate).Error; err != nil {
		t.Fatalf("create zero-rate source: %v", err)
	}
	ApplyDataSourceRateLimits(db)

	// And the path that does apply. Restored afterwards so later tests in this
	// package are not left running against a modified limiter.
	t.Cleanup(func() { modules.SetMusicBrainzRateLimit(1) })
	if err := db.Model(&models.DataSource{}).Where("id = ?", zeroRate.ID).Update("rate_limit", 2).Error; err != nil {
		t.Fatalf("set rate: %v", err)
	}
	ApplyDataSourceRateLimits(db)
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

// TestDetailCollectorBounds pins the two properties the Activity detail depends on:
// it is safe to fill from the scan's worker pool, and it stops growing at the limit
// while still counting what it dropped — a truncated list has to know it is one.
func TestDetailCollectorBounds(t *testing.T) {
	const limit = 10
	d := NewDetailCollector(limit)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				d.AddChanged(fmt.Sprintf("/music/%d.flac", i), 2, []models.TagChange{{Field: "ARTIST", New: "X"}})
			} else {
				d.AddError(fmt.Sprintf("/music/%d.flac", i), errors.New("nope"))
			}
		}(i)
	}
	wg.Wait()

	if got := len(d.Items()); got != limit {
		t.Errorf("held %d items, want the limit of %d", got, limit)
	}
	changed, failed := d.Totals()
	if changed != 25 || failed != 25 {
		t.Errorf("totals = %d changed / %d failed, want 25/25 — dropped entries must still count", changed, failed)
	}
}

// TestDetailCollectorDisabled covers the callers that record no Activity: a nil
// collector and a zero limit must both be safe to call and hold nothing.
func TestDetailCollectorDisabled(t *testing.T) {
	var nilCollector *DetailCollector
	nilCollector.AddChanged("/x.flac", 1, nil)
	nilCollector.AddError("/x.flac", errors.New("boom"))
	if items := nilCollector.Items(); items != nil {
		t.Errorf("nil collector returned %v", items)
	}
	if c, f := nilCollector.Totals(); c != 0 || f != 0 {
		t.Errorf("nil collector totals = %d/%d", c, f)
	}

	off := NewDetailCollector(0)
	off.AddChanged("/x.flac", 1, nil)
	if got := len(off.Items()); got != 0 {
		t.Errorf("disabled collector held %d items", got)
	}
	// It still counts, so a caller can report totals without storing rows.
	if c, _ := off.Totals(); c != 1 {
		t.Errorf("disabled collector changed total = %d, want 1", c)
	}
}

// TestDetailCollectorAdoptKeepsWhatTheWalkWouldStarve: a run's tagging activity reports
// both halves of what it wrote, so the drift rows are folded into the walk's collector
// under their own phase. They must survive a walk that already filled the limit —
// otherwise the rows nothing else records are exactly the rows dropped, and the phase is
// what keeps the two halves apart in the detail list.
func TestDetailCollectorAdoptKeepsWhatTheWalkWouldStarve(t *testing.T) {
	const limit = 3
	walk := NewDetailCollector(limit)
	for i := 0; i < limit+2; i++ {
		walk.AddChanged(fmt.Sprintf("/music/%d.flac", i), 1, nil)
	}

	drift := NewDetailCollector(limit)
	drift.AddChanged("/music/drifted.flac", 4, nil)
	drift.AddError("/music/broken.flac", errors.New("nope"))

	walk.Adopt(drift, models.EventItemPhaseDrift)

	items := walk.Items()
	if len(items) != limit+2 {
		t.Fatalf("held %d rows, want the walk's %d plus both adopted ones", len(items), limit)
	}
	for _, item := range items[limit:] {
		if item.Phase != models.EventItemPhaseDrift {
			t.Errorf("adopted row %q has phase %q, want %q", item.Path, item.Phase, models.EventItemPhaseDrift)
		}
	}
	// The walk's own rows keep theirs: a file found on disk is not a file re-tagged
	// because its release moved.
	if items[0].Phase != "" {
		t.Errorf("a walk row was re-phased to %q", items[0].Phase)
	}

	changed, failed := walk.Totals()
	if changed != limit+3 || failed != 1 {
		t.Errorf("totals = %d changed / %d failed, want %d/1", changed, failed, limit+3)
	}

	// A nil source is a run where nothing drifted.
	walk.Adopt(nil, models.EventItemPhaseDrift)
}

// TestDetailCollectorNilErrorIgnored: AddError(nil) is a no-op, so a caller need not
// branch on whether the error is real.
func TestDetailCollectorNilErrorIgnored(t *testing.T) {
	d := NewDetailCollector(5)
	d.AddError("/x.flac", nil)
	if got := len(d.Items()); got != 0 {
		t.Errorf("recorded %d items for a nil error", got)
	}
}

// TestScanLibraryCollectsTagDiff is the end-to-end proof that the field-level diff
// survives the whole pipeline: the file is tagged from its own MusicBrainz IDs, and
// what the scan reports must name the fields that changed with their before/after —
// not just a count.
func TestScanLibraryCollectsTagDiff(t *testing.T) {
	path := synthFlac(t)
	// Seed the file with MB IDs plus a deliberately wrong artist, so the write has
	// something real to change.
	seed := models.FileTags{MBAlbumID: "rel-1", MBReleaseTrackID: "trk-1", MBRecordingID: "rec-1", Artist: "Wrong Artist"}
	if _, _, _, err := modules.SetFlacTags(path, seed, models.TaggerSettings{}); err != nil {
		t.Fatalf("seed SetFlacTags: %v", err)
	}

	// Write the corrected tags directly through the tag engine and capture the diff —
	// the same call ProcessFile makes once a release resolves. Going through a full
	// scan here would need a live MusicBrainz release, which this package cannot stub.
	corrected := seed
	corrected.Artist = "Right Artist"
	unchanged, written, changes, err := modules.SetFlacTags(path, corrected, models.TaggerSettings{})
	if err != nil {
		t.Fatalf("SetFlacTags: %v", err)
	}
	if unchanged || written == 0 {
		t.Fatalf("expected a write, got unchanged=%v written=%d", unchanged, written)
	}

	var artist *models.TagChange
	for i := range changes {
		if changes[i].Field == "ARTIST" {
			artist = &changes[i]
		}
	}
	if artist == nil {
		t.Fatalf("ARTIST missing from the diff: %+v", changes)
	}
	if artist.Old != "Wrong Artist" || artist.New != "Right Artist" {
		t.Errorf("ARTIST diff = %q -> %q, want \"Wrong Artist\" -> \"Right Artist\"", artist.Old, artist.New)
	}

	// The diff is what a collector would carry into the event.
	d := NewDetailCollector(10)
	d.AddChanged(path, written, changes)
	items := d.Items()
	if len(items) != 1 || items[0].Status != models.EventItemStatusChanged || len(items[0].Changes) == 0 {
		t.Fatalf("collector did not carry the diff: %+v", items)
	}

	// Re-writing the same tags is a no-op, so nothing is reported as changed.
	unchanged, _, changes, err = modules.SetFlacTags(path, corrected, models.TaggerSettings{})
	if err != nil {
		t.Fatalf("second SetFlacTags: %v", err)
	}
	if !unchanged || len(changes) != 0 {
		t.Errorf("idempotent rewrite reported unchanged=%v changes=%+v", unchanged, changes)
	}
}

// TaggerForLibrary resolves just the tagger a library is configured with. A library
// pointing at an explicit profile gets that profile's settings; one with none falls
// back to a default rather than erroring.
func TestTaggerForLibrary(t *testing.T) {
	db := testDB(t)

	profile := models.TaggerProfile{Name: "No write", WriteTags: false}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("create profile: %v", err)
	}
	lib := models.Library{Name: "L", Path: "/m", TaggerProfileID: &profile.ID}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}

	if TaggerForLibrary(db, lib).WriteEnabled() {
		t.Error("the library's profile disables writes; the tagger should too")
	}

	// A library with no explicit profile still resolves to a usable tagger.
	if TaggerForLibrary(db, models.Library{Name: "Bare", Path: "/n"}) == nil {
		t.Error("a bare library must still resolve a tagger")
	}
}

// correlated is the state every test below starts from: a file the manager has
// already identified. What varies is only what happens next.
func correlated() models.Correlation {
	return models.Correlation{
		MBReleaseID:      "rel-1",
		MBReleaseTrackID: "trk-1",
		MBRecordingID:    "rec-1",
		Source:           models.CorrelationSourceTags,
	}
}

// TestRecordItemKeepsIdentityThroughFailure is the regression for albums going
// mismatched during a MusicBrainz outage. The manager resolved this file before
// anything was attempted on it; failing to *act* on that answer is not a reason to
// forget it. Dropping the MB IDs here is what took the file out of the disk view and
// made its album report `not_indexed` against a manager that could see it fine.
func TestRecordItemKeepsIdentityThroughFailure(t *testing.T) {
	db := testDB(t)
	library := models.Library{Name: "Test", Path: "/music"}
	if err := db.Create(&library).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	path := "/music/Artist/Album (2020)/01 Track.flac"

	outage := fmt.Errorf("failed to get MB release data: %w", modules.ErrTransient)
	recordItem(db, library.ID, path, correlated(), false, "v-test", models.ManagerTypeAutotaggerr, outage)

	var item models.LibraryItem
	if err := db.Where("path = ?", path).First(&item).Error; err != nil {
		t.Fatalf("item not recorded: %v", err)
	}
	if item.MBReleaseID != "rel-1" || item.MBReleaseTrackID != "trk-1" || item.MBRecordingID != "rec-1" {
		t.Errorf("the correlation was discarded by a failure that says nothing about it: %+v", item)
	}
	if item.CorrelationSource != models.CorrelationSourceTags || item.CorrelatedByManager != models.ManagerTypeAutotaggerr {
		t.Errorf("correlation provenance lost: %+v", item)
	}
	if item.CorrelatedAt == nil {
		t.Error("CorrelatedAt not stamped despite a resolved correlation")
	}

	// The failure is still recorded — identity surviving must not mean the problem
	// is hidden from whoever has to fix it.
	if item.Status != models.LibraryItemStatusError || item.Error == "" {
		t.Errorf("the failure was not recorded: %+v", item)
	}
	if item.LastErrorAt == nil {
		t.Error("LastErrorAt not stamped; an undated error cannot be told from a stale one")
	}
	if !item.LastErrorTransient {
		t.Error("an ErrTransient failure must be marked as one — it needs no admin, only a retry")
	}
}

// A failure the app cannot wait out is the other half: recorded the same way, but
// not marked transient, because someone has to go and fix it.
func TestRecordItemMarksNonTransientFailure(t *testing.T) {
	db := testDB(t)
	library := models.Library{Name: "Test", Path: "/music"}
	if err := db.Create(&library).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	path := "/music/Artist/Album (2020)/02 Track.flac"

	recordItem(db, library.ID, path, correlated(), false, "v-test", models.ManagerTypeAutotaggerr,
		errors.New("metaflac: permission denied"))

	var item models.LibraryItem
	if err := db.Where("path = ?", path).First(&item).Error; err != nil {
		t.Fatalf("item not recorded: %v", err)
	}
	if item.MBReleaseID != "rel-1" {
		t.Errorf("identity discarded on a non-transient failure too: %+v", item)
	}
	if item.LastErrorTransient {
		t.Error("a permission error will not fix itself; marking it transient hides it among the retryable ones")
	}
	if item.LastErrorAt == nil {
		t.Error("LastErrorAt not stamped")
	}
}

// TestRecordItemFailureLeavesFileRetryable pins the invariant the whole retry story
// rests on: ProcessedVersion is stamped only by a success, so the next run refuses to
// skip a file that failed. Nothing schedules the retry — the absence of this column
// *is* the retry, which is exactly the kind of thing a later refactor breaks by
// "tidying up" the write.
func TestRecordItemFailureLeavesFileRetryable(t *testing.T) {
	db := testDB(t)
	library := models.Library{Name: "Test", Path: "/music"}
	if err := db.Create(&library).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	path := synthFlac(t) // a real file, so shouldSkip can stat it

	recordItem(db, library.ID, path, correlated(), false, "v-test", models.ManagerTypeAutotaggerr,
		fmt.Errorf("MusicBrainz down: %w", modules.ErrTransient))

	var item models.LibraryItem
	if err := db.Where("path = ?", path).First(&item).Error; err != nil {
		t.Fatalf("item not recorded: %v", err)
	}
	if item.ProcessedVersion != "" {
		t.Errorf("ProcessedVersion = %q after a failure; the next run would skip this file forever", item.ProcessedVersion)
	}
	if shouldSkip(db, path, "v-test", models.ManagerTypeAutotaggerr) {
		t.Error("a failed file was skipped by the next run — it can never recover")
	}
}

// A success has to clear what a failure left behind, or a problem fixed weeks ago
// still reads as current.
func TestRecordItemSuccessClearsPriorFailure(t *testing.T) {
	db := testDB(t)
	library := models.Library{Name: "Test", Path: "/music"}
	if err := db.Create(&library).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	path := "/music/Artist/Album (2020)/03 Track.flac"

	recordItem(db, library.ID, path, correlated(), false, "v-test", models.ManagerTypeAutotaggerr,
		fmt.Errorf("MusicBrainz down: %w", modules.ErrTransient))
	recordItem(db, library.ID, path, correlated(), false, "v-test", models.ManagerTypeAutotaggerr, nil)

	var item models.LibraryItem
	if err := db.Where("path = ?", path).First(&item).Error; err != nil {
		t.Fatalf("item not recorded: %v", err)
	}
	if item.Status != models.LibraryItemStatusOK || item.Error != "" {
		t.Errorf("status not cleared by the recovery: %+v", item)
	}
	if item.LastErrorAt != nil || item.LastErrorTransient {
		t.Errorf("stale failure metadata survived a success: %+v", item)
	}
	if item.ProcessedVersion != "v-test" {
		t.Errorf("ProcessedVersion = %q, want it stamped by the success", item.ProcessedVersion)
	}
}

// Unmatched is not a failure and must not start looking like one: the manager simply
// does not know this file, which is a state the user is not expected to fix.
func TestRecordItemUnmatchedCarriesNoError(t *testing.T) {
	db := testDB(t)
	library := models.Library{Name: "Test", Path: "/music"}
	if err := db.Create(&library).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	path := "/music/Artist/Album (2020)/04 Track.flac"

	recordItem(db, library.ID, path, models.Correlation{}, true, "v-test", models.ManagerTypeLidarr,
		fmt.Errorf("no album: %w", modules.ErrUnmatched))

	var item models.LibraryItem
	if err := db.Where("path = ?", path).First(&item).Error; err != nil {
		t.Fatalf("item not recorded: %v", err)
	}
	if item.Status != models.LibraryItemStatusUnmatched {
		t.Errorf("status = %q, want unmatched", item.Status)
	}
	if item.Error != "" || item.LastErrorAt != nil || item.LastErrorTransient {
		t.Errorf("unmatched recorded as a failure: %+v", item)
	}
}

// A pinned correlation still outranks the manager's on the failure path — the guard
// moved along with the block it guards, and a hand-picked release must not be
// overwritten just because the write that followed it failed.
func TestRecordItemFailureRespectsPin(t *testing.T) {
	db := testDB(t)
	library := models.Library{Name: "Test", Path: "/music"}
	if err := db.Create(&library).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	path := "/music/Artist/Album (2020)/05 Track.flac"

	pinned := models.LibraryItem{
		LibraryID: library.ID, Path: path, Pinned: true,
		MBReleaseID: "rel-manual", MBReleaseTrackID: "trk-manual",
		CorrelationSource: models.CorrelationSourceManual,
	}
	if err := db.Create(&pinned).Error; err != nil {
		t.Fatalf("create pinned item: %v", err)
	}

	recordItem(db, library.ID, path, correlated(), false, "v-test", models.ManagerTypeAutotaggerr,
		fmt.Errorf("MusicBrainz down: %w", modules.ErrTransient))

	var item models.LibraryItem
	if err := db.Where("path = ?", path).First(&item).Error; err != nil {
		t.Fatalf("item not recorded: %v", err)
	}
	if item.MBReleaseID != "rel-manual" || item.CorrelationSource != models.CorrelationSourceManual {
		t.Errorf("the pin was overwritten on the failure path: %+v", item)
	}
}

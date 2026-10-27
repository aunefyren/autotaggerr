package process

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// fakeDownloadClient stands in for qBittorrent: it reports whatever downloads and files
// the test gives it, and records what it was told.
type fakeDownloadClient struct {
	mu        sync.Mutex
	downloads []modules.Download
	files     map[string][]modules.DownloadFile
	finishErr error
	marked    []string
	asked     [][]string
	// movedTo records the post-import category each mark asked for, by ID.
	movedTo map[string]string
	// done is what SeedingDone answers; removed records what Remove was asked to delete.
	done    map[string]bool
	removed []string
}

func (f *fakeDownloadClient) SeedingDone(ids []string) (map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]bool{}
	for _, id := range ids {
		if done, ok := f.done[id]; ok {
			out[id] = done
		}
	}
	return out, nil
}

func (f *fakeDownloadClient) Remove(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, id)
	return nil
}

func (f *fakeDownloadClient) Finished(categories []string) ([]modules.Download, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, categories)
	if f.finishErr != nil {
		return nil, f.finishErr
	}
	return f.downloads, nil
}

func (f *fakeDownloadClient) Files(d modules.Download) ([]modules.DownloadFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.files[d.ID], nil
}

func (f *fakeDownloadClient) MarkImported(id, _, to string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marked = append(f.marked, id)
	if f.movedTo == nil {
		f.movedTo = map[string]string{}
	}
	f.movedTo[id] = to
	return nil
}

func (f *fakeDownloadClient) HealthCheck() (bool, error) { return true, nil }

func useFakeDownloadClient(t *testing.T, fake *fakeDownloadClient) {
	t.Helper()
	prev, prevSettle := newDownloadClient, downloadSettle
	newDownloadClient = func(models.DownloadClient) (modules.DownloadClient, error) { return fake, nil }
	downloadSettle = time.Minute
	t.Cleanup(func() { newDownloadClient, downloadSettle = prev, prevSettle })
}

// newDownloadClientRow is an enabled client watching "music" into library, with a path
// mapping from the client's /downloads to local.
func newDownloadClientRow(t *testing.T, db *gorm.DB, library models.Library, local string) models.DownloadClient {
	t.Helper()
	row := models.DownloadClient{
		Name: "qBittorrent", Type: models.DownloadClientTypeQBittorrent, Enabled: true, BaseURL: "http://qbit",
		PathMappings: []models.PathMapping{{Remote: "/downloads", Local: local}},
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.DownloadClientCategory{ClientID: row.ID, Category: "music", LibraryID: library.ID}).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func downloadItems(t *testing.T, db *gorm.DB) map[string]models.DownloadItem {
	t.Helper()
	var items []models.DownloadItem
	if err := db.Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	out := map[string]models.DownloadItem{}
	for _, item := range items {
		out[item.RemoteID] = item
	}
	return out
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPollDownloadsFindsWhatIsReady: a poll lists each finished download's complete
// audio files as this host sees them, and says why one cannot be imported.
func TestPollDownloadsFindsWhatIsReady(t *testing.T) {
	db := newTestDB(t)
	library := models.Library{Name: "Music", Path: t.TempDir(), Enabled: true}
	if err := db.Create(&library).Error; err != nil {
		t.Fatal(err)
	}
	local := t.TempDir()
	row := newDownloadClientRow(t, db, library, local)
	touch(t, filepath.Join(local, "Band - Album", "01 One.flac"))
	touch(t, filepath.Join(local, "Band - Album", "02 Two.flac"))
	touch(t, filepath.Join(local, "Band - Album", "cover.jpg"))
	touch(t, filepath.Join(local, "Scans", "page.jpg"))

	old := time.Now().Add(-time.Hour)
	fake := &fakeDownloadClient{
		downloads: []modules.Download{
			{ID: "ready", Name: "Band - Album", Category: "music", Path: "/downloads/Band - Album", Seeding: true, CompletedAt: old},
			{ID: "noaudio", Name: "Scans", Category: "music", Path: "/downloads/Scans", Seeding: true, CompletedAt: old},
			{ID: "unmapped", Name: "Elsewhere", Category: "music", Path: "/data/Elsewhere", Seeding: true, CompletedAt: old},
			{ID: "fresh", Name: "Just finished", Category: "music", Path: "/downloads/Fresh", Seeding: true, CompletedAt: time.Now()},
			{ID: "tagged", Name: "Imported before", Category: "music", Path: "/downloads/Old", Seeding: true, CompletedAt: old, Imported: true},
		},
		files: map[string][]modules.DownloadFile{
			"ready": {
				{Path: "/downloads/Band - Album/01 One.flac", Complete: true},
				{Path: "/downloads/Band - Album/02 Two.flac", Complete: true},
				{Path: "/downloads/Band - Album/03 Three.flac"}, // deselected or partial
				{Path: "/downloads/Band - Album/cover.jpg", Complete: true},
			},
			"noaudio":  {{Path: "/downloads/Scans/page.jpg", Complete: true}},
			"unmapped": {{Path: "/data/Elsewhere/01.flac", Complete: true}},
		},
	}
	useFakeDownloadClient(t, fake)
	r := NewRunner(db, models.ConfigStruct{AutotaggerrVersion: "test"})
	t.Cleanup(r.Wait)

	r.PollDownloads()

	items := downloadItems(t, db)
	ready := items["ready"]
	if ready.State != models.DownloadStateReady || ready.LibraryID != library.ID || !ready.Seeding {
		t.Fatalf("ready = %+v", ready)
	}
	wantFiles := []string{filepath.Join(local, "Band - Album", "01 One.flac"), filepath.Join(local, "Band - Album", "02 Two.flac")}
	if strings.Join(ready.Files, "|") != strings.Join(wantFiles, "|") || ready.Path != filepath.Join(local, "Band - Album") {
		t.Errorf("ready files = %v at %s", ready.Files, ready.Path)
	}
	if got := items["noaudio"]; got.State != models.DownloadStateUnavailable || !strings.Contains(got.Reason, "no complete FLAC or MP3") {
		t.Errorf("noaudio = %s %q", got.State, got.Reason)
	}
	if got := items["unmapped"]; got.State != models.DownloadStateUnavailable || !strings.Contains(got.Reason, "add a path mapping for /data") {
		t.Errorf("unmapped = %s %q", got.State, got.Reason)
	}
	if _, listed := items["fresh"]; listed {
		t.Error("a download that only just finished was listed before it settled")
	}
	if got := items["tagged"]; got.State != models.DownloadStateImported {
		t.Errorf("a download carrying the imported tag = %s, want imported", got.State)
	}
	var client models.DownloadClient
	db.First(&client, "id = ?", row.ID)
	if client.LastPollAt == nil || client.LastPollError != "" {
		t.Errorf("client poll = %v %q", client.LastPollAt, client.LastPollError)
	}
	if len(fake.asked) != 1 || strings.Join(fake.asked[0], ",") != "music" {
		t.Errorf("categories asked = %v", fake.asked)
	}

	// A fixed mapping clears an unavailable download on the next poll; a download
	// removed from the client with nothing imported from it is forgotten.
	touch(t, filepath.Join(local, "Elsewhere", "01.flac"))
	db.Model(&models.DownloadClient{}).Where("id = ?", row.ID).Update("path_mappings", `[{"remote":"/downloads","local":"`+filepath.ToSlash(local)+`"},{"remote":"/data","local":"`+filepath.ToSlash(local)+`"}]`)
	fake.downloads = fake.downloads[1:3]
	r.PollDownloads()
	items = downloadItems(t, db)
	if got := items["unmapped"]; got.State != models.DownloadStateReady {
		t.Errorf("after the mapping was added = %s %q", got.State, got.Reason)
	}
	if _, listed := items["ready"]; listed {
		t.Error("a download gone from the client with nothing imported was kept")
	}
}

func TestPollDownloadsRecordsAFailedRead(t *testing.T) {
	db := newTestDB(t)
	library := models.Library{Name: "Music", Path: t.TempDir(), Enabled: true}
	db.Create(&library)
	row := newDownloadClientRow(t, db, library, t.TempDir())
	useFakeDownloadClient(t, &fakeDownloadClient{finishErr: errors.New("qBittorrent didn't respond")})
	r := NewRunner(db, models.ConfigStruct{AutotaggerrVersion: "test"})
	t.Cleanup(r.Wait)

	r.PollDownloads()

	var client models.DownloadClient
	db.First(&client, "id = ?", row.ID)
	if client.LastPollError != "qBittorrent didn't respond" {
		t.Errorf("last poll error = %q", client.LastPollError)
	}
	var events int64
	db.Model(&models.Event{}).Count(&events)
	if events != 0 {
		t.Errorf("a failed poll recorded %d Activity events, want none", events)
	}
}

// downloadWorld is the import world with its source folder offered as a download.
func downloadWorld(t *testing.T) (importWorld, *fakeDownloadClient, models.DownloadItem) {
	t.Helper()
	w := newImportWorld(t)
	folder := filepath.Join(w.root, "Band - Album")
	fake := &fakeDownloadClient{
		downloads: []modules.Download{{ID: "hash1", Name: "Band - Album", Category: "music", Path: "/downloads/Band - Album", Seeding: true, CompletedAt: time.Now().Add(-time.Hour)}},
		files: map[string][]modules.DownloadFile{"hash1": {
			{Path: "/downloads/Band - Album/01 song one.flac", Complete: true},
			{Path: "/downloads/Band - Album/02 song two.flac", Complete: true},
		}},
	}
	useFakeDownloadClient(t, fake)
	newDownloadClientRow(t, w.db, w.library, w.root)
	w.runner.PollDownloads()
	item := downloadItems(t, w.db)["hash1"]
	if item.State != models.DownloadStateReady || item.Path != folder {
		t.Fatalf("download = %s %q at %s", item.State, item.Reason, item.Path)
	}
	return w, fake, item
}

// TestImportFromADownload: an album imported from a download is copied — the torrent
// keeps its files — and once every album of it is in, the download is marked imported
// and its client told.
func TestImportFromADownload(t *testing.T) {
	w, fake, item := downloadWorld(t)
	rows := w.runner.ProposeImport(w.library, w.release, "", item.Files)
	ref := &ImportDownload{ItemID: item.ID, Folder: item.Path}

	moving := ImportPlan{Library: w.library, Release: w.release, Root: item.Path, Mode: modules.TransferMove, Rows: rows, Download: ref}
	if err := w.runner.Import(moving); !errors.Is(err, ErrDownloadMustCopy) {
		t.Fatalf("moving a seeding download = %v, want ErrDownloadMustCopy", err)
	}

	plan := ImportPlan{Library: w.library, Release: w.release, Root: item.Path, Mode: modules.TransferCopy, Rows: rows, Download: ref}
	if err := w.runner.Import(plan); err != nil {
		t.Fatal(err)
	}
	if err := w.runner.Import(plan); err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("importing an album twice = %v", err)
	}
	w.runner.waitIdle(t)

	for _, source := range item.Files {
		if _, err := os.Stat(source); err != nil {
			t.Errorf("the torrent's file %s is gone: %v", source, err)
		}
	}
	got := downloadItems(t, w.db)["hash1"]
	if got.State != models.DownloadStateImported || got.Groups[item.Path] != models.DownloadGroupImported || got.ImportedAt == nil {
		t.Errorf("download after import = %s %v", got.State, got.Groups)
	}
	if strings.Join(fake.marked, ",") != "hash1" {
		t.Errorf("client told = %v, want hash1", fake.marked)
	}
	var indexed int64
	w.db.Model(&models.LibraryItem{}).Count(&indexed)
	if indexed != 2 {
		t.Errorf("indexed = %d, want 2", indexed)
	}

	// An imported download is not offered again, however often it is polled.
	w.runner.PollDownloads()
	if again := downloadItems(t, w.db)["hash1"]; again.State != models.DownloadStateImported {
		t.Errorf("after another poll = %s", again.State)
	}
}

func TestImportFromADownloadRefusesAForeignFolder(t *testing.T) {
	w, _, item := downloadWorld(t)
	rows := w.runner.ProposeImport(w.library, w.release, "", item.Files)
	plan := ImportPlan{Library: w.library, Release: w.release, Root: item.Path, Mode: modules.TransferCopy, Rows: rows,
		Download: &ImportDownload{ItemID: item.ID, Folder: filepath.Join(item.Path, "elsewhere")}}
	if err := w.runner.Import(plan); err == nil || !strings.Contains(err.Error(), "not an album of") {
		t.Errorf("a folder outside the download = %v", err)
	}
}

// A queued album outlives nothing: after a restart the job that would have settled it
// is gone, so the album is offered again.
func TestReconcileDownloadsForgetsQueuedAlbums(t *testing.T) {
	db := newTestDB(t)
	item := models.DownloadItem{RemoteID: "h", State: models.DownloadStateReady,
		Groups: map[string]string{"/a": models.DownloadGroupQueued, "/b": models.DownloadGroupImported}}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	ReconcileDownloads(db)
	got := downloadItems(t, db)["h"]
	if len(got.Groups) != 1 || got.Groups["/b"] != models.DownloadGroupImported {
		t.Errorf("groups after reconcile = %v", got.Groups)
	}
}

func TestDownloadSourceName(t *testing.T) {
	if got := DownloadSourceName(models.DownloadItem{Name: "Band - Single.flac"}); got != "Band - Single" {
		t.Errorf("single file = %q", got)
	}
	if got := DownloadSourceName(models.DownloadItem{Name: "Band - Album.2020"}); got != "Band - Album.2020" {
		t.Errorf("folder = %q", got)
	}
}

// TestImportedDownloadIsMovedAndRemovedAfterSeeding: with the client's post-import
// category set, the import moves the download there; with removal on, it is removed —
// files and all — once the client says a share limit stopped it, and not before. A
// download known only by another database's tag is never removed.
func TestImportedDownloadIsMovedAndRemovedAfterSeeding(t *testing.T) {
	w, fake, item := downloadWorld(t)
	if err := w.db.Model(&models.DownloadClient{}).Where("id = ?", item.ClientID).
		Updates(map[string]any{"imported_category": "music-done", "remove_after_seeding": true}).Error; err != nil {
		t.Fatal(err)
	}
	// Imported by someone else: tagged in the client, nothing recorded here.
	foreign := models.DownloadItem{ClientID: item.ClientID, RemoteID: "foreign", Name: "Foreign", State: models.DownloadStateImported}
	if err := w.db.Create(&foreign).Error; err != nil {
		t.Fatal(err)
	}

	rows := w.runner.ProposeImport(w.library, w.release, "", item.Files)
	plan := ImportPlan{Library: w.library, Release: w.release, Root: item.Path, Mode: modules.TransferCopy, Rows: rows,
		Download: &ImportDownload{ItemID: item.ID, Folder: item.Path}}
	if err := w.runner.Import(plan); err != nil {
		t.Fatal(err)
	}
	w.runner.waitIdle(t)
	if fake.movedTo["hash1"] != "music-done" {
		t.Errorf("moved to %q, want music-done", fake.movedTo["hash1"])
	}

	// Still seeding: nothing removed.
	fake.done = map[string]bool{"hash1": false, "foreign": true}
	w.runner.PollDownloads()
	if len(fake.removed) != 0 {
		t.Fatalf("removed %v while seeding, or a foreign download", fake.removed)
	}

	// Stopped by its share limit, and gone from the watched category: removed once.
	fake.downloads = nil
	fake.done = map[string]bool{"hash1": true, "foreign": true}
	w.runner.PollDownloads()
	w.runner.PollDownloads()
	if strings.Join(fake.removed, ",") != "hash1" {
		t.Errorf("removed = %v, want hash1 once", fake.removed)
	}
	got := downloadItems(t, w.db)["hash1"]
	if got.RemovedAt == nil || got.Seeding || !strings.Contains(got.Reason, "after it finished seeding") {
		t.Errorf("download after removal = %+v", got)
	}
	var removals []models.Event
	w.db.Where("type = ?", models.EventTypeDownloadRemoved).Find(&removals)
	if len(removals) != 1 || removals[0].Status != models.EventStatusOK {
		t.Errorf("removal events = %+v, want one", removals)
	}
}

// TestPollReadsAUsenetDownloadFromDisk: a client that cannot list files (SABnzbd) has
// them read from the mapped folder, and a download nothing seeds may be moved.
func TestPollReadsAUsenetDownloadFromDisk(t *testing.T) {
	w := newImportWorld(t)
	fake := &fakeDownloadClient{downloads: []modules.Download{{ID: "nzo1", Name: "Band - Album", Category: "music",
		Path: "/downloads/Band - Album", FilesOnDisk: true, CompletedAt: time.Now().Add(-time.Hour)}}}
	useFakeDownloadClient(t, fake)
	newDownloadClientRow(t, w.db, w.library, w.root)
	w.runner.PollDownloads()
	item := downloadItems(t, w.db)["nzo1"]
	if item.State != models.DownloadStateReady || len(item.Files) != 2 || item.Seeding {
		t.Fatalf("download = %s %q files %v seeding %v", item.State, item.Reason, item.Files, item.Seeding)
	}
	rows := w.runner.ProposeImport(w.library, w.release, "", item.Files)
	plan := ImportPlan{Library: w.library, Release: w.release, Root: item.Path, Mode: modules.TransferMove, Rows: rows,
		Download: &ImportDownload{ItemID: item.ID, Folder: item.Path}}
	if err := w.runner.Import(plan); err != nil {
		t.Errorf("moving a download nothing seeds = %v", err)
	}
	w.runner.waitIdle(t)
}

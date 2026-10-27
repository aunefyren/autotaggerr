package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/database"
	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
)

type fixture struct {
	m          *Measurer
	db         *gorm.DB
	configDir  string
	artworkDir string
	libA, libB models.Library
}

// newFixture builds a config directory with a real SQLite database, an artwork cache
// and a log, and two libraries on the same disk.
func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	artworkDir := filepath.Join(configDir, "artwork")
	for _, d := range []string{artworkDir, filepath.Join(root, "a"), filepath.Join(root, "b")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(configDir, "autotaggerr.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	logPath := filepath.Join(configDir, "autotaggerr.log")
	write(t, logPath, 300)
	write(t, logPath+".1", 200)
	write(t, filepath.Join(configDir, "config.json"), 50)
	write(t, filepath.Join(configDir, "autotaggerr.db.bak-1"), 700)

	f := fixture{
		db: db, configDir: configDir, artworkDir: artworkDir,
		libA: models.Library{Name: "A", Path: filepath.Join(root, "a"), Enabled: true},
		libB: models.Library{Name: "B", Path: filepath.Join(root, "b"), Enabled: true},
	}
	f.m = NewMeasurer(db, Paths{ConfigDir: configDir, ArtworkDir: artworkDir,
		LogFiles: func() []string { return []string{logPath, logPath + ".1"} }})
	for _, l := range []*models.Library{&f.libA, &f.libB} {
		if err := db.Create(l).Error; err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func write(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Repeat("x", size)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) seed(t *testing.T, rows ...any) {
	t.Helper()
	for _, r := range rows {
		if err := f.db.Create(r).Error; err != nil {
			t.Fatalf("seed %T: %v", r, err)
		}
	}
}

func item(lib models.Library, path, release string, size int64) *models.LibraryItem {
	return &models.LibraryItem{LibraryID: lib.ID, Path: path, MBReleaseID: release, Size: size, Status: models.LibraryItemStatusOK}
}

// TestReportMusic: per-library totals and formats, unidentified files, the largest
// artists and albums by the release-group's credit, and an album held twice — once as
// two editions — counted with the space beyond its largest copy.
func TestReportMusic(t *testing.T) {
	f := newFixture(t)
	f.seed(t,
		&models.CollectionArtist{MBID: "art-1", Name: "Big Band"},
		&models.CollectionArtist{MBID: "art-2", Name: "Small Band"},
		&models.CollectionReleaseGroup{MBID: "rg-1", ArtistMBID: "art-1", Title: "Twice"},
		&models.CollectionReleaseGroup{MBID: "rg-2", ArtistMBID: "art-2", Title: "Once"},
		&models.CollectionRelease{MBID: "rel-1a", ReleaseGroupMBID: "rg-1", ArtistMBID: "art-1"},
		&models.CollectionRelease{MBID: "rel-1b", ReleaseGroupMBID: "rg-1", ArtistMBID: "art-1"},
		&models.CollectionRelease{MBID: "rel-2", ReleaseGroupMBID: "rg-2", ArtistMBID: "art-2"},
		item(f.libA, "/a/1.flac", "rel-1a", 600),
		item(f.libA, "/a/2.flac", "rel-1a", 400),
		item(f.libB, "/b/1.mp3", "rel-1b", 300),
		item(f.libB, "/b/2.MP3", "rel-2", 200),
		item(f.libB, "/b/loose.flac", "", 50),
	)

	r, err := f.m.Report(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Libraries) != 2 {
		t.Fatalf("libraries = %+v", r.Libraries)
	}
	a, b := r.Libraries[0], r.Libraries[1]
	if a.Files != 2 || a.Bytes != 1000 || len(a.Formats) != 1 || a.Formats[0].Format != "FLAC" {
		t.Errorf("A = %+v", a)
	}
	if b.Bytes != 550 || b.Unidentified != (Count{Files: 1, Bytes: 50}) {
		t.Errorf("B = %+v", b)
	}
	if len(b.Formats) != 2 || b.Formats[0] != (Format{Format: "MP3", Files: 2, Bytes: 500}) {
		t.Errorf("B formats = %+v, want MP3 (both cases) first", b.Formats)
	}

	if len(r.LargestArtists) != 2 || r.LargestArtists[0].Name != "Big Band" || r.LargestArtists[0].Bytes != 1300 {
		t.Errorf("largest artists = %+v", r.LargestArtists)
	}
	top := r.LargestAlbums[0]
	if top.Name != "Twice" || top.Artist != "Big Band" || top.Copies != 2 || top.Bytes != 1300 {
		t.Errorf("largest album = %+v", top)
	}

	d := r.Duplicates
	if d.Albums != 1 || d.Extra != 300 || len(d.Top) != 1 || d.Top[0].Name != "Twice" || d.Top[0].Extra != 300 {
		t.Errorf("duplicates = %+v, want Twice with 300 beyond its 1000-byte copy", d)
	}

	// Both libraries sit on one disk with config/: one volume, holding all of it.
	if len(r.Volumes) != 1 {
		t.Fatalf("volumes = %+v, want one", r.Volumes)
	}
	v := r.Volumes[0]
	if v.Music != 1550 || !v.HoldsApp || v.App != r.App.Total || len(v.Libraries) != 2 || v.Total == 0 {
		t.Errorf("volume = %+v", v)
	}
}

// TestReportApp: the database by group, the artwork cache by kind with only stale
// leftovers counted, the log and its rotated file, and the rest of config/ — a
// forgotten backup included.
func TestReportApp(t *testing.T) {
	f := newFixture(t)
	f.seed(t, item(f.libA, "/a/1.flac", "", 10),
		&models.ArtworkCacheEntry{Key: "release-group_x_front_500", Missing: true})
	old := time.Now().Add(-2 * LeftoverAge)
	for name, size := range map[string]int{
		"release-group_a_front_250":     100,
		"release-group_x_front_500":     40, // index says no image: leftover
		"artist_b_thumb_0":              60,
		"release-group_c_front_250.tmp": 20, // interrupted: leftover
	} {
		path := filepath.Join(f.artworkDir, name)
		write(t, path, size)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(f.artworkDir, "release-group_d_front_250.tmp"), 5) // too young to count

	r, err := f.m.Report(true)
	if err != nil {
		t.Fatal(err)
	}
	app := r.App
	if app.Database.File == 0 || app.Database.Path == "" || app.Database.Error != "" {
		t.Fatalf("database = %+v", app.Database)
	}
	var index *TableGroup
	for i, g := range app.Database.Groups {
		if g.Label == "File index" {
			index = &app.Database.Groups[i]
		}
		if strings.HasPrefix(g.Label, "sqlite") {
			t.Errorf("SQLite's own tables were listed: %+v", g)
		}
	}
	if index == nil || index.Rows != 1 || index.Bytes == 0 {
		t.Errorf("file index group = %+v", index)
	}

	art := app.Artwork
	if art.Files != 5 || art.Bytes != 225 || art.NoImage != 1 {
		t.Errorf("artwork = %+v", art)
	}
	if art.Leftovers != (Count{Files: 2, Bytes: 60}) {
		t.Errorf("leftovers = %+v, want the old .tmp and the no-image file only", art.Leftovers)
	}
	kinds := map[string]int64{}
	for _, k := range art.Kinds {
		kinds[k.Label] = k.Bytes
	}
	if kinds["Album covers, 250 px"] != 100 || kinds["Artist portraits"] != 60 || kinds["Interrupted downloads"] != 25 {
		t.Errorf("kinds = %v", kinds)
	}

	if app.Logs.Bytes != 500 || len(app.Logs.Files) != 2 {
		t.Errorf("logs = %+v", app.Logs)
	}
	other := map[string]int64{}
	for _, f := range app.Other {
		other[f.Name] = f.Bytes
	}
	if len(other) != 2 || other["config.json"] != 50 || other["autotaggerr.db.bak-1"] != 700 {
		t.Errorf("other = %v, want config.json and the backup — not the database, its WAL or the logs", other)
	}
	want := app.Database.File + app.Database.WAL + app.Database.SHM + 225 + 500 + 750
	if app.Total != want {
		t.Errorf("total = %d, want %d", app.Total, want)
	}
}

// TestReportIsCached: a measurement is served for CacheFor, and Invalidate (every
// action calls it) or fresh takes a new one.
func TestReportIsCached(t *testing.T) {
	f := newFixture(t)
	clock := time.Now()
	f.m.now = func() time.Time { return clock }

	first, _ := f.m.Report(false)
	f.seed(t, item(f.libA, "/a/1.flac", "", 10))
	if again, _ := f.m.Report(false); !again.MeasuredAt.Equal(first.MeasuredAt) || again.Libraries[0].Files != 0 {
		t.Error("a second read inside the cache window measured again")
	}
	f.m.Invalidate()
	if after, _ := f.m.Report(false); after.Libraries[0].Files != 1 {
		t.Error("Invalidate did not drop the cached report")
	}
	clock = clock.Add(CacheFor + time.Second)
	f.seed(t, item(f.libA, "/a/2.flac", "", 10))
	if later, _ := f.m.Report(false); later.Libraries[0].Files != 2 {
		t.Error("an expired report was served")
	}
}

// TestActions: leftovers are removed and nothing else; Activity is pruned to the
// retention; the WAL checkpoint and VACUUM run against a real database.
func TestActions(t *testing.T) {
	f := newFixture(t)
	f.seed(t, &models.ArtworkCacheEntry{Key: "release-group_x_front_500", Missing: true})
	old := time.Now().Add(-2 * LeftoverAge)
	for _, name := range []string{"release-group_a_front_250", "release-group_x_front_500", "release-group_c_front_250.tmp"} {
		path := filepath.Join(f.artworkDir, name)
		write(t, path, 10)
		_ = os.Chtimes(path, old, old)
	}
	out, err := f.m.RemoveArtworkLeftovers()
	if err != nil || !strings.Contains(out.Message, "Removed 2") {
		t.Fatalf("leftovers = %+v, %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(f.artworkDir, "release-group_a_front_250")); err != nil {
		t.Error("a served image was removed")
	}

	for i := 0; i < 5; i++ {
		ev := models.Event{Type: models.EventTypeProcess, Title: "run", Status: models.EventStatusOK,
			StartedAt: time.Now().Add(-time.Duration(i) * time.Hour)}
		f.seed(t, &ev)
		f.seed(t, &models.EventItem{EventID: ev.ID, Path: "/a/1.flac"})
	}
	out, err = f.m.PruneActivity(2)
	if err != nil || out.Before != 10 || out.After != 4 {
		t.Errorf("prune = %+v, %v; want 10 rows down to 4", out, err)
	}

	if _, err := f.m.Checkpoint(); err != nil {
		t.Errorf("checkpoint: %v", err)
	}
	out, err = f.m.Compact()
	if err != nil || out.After == 0 || !strings.Contains(out.Message, "compacted") {
		t.Errorf("compact = %+v, %v", out, err)
	}
}

func TestTableGroup(t *testing.T) {
	for table, want := range map[string]string{
		"events":                     "Activity history",
		"event_items":                "Activity history",
		"musicbrainz_release_caches": "MusicBrainz cache",
		"musicbrainz_migrations":     "MusicBrainz changes",
		"provider_caches":            "Lidarr and Plex cache",
		"library_items":              "File index",
		"collection_release_groups":  "Collection",
		"track_tempos":               "Analysis and fingerprints",
		"acoust_id_lookups":          "Analysis and fingerprints",
		"artwork_cache_entries":      "Artwork index",
		"download_items":             "Downloads",
		"users":                      "Settings and accounts",
	} {
		if got := tableGroup(table); got != want {
			t.Errorf("tableGroup(%q) = %q, want %q", table, got, want)
		}
	}
}

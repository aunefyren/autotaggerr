package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aunefyren/autotaggerr/auth"
	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/database"
	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/insights"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// markerFile marks a directory as a demo instance. generate refuses any non-empty
// directory without it, and serve reads the seed and size back from it.
const markerFile = ".autotaggerr-demo"

// Demo ports: the app and the stand-in MusicBrainz.
const (
	appPort  = 8090
	stubAddr = "127.0.0.1:8091"
)

// Marker is what a demo directory records about itself.
type Marker struct {
	Seed     int64  `json:"seed"`
	Size     string `json:"size"`
	Raw      bool   `json:"raw"`
	StubAddr string `json:"stub_addr"`
}

func readMarker(dir string) (Marker, error) {
	var m Marker
	raw, err := os.ReadFile(filepath.Join(dir, markerFile))
	if err != nil {
		return m, fmt.Errorf("%s is not a demo directory (no %s); run generate first", dir, markerFile)
	}
	err = json.Unmarshal(raw, &m)
	if m.StubAddr == "" {
		m.StubAddr = stubAddr
	}
	return m, err
}

// checkTarget refuses to generate anywhere that holds something other than a demo:
// a non-empty directory without the marker, or the repository's own config.
func checkTarget(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if wd, err := os.Getwd(); err == nil && (abs == wd || abs == filepath.Join(wd, "config")) {
		return errors.New("refusing to generate into the repository itself; pick a directory of its own, e.g. ~/autotaggerr-demo")
	}
	entries, err := os.ReadDir(abs)
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(entries) == 0) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(abs, markerFile)); err != nil {
		return fmt.Errorf("%s is not empty and is not a demo directory; refusing to write into it", abs)
	}
	return nil
}

// GenerateOptions configures generate.
type GenerateOptions struct {
	Dir    string
	Seed   int64
	Preset Preset
	// Raw skips the pre-fill: the library starts unindexed, and the first Process
	// identifies and tags every file through the stand-in server.
	Raw bool
	Log func(format string, args ...any)
}

// fileSpec is one file to write and index.
type fileSpec struct {
	path, format string
	tags         FileTags
	lengthMS     int
	seed         uint64
	enc          Encoding
	release      *Release
	track        *Track
}

var unsafePath = strings.NewReplacer("/", "-", "\\", "-", ":", "-", "*", "", "?", "", "\"", "", "<", "", ">", "", "|", "")

func safe(name string) string { return strings.TrimSpace(unsafePath.Replace(name)) }

// plan lists every file the universe's holdings put on disk, plus unmatched strays.
func plan(u *Universe, music string) []fileSpec {
	var out []fileSpec
	// Generated titles repeat, so one artist can hold two albums with the same title
	// and year; the second gets a numbered folder, as it would on a real disk.
	taken := map[string]bool{}
	for _, h := range u.Holdings {
		rel := h.Release
		albumArtist := CreditName(rel.Group.Credit)
		base := filepath.Join(music, safe(albumArtist), safe(fmt.Sprintf("%s (%s)%s", rel.Title, rel.Year(), h.Suffix)))
		folder := base
		for n := 2; taken[folder]; n++ {
			folder = fmt.Sprintf("%s (%d)", base, n)
		}
		taken[folder] = true
		for _, m := range rel.Media {
			dir := folder
			if len(rel.Media) > 1 {
				dir = filepath.Join(folder, fmt.Sprintf("CD %d", m.Position))
			}
			for _, t := range m.Tracks {
				rec := t.Recording
				out = append(out, fileSpec{
					path:   filepath.Join(dir, safe(fmt.Sprintf("%02d %s.%s", t.Position, rec.Title, h.Format))),
					format: h.Format,
					tags: FileTags{Title: rec.Title, Artist: CreditName(rec.Credit), Album: rel.Title, AlbumArtist: albumArtist,
						Track: t.Position, Disc: m.Position, Date: rel.Date, ReleaseID: rel.ID, ReleaseGroupID: rel.Group.ID,
						ReleaseTrackID: t.ID, RecordingID: rec.ID, AlbumArtistID: rel.Group.Credit[0].Artist.ID},
					lengthMS: rec.LengthMS, seed: rec.Master ^ h.Salt, enc: encodingFor(rel.Group.ID, h.Format), release: rel, track: t,
				})
			}
		}
	}
	for i := 0; i < u.Unmatched; i++ {
		out = append(out, fileSpec{
			path:   filepath.Join(music, "Unsorted", fmt.Sprintf("track%03d.flac", i+1)),
			format: "flac", tags: FileTags{Title: fmt.Sprintf("Untitled %d", i+1)}, lengthMS: 200000, seed: uint64(i) + 1, enc: Encoding{BitDepth: 16},
		})
	}
	return out
}

// writeFiles writes every file with a small worker pool.
func writeFiles(specs []fileSpec) error {
	work := make(chan fileSpec)
	errs := make(chan error, 1)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for s := range work {
				if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err == nil {
					err = writeAudio(s.path, s.format, s.tags, s.lengthMS, s.seed, s.enc)
					if err == nil {
						continue
					}
					select {
					case errs <- fmt.Errorf("%s: %w", s.path, err):
					default:
					}
				}
			}
		}()
	}
	for _, s := range specs {
		work <- s
	}
	close(work)
	wg.Wait()
	select {
	case err := <-errs:
		return err
	default:
		return nil
	}
}

// Generate builds a demo instance: files, config, database and (unless Raw) the
// pre-filled index, release cache, collection and graph credits.
func Generate(opts GenerateOptions) (*Universe, error) {
	log := opts.Log
	if log == nil {
		log = func(string, ...any) {}
	}
	dir, err := filepath.Abs(opts.Dir)
	if err != nil {
		return nil, err
	}
	if err := checkTarget(dir); err != nil {
		return nil, err
	}
	// A fresh start every time: a generate over an old demo would mix two universes.
	for _, sub := range []string{"music", "config"} {
		if err := os.RemoveAll(filepath.Join(dir, sub)); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0o755); err != nil {
		return nil, err
	}
	marker, _ := json.MarshalIndent(Marker{Seed: opts.Seed, Size: opts.Preset.Name, Raw: opts.Raw, StubAddr: stubAddr}, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, markerFile), marker, 0o644); err != nil {
		return nil, err
	}

	started := time.Now()
	u := GenerateUniverse(opts.Seed, opts.Preset)
	specs := plan(u, filepath.Join(dir, "music"))
	log("universe: %d artists, %d release groups, %d releases; writing %d files", len(u.Artists), len(u.Groups), len(u.Releases), len(specs))
	if err := writeFiles(specs); err != nil {
		return nil, err
	}
	log("files written in %s", time.Since(started).Round(time.Millisecond))

	db, err := setupInstance(dir)
	if err != nil {
		return nil, err
	}
	if sqlDB, err := db.DB(); err == nil {
		defer sqlDB.Close()
	}
	if opts.Raw {
		log("raw: library left unindexed; press Process in the app")
		return u, nil
	}
	// The batched pre-fill inserts trip GORM's slow-query log, which prints each whole
	// statement; errors still come back as values.
	quiet := db.Session(&gorm.Session{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err := prefill(quiet, u, specs, dir); err != nil {
		return nil, err
	}
	plays, err := seedListens(quiet, specs, opts.Seed)
	if err != nil {
		return nil, err
	}
	log("listening history: %d plays for admin", plays)
	log("pre-filled in %s total", time.Since(started).Round(time.Millisecond))
	return u, nil
}

// setupInstance writes config.json and creates the database the app will open: the
// seeded defaults, admin/admin, the data sources pointed at the stand-in server, a
// native manager and one library on music/.
func setupInstance(dir string) (*gorm.DB, error) {
	configDir := filepath.Join(dir, "config")
	restore := files.SetConfigPaths(configDir)
	defer restore()
	if err := files.LoadConfig(); err != nil {
		return nil, err
	}
	files.ConfigFile.AutotaggerrPort = appPort
	files.ConfigFile.AutotaggerrName = "Autotaggerr demo"
	if err := files.SaveConfig(); err != nil {
		return nil, err
	}
	// Not logger.InitLogger: it opens config/autotaggerr.log relative to the working
	// directory, which from the repository root is the maintainer's own log. The
	// default logger writes to stderr; only warnings are worth showing here.
	logger.Log.SetLevel(logrus.WarnLevel)

	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(configDir, "autotaggerr.db")})
	if err != nil {
		return nil, err
	}
	if err := database.ApplyDataMigrations(db); err != nil {
		return nil, err
	}
	if _, err := database.Seed(db); err != nil {
		return nil, err
	}
	hash, err := auth.HashPassword("admin")
	if err != nil {
		return nil, err
	}
	if err := db.Model(&models.User{}).Where("username = ?", "admin").Update("password_hash", hash).Error; err != nil {
		return nil, err
	}

	base := "http://" + stubAddr
	if err := db.Model(&models.DataSource{}).Where("type = ?", models.DataSourceTypeMusicBrainz).
		Updates(map[string]any{"base_url": base + "/ws/2", "rate_limit": 50, "contact": "autotaggerr-demo"}).Error; err != nil {
		return nil, err
	}
	if err := db.Model(&models.DataSource{}).Where("type = ?", models.DataSourceTypeCoverArtArchive).
		Update("base_url", base+"/caa").Error; err != nil {
		return nil, err
	}

	manager := models.Manager{Name: "Autotaggerr", Type: models.ManagerTypeAutotaggerr, Enabled: true}
	if err := db.Create(&manager).Error; err != nil {
		return nil, err
	}
	var profile models.TaggerProfile
	db.Order("created_at").First(&profile)
	var source models.DataSource
	db.Where("type = ?", models.DataSourceTypeMusicBrainz).First(&source)
	library := models.Library{Name: "Demo library", Path: filepath.Join(dir, "music"), ManagerID: &manager.ID, Enabled: true}
	if profile.ID != [16]byte{} {
		library.TaggerProfileID = &profile.ID
	}
	if source.ID != [16]byte{} {
		library.DataSourceID = &source.ID
	}
	if err := db.Create(&library).Error; err != nil {
		return nil, err
	}
	return db, nil
}

// prefill records what a first Process would have: the release cache for every held
// release, an index row per file, then the collection and the graph credits.
func prefill(db *gorm.DB, u *Universe, specs []fileSpec, dir string) error {
	var library models.Library
	if err := db.First(&library).Error; err != nil {
		return err
	}

	now := time.Now()
	held := map[string]*Release{}
	for _, h := range u.Holdings {
		held[h.Release.ID] = h.Release
	}
	var rows []models.MusicbrainzReleaseCache
	for id, rel := range held {
		model, err := ReleaseModel(rel)
		if err != nil {
			return err
		}
		payload, err := json.Marshal(model)
		if err != nil {
			return err
		}
		rows = append(rows, models.MusicbrainzReleaseCache{MBID: id, Payload: string(payload), FetchedAt: now, ExpiresAt: now.Add(30 * 24 * time.Hour)})
	}
	if err := db.CreateInBatches(rows, 200).Error; err != nil {
		return err
	}

	items := make([]models.LibraryItem, 0, len(specs))
	for _, s := range specs {
		info, err := os.Stat(s.path)
		if err != nil {
			return err
		}
		mt := info.ModTime()
		item := models.LibraryItem{LibraryID: library.ID, Path: s.path, Size: info.Size(), ModTime: &mt}
		// Measured from the file, as Process would: a header read, so cheap at any size.
		if props, err := modules.ReadAudioProperties(s.path); err == nil {
			item.Quality = string(props.Quality())
			item.Bitrate, item.SampleRate, item.BitDepth = props.BitrateKbps, props.SampleRate, props.BitsPerSample
			item.QualitySize, item.QualityModTime = item.Size, &mt
		}
		if s.release == nil {
			item.Status = models.LibraryItemStatusUnmatched
			item.Error = "no MusicBrainz IDs in the file's tags"
		} else {
			item.Status = models.LibraryItemStatusOK
			item.MBReleaseID, item.MBRecordingID, item.MBReleaseTrackID = s.release.ID, s.track.Recording.ID, s.track.ID
			item.CorrelationSource = models.CorrelationSourceTags
			item.CorrelatedByManager = models.ManagerTypeAutotaggerr
			item.CorrelatedAt, item.LastScannedAt = &now, &now
		}
		items = append(items, item)
	}
	if err := db.CreateInBatches(items, 500).Error; err != nil {
		return err
	}

	modules.SetDB(db)
	modules.LoadAllCaches()
	if _, err := collection.Rebuild(db); err != nil {
		return err
	}
	if err := wantInQuality(db, library); err != nil {
		return err
	}
	_, err := insights.SyncCredits(db)
	return err
}

// wantInQuality gives the demo something to want and a quality to want it in: the
// library uses the Lossless preset, and every third artist (by MBID, so it is stable)
// is followed. Their MP3 albums are then marked Upgrade, and the discography read at
// startup fills in what they are missing from the stand-in.
func wantInQuality(db *gorm.DB, library models.Library) error {
	var lossless models.QualityProfile
	if err := db.Where("name = ?", "Lossless").First(&lossless).Error; err != nil {
		return fmt.Errorf("the Lossless quality profile was not seeded: %w", err)
	}
	if err := db.Model(&library).Update("quality_profile_id", lossless.ID).Error; err != nil {
		return err
	}
	var artists []string
	if err := db.Model(&models.CollectionArtist{}).Where("mb_id <> ?", models.VariousArtistsMBID).
		Order("mb_id").Pluck("mb_id", &artists).Error; err != nil {
		return err
	}
	followed := []string{}
	for i, id := range artists {
		if i%3 == 0 {
			followed = append(followed, id)
		}
	}
	for _, chunk := range chunks(followed, 500) {
		if err := db.Model(&models.CollectionArtist{}).Where("mb_id IN ?", chunk).Update("monitored", true).Error; err != nil {
			return err
		}
	}
	return nil
}

// chunks splits ids for IN clauses SQLite can take.
func chunks(ids []string, size int) [][]string {
	var out [][]string
	for len(ids) > size {
		out, ids = append(out, ids[:size]), ids[size:]
	}
	if len(ids) > 0 {
		out = append(out, ids)
	}
	return out
}

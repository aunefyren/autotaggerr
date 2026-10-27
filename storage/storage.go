// Package storage measures where the bytes are: the music in each library, and
// Autotaggerr's own data in config/ — the database by table, the artwork cache, the
// log — against the free space of the disks they sit on.
//
// The two are reported apart on purpose. Music is hundreds of gigabytes to terabytes
// and the app's own data is megabytes; drawn on one scale, the second is invisible,
// and it is the one that grows without anyone noticing (an unrotated log, a write-ahead
// log larger than the database, Activity detail on a big library).
//
// Music sizes come from the index (library_items.size, written by every scan), not a
// walk of the disk: a library is one query rather than a stat of every file. They are
// therefore as of the last scan, and the page says so.
package storage

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// CacheFor is how long a measurement is served before it is taken again. Sizing the
// database by table reads every page of it, which is not free on a large install, and
// nothing here changes by the minute.
const CacheFor = 10 * time.Minute

// LeftoverAge is how old an artwork leftover must be before it is reported or removed.
// An image is written to disk before its index row, so a younger file may be one
// being fetched right now.
const LeftoverAge = time.Hour

// Report is one measurement. App is the admin half — paths, the database by table and
// the actions — and is nil in what a non-admin is sent.
type Report struct {
	MeasuredAt     time.Time `json:"measured_at"`
	Volumes        []Volume  `json:"volumes"`
	Libraries      []Library `json:"libraries"`
	LargestArtists []Ranked  `json:"largest_artists"`
	LargestAlbums  []Ranked  `json:"largest_albums"`
	Duplicates     Copies    `json:"duplicates"`
	App            *App      `json:"app,omitempty"`
}

// Volume is one disk, with what Autotaggerr put on it. Two libraries on one disk are
// one volume: the question is whether that disk is running out, not each folder.
type Volume struct {
	// Path names the volume by the first folder of ours found on it.
	Path      string   `json:"path"`
	Libraries []string `json:"libraries"`
	HoldsApp  bool     `json:"holds_app"`
	Total     uint64   `json:"total"`
	Free      uint64   `json:"free"`
	Music     int64    `json:"music"`
	App       int64    `json:"app"`
	Error     string   `json:"error,omitempty"`
}

// Library is one library's music as the index knows it.
type Library struct {
	ID           uuid.UUID `json:"id"`
	Name         string    `json:"name"`
	Path         string    `json:"path"`
	Files        int64     `json:"files"`
	Bytes        int64     `json:"bytes"`
	Formats      []Format  `json:"formats"`
	Unidentified Count     `json:"unidentified"`
}

// Format is the share of one file format in a library.
type Format struct {
	Format string `json:"format"`
	Files  int64  `json:"files"`
	Bytes  int64  `json:"bytes"`
}

// Count is a number of files and their size.
type Count struct {
	Files int64 `json:"files"`
	Bytes int64 `json:"bytes"`
}

// Ranked is an artist or an album in a largest-first list.
type Ranked struct {
	MBID       string `json:"mb_id"`
	Name       string `json:"name"`
	ArtistMBID string `json:"artist_mb_id,omitempty"`
	Artist     string `json:"artist,omitempty"`
	Files      int64  `json:"files"`
	Bytes      int64  `json:"bytes"`
	// Copies is, for an album, how many editions-in-a-library hold its files.
	Copies int `json:"copies,omitempty"`
	// Extra is, for an album held more than once, the size of every copy but the
	// largest.
	Extra int64 `json:"extra,omitempty"`
}

// Copies is albums held more than once — two editions, or one edition in two
// libraries — and the space beyond one copy of each. Not waste by definition (a
// deluxe beside the original is often the point), but the one figure on the music
// side that is space someone could get back.
type Copies struct {
	Albums int      `json:"albums"`
	Extra  int64    `json:"extra"`
	Top    []Ranked `json:"top"`
}

// App is Autotaggerr's own data.
type App struct {
	Dir      string   `json:"dir"`
	Total    int64    `json:"total"`
	Database Database `json:"database"`
	Artwork  Artwork  `json:"artwork"`
	Logs     Logs     `json:"logs"`
	// Other is every other file at the top of the config directory — config.json, and
	// whatever else has been left there (database backups, for one).
	Other []File `json:"other"`
}

// File is one file's name and size.
type File struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

// Database is the database's files and what is in them.
type Database struct {
	Path string `json:"path"`
	File int64  `json:"file"`
	// WAL is SQLite's write-ahead log; Checkpoint folds it back into the file.
	WAL int64 `json:"wal"`
	SHM int64 `json:"shm"`
	// Reclaimable is free pages inside the file: space Compact returns to the disk.
	Reclaimable int64        `json:"reclaimable"`
	Groups      []TableGroup `json:"groups"`
	Error       string       `json:"error,omitempty"`
}

// TableGroup is tables grouped by what they hold.
type TableGroup struct {
	Label  string  `json:"label"`
	Bytes  int64   `json:"bytes"`
	Rows   int64   `json:"rows"`
	Tables []Table `json:"tables"`
}

// Table is one table with its indexes.
type Table struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
	Rows  int64  `json:"rows"`
}

// Artwork is the artwork cache on disk.
type Artwork struct {
	Path  string `json:"path"`
	Files int64  `json:"files"`
	Bytes int64  `json:"bytes"`
	Kinds []Kind `json:"kinds"`
	// NoImage is how many lookups are remembered as having no image — rows, not files.
	NoImage int64 `json:"no_image"`
	// Leftovers are files nothing will serve: interrupted downloads (.tmp), and images
	// for keys the index now records as having none.
	Leftovers Count `json:"leftovers"`
}

// Kind is one kind of artwork image.
type Kind struct {
	Label string `json:"label"`
	Files int64  `json:"files"`
	Bytes int64  `json:"bytes"`
}

// Logs is the log file and its rotated copies.
type Logs struct {
	Files []File `json:"files"`
	Bytes int64  `json:"bytes"`
}

// Paths says where Autotaggerr's own data lives.
type Paths struct {
	ConfigDir  string
	ArtworkDir string
	LogFiles   func() []string
}

// Measurer takes and caches measurements.
type Measurer struct {
	db    *gorm.DB
	paths Paths
	now   func() time.Time

	mu   sync.Mutex
	last *Report
}

func NewMeasurer(db *gorm.DB, paths Paths) *Measurer {
	return &Measurer{db: db, paths: paths, now: time.Now}
}

// Report returns the last measurement while it is younger than CacheFor, or a new one.
// fresh forces a new one. Measurements are serialised: two page loads at once share one.
func (m *Measurer) Report(fresh bool) (Report, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !fresh && m.last != nil && m.now().Sub(m.last.MeasuredAt) < CacheFor {
		return *m.last, nil
	}
	report, err := m.measure()
	if err != nil {
		return Report{}, err
	}
	m.last = &report
	return report, nil
}

// Invalidate drops the cached measurement, after an action changed what it counted.
func (m *Measurer) Invalidate() {
	m.mu.Lock()
	m.last = nil
	m.mu.Unlock()
}

func (m *Measurer) measure() (Report, error) {
	report := Report{MeasuredAt: m.now()}

	libraries, err := m.libraries()
	if err != nil {
		return Report{}, err
	}
	report.Libraries = libraries
	if report.LargestArtists, err = m.largestArtists(10); err != nil {
		return Report{}, err
	}
	if report.LargestAlbums, err = m.largestAlbums(10); err != nil {
		return Report{}, err
	}
	if report.Duplicates, err = m.duplicates(10); err != nil {
		return Report{}, err
	}

	app := m.app()
	report.App = &app
	report.Volumes = volumes(libraries, app)
	return report, nil
}

func (m *Measurer) libraries() ([]Library, error) {
	var rows []models.Library
	if err := m.db.Order("name").Find(&rows).Error; err != nil {
		return nil, err
	}
	type total struct {
		LibraryID    uuid.UUID
		Files, Bytes int64
		UnidFiles    int64
		UnidBytes    int64
	}
	var totals []total
	if err := m.db.Raw(`SELECT library_id, COUNT(*) AS files, COALESCE(SUM(size), 0) AS bytes,
		COALESCE(SUM(CASE WHEN mb_release_id = '' THEN 1 ELSE 0 END), 0) AS unid_files,
		COALESCE(SUM(CASE WHEN mb_release_id = '' THEN size ELSE 0 END), 0) AS unid_bytes
		FROM library_items GROUP BY library_id`).Scan(&totals).Error; err != nil {
		return nil, err
	}
	type format struct {
		LibraryID    uuid.UUID
		Format       string
		Files, Bytes int64
	}
	var formats []format
	if err := m.db.Raw(`SELECT library_id,
		CASE WHEN LOWER(path) LIKE '%.flac' THEN 'FLAC' WHEN LOWER(path) LIKE '%.mp3' THEN 'MP3' ELSE 'Other' END AS format,
		COUNT(*) AS files, COALESCE(SUM(size), 0) AS bytes
		FROM library_items GROUP BY library_id, format ORDER BY bytes DESC`).Scan(&formats).Error; err != nil {
		return nil, err
	}

	byID := map[uuid.UUID]*Library{}
	out := make([]Library, len(rows))
	for i, l := range rows {
		out[i] = Library{ID: l.ID, Name: l.Name, Path: l.Path, Formats: []Format{}}
		byID[l.ID] = &out[i]
	}
	for _, t := range totals {
		if l := byID[t.LibraryID]; l != nil {
			l.Files, l.Bytes = t.Files, t.Bytes
			l.Unidentified = Count{Files: t.UnidFiles, Bytes: t.UnidBytes}
		}
	}
	for _, f := range formats {
		if l := byID[f.LibraryID]; l != nil {
			l.Formats = append(l.Formats, Format{Format: f.Format, Files: f.Files, Bytes: f.Bytes})
		}
	}
	return out, nil
}

func (m *Measurer) largestArtists(limit int) ([]Ranked, error) {
	out := []Ranked{}
	err := m.db.Raw(`SELECT COALESCE(rg.artist_mb_id, cr.artist_mb_id) AS mb_id, COALESCE(MAX(ca.name), '') AS name,
		COUNT(*) AS files, SUM(li.size) AS bytes
		FROM library_items li
		JOIN collection_releases cr ON cr.mb_id = li.mb_release_id
		LEFT JOIN collection_release_groups rg ON rg.mb_id = cr.release_group_mb_id
		LEFT JOIN collection_artists ca ON ca.mb_id = COALESCE(rg.artist_mb_id, cr.artist_mb_id)
		WHERE li.mb_release_id <> ''
		GROUP BY COALESCE(rg.artist_mb_id, cr.artist_mb_id) ORDER BY bytes DESC LIMIT ?`, limit).Scan(&out).Error
	return out, err
}

func (m *Measurer) largestAlbums(limit int) ([]Ranked, error) {
	out := []Ranked{}
	err := m.db.Raw(`SELECT cr.release_group_mb_id AS mb_id, COALESCE(MAX(rg.title), '') AS name,
		COALESCE(MAX(rg.artist_mb_id), MAX(cr.artist_mb_id)) AS artist_mb_id, COALESCE(MAX(ca.name), '') AS artist,
		COUNT(*) AS files, SUM(li.size) AS bytes, COUNT(DISTINCT li.mb_release_id || '|' || li.library_id) AS copies
		FROM library_items li
		JOIN collection_releases cr ON cr.mb_id = li.mb_release_id
		LEFT JOIN collection_release_groups rg ON rg.mb_id = cr.release_group_mb_id
		LEFT JOIN collection_artists ca ON ca.mb_id = COALESCE(rg.artist_mb_id, cr.artist_mb_id)
		WHERE li.mb_release_id <> ''
		GROUP BY cr.release_group_mb_id ORDER BY bytes DESC LIMIT ?`, limit).Scan(&out).Error
	return out, err
}

// duplicates finds albums whose files sit in more than one copy (a release in a
// library) and how much is beyond the largest copy of each.
func (m *Measurer) duplicates(limit int) (Copies, error) {
	type copyRow struct {
		Album string
		Copy  string
		Bytes int64
	}
	var rows []copyRow
	if err := m.db.Raw(`SELECT cr.release_group_mb_id AS album, li.mb_release_id || '|' || li.library_id AS copy, SUM(li.size) AS bytes
		FROM library_items li JOIN collection_releases cr ON cr.mb_id = li.mb_release_id
		WHERE li.mb_release_id <> '' GROUP BY album, copy`).Scan(&rows).Error; err != nil {
		return Copies{}, err
	}
	type album struct {
		copies       int
		total, large int64
	}
	albums := map[string]*album{}
	for _, r := range rows {
		a := albums[r.Album]
		if a == nil {
			a = &album{}
			albums[r.Album] = a
		}
		a.copies++
		a.total += r.Bytes
		if r.Bytes > a.large {
			a.large = r.Bytes
		}
	}
	out := Copies{Top: []Ranked{}}
	ranked := []Ranked{}
	for id, a := range albums {
		if a.copies < 2 {
			continue
		}
		out.Albums++
		out.Extra += a.total - a.large
		ranked = append(ranked, Ranked{MBID: id, Bytes: a.total, Copies: a.copies, Extra: a.total - a.large})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].Extra > ranked[j].Extra })
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	if len(ranked) > 0 {
		ids := make([]string, len(ranked))
		for i, r := range ranked {
			ids[i] = r.MBID
		}
		type name struct{ MBID, Title, ArtistMBID, Artist string }
		var names []name
		if err := m.db.Raw(`SELECT rg.mb_id, rg.title, rg.artist_mb_id, COALESCE(ca.name, '') AS artist
			FROM collection_release_groups rg LEFT JOIN collection_artists ca ON ca.mb_id = rg.artist_mb_id
			WHERE rg.mb_id IN ?`, ids).Scan(&names).Error; err != nil {
			return Copies{}, err
		}
		byID := map[string]name{}
		for _, n := range names {
			byID[n.MBID] = n
		}
		for i := range ranked {
			n := byID[ranked[i].MBID]
			ranked[i].Name, ranked[i].ArtistMBID, ranked[i].Artist = n.Title, n.ArtistMBID, n.Artist
		}
	}
	out.Top = ranked
	return out, nil
}

// app measures config/: the database, the artwork cache, the logs and the rest. Every
// part degrades to what it could read rather than failing the report — a page that
// shows the music because the artwork directory is unreadable is still useful.
func (m *Measurer) app() App {
	app := App{Dir: m.paths.ConfigDir, Other: []File{}}
	app.Database = m.database()
	app.Artwork = m.artwork()
	app.Logs = m.logs()

	accounted := map[string]bool{}
	for _, p := range []string{app.Database.Path, app.Database.Path + "-wal", app.Database.Path + "-shm"} {
		accounted[filepath.Clean(p)] = true
	}
	for _, f := range m.logFiles() {
		accounted[filepath.Clean(f)] = true
	}
	if entries, err := os.ReadDir(m.paths.ConfigDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			path := filepath.Clean(filepath.Join(m.paths.ConfigDir, e.Name()))
			if accounted[path] {
				continue
			}
			if info, err := e.Info(); err == nil {
				app.Other = append(app.Other, File{Name: e.Name(), Bytes: info.Size()})
			}
		}
	}
	sort.Slice(app.Other, func(i, j int) bool { return app.Other[i].Bytes > app.Other[j].Bytes })

	app.Total = app.Database.File + app.Database.WAL + app.Database.SHM + app.Artwork.Bytes + app.Logs.Bytes
	for _, f := range app.Other {
		app.Total += f.Bytes
	}
	return app
}

func (m *Measurer) logFiles() []string {
	if m.paths.LogFiles == nil {
		return nil
	}
	out := []string{}
	for _, f := range m.paths.LogFiles() {
		if abs, err := filepath.Abs(f); err == nil {
			out = append(out, abs)
		}
	}
	return out
}

func (m *Measurer) logs() Logs {
	out := Logs{Files: []File{}}
	for _, f := range m.logFiles() {
		if info, err := os.Stat(f); err == nil {
			out.Files = append(out.Files, File{Name: filepath.Base(f), Bytes: info.Size()})
			out.Bytes += info.Size()
		}
	}
	return out
}

func (m *Measurer) artwork() Artwork {
	out := Artwork{Path: m.paths.ArtworkDir, Kinds: []Kind{}}
	var noImage []string
	if err := m.db.Model(&models.ArtworkCacheEntry{}).Where("missing = ?", true).Pluck("key", &noImage).Error; err == nil {
		out.NoImage = int64(len(noImage))
	}
	missing := map[string]bool{}
	for _, k := range noImage {
		missing[k] = true
	}

	kinds := map[string]*Kind{}
	entries, err := os.ReadDir(m.paths.ArtworkDir)
	if err != nil {
		return out
	}
	cutoff := m.now().Add(-LeftoverAge)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out.Files++
		out.Bytes += info.Size()
		if isLeftover(e.Name(), missing) && info.ModTime().Before(cutoff) {
			out.Leftovers.Files++
			out.Leftovers.Bytes += info.Size()
		}
		label := artworkKind(e.Name())
		k := kinds[label]
		if k == nil {
			k = &Kind{Label: label}
			kinds[label] = k
		}
		k.Files++
		k.Bytes += info.Size()
	}
	for _, k := range kinds {
		out.Kinds = append(out.Kinds, *k)
	}
	sort.Slice(out.Kinds, func(i, j int) bool { return out.Kinds[i].Bytes > out.Kinds[j].Bytes })
	return out
}

// isLeftover: an interrupted download, or an image for a key the index says has none.
// A file with no index row is *not* a leftover — the artwork cache adopts those
// (modules.readArtworkCache).
func isLeftover(name string, noImage map[string]bool) bool {
	return strings.HasSuffix(name, ".tmp") || noImage[name]
}

// artworkKind names an artwork file by what it is, from its cache key
// (entity_mbid_kind_size, modules.artworkCacheKey).
func artworkKind(name string) string {
	if strings.HasSuffix(name, ".tmp") {
		return "Interrupted downloads"
	}
	parts := strings.Split(name, "_")
	if len(parts) != 4 {
		return "Other"
	}
	entity, kind, size := parts[0], parts[2], parts[3]
	switch {
	case entity == "artist" && kind == "thumb":
		return "Artist portraits"
	case entity == "artist" && kind == "background":
		return "Artist backdrops"
	case entity == "release-group" || entity == "release":
		return "Album covers, " + size + " px"
	}
	return "Other"
}

// volumes groups the libraries and the config directory by the disk they sit on.
func volumes(libraries []Library, app App) []Volume {
	out := []Volume{}
	index := map[string]int{}
	place := func(path string) (*Volume, bool) {
		d, err := diskUsage(path)
		if err != nil {
			out = append(out, Volume{Path: path, Libraries: []string{}, Error: unreachable(err)})
			return &out[len(out)-1], false
		}
		if i, ok := index[d.id]; ok {
			return &out[i], true
		}
		out = append(out, Volume{Path: path, Libraries: []string{}, Total: d.total, Free: d.free})
		index[d.id] = len(out) - 1
		return &out[len(out)-1], true
	}
	for _, l := range libraries {
		v, _ := place(l.Path)
		v.Libraries = append(v.Libraries, l.Name)
		v.Music += l.Bytes
	}
	if app.Dir != "" {
		v, _ := place(app.Dir)
		v.HoldsApp = true
		v.App += app.Total
	}
	return out
}

func unreachable(err error) string {
	if errors.Is(err, os.ErrNotExist) {
		return "the folder does not exist"
	}
	return err.Error()
}

type disk struct {
	id          string
	total, free uint64
}

package insights

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/database"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return db
}

func addLibrary(t *testing.T, db *gorm.DB, name string, enabled bool) models.Library {
	t.Helper()
	lib := models.Library{Name: name, Path: "/" + name, Enabled: true}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatal(err)
	}
	if !enabled {
		// Enabled is a plain bool; GORM skips a false on create.
		db.Model(&lib).Update("enabled", false)
	}
	return lib
}

// album records a release and its release group in the collection tables, the way
// collection.Rebuild would.
func album(t *testing.T, db *gorm.DB, releaseID, groupID, title, primary, secondary string) {
	t.Helper()
	group := models.CollectionReleaseGroup{MBID: groupID, ArtistMBID: "artist", Title: title, PrimaryType: primary, SecondaryTypes: secondary}
	if err := db.Where("mb_id = ?", groupID).FirstOrCreate(&group).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.CollectionRelease{MBID: releaseID, ReleaseGroupMBID: groupID, Title: title}).Error; err != nil {
		t.Fatal(err)
	}
}

var stamp = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

type fileOpt func(*models.LibraryItem)

// hashed gives the item an audio hash taken of the file as the index last saw it.
func hashed(h string) fileOpt {
	return func(i *models.LibraryItem) {
		i.AudioHash, i.AudioHashSize, i.AudioHashModTime = h, i.Size, &stamp
	}
}

func addItem(t *testing.T, db *gorm.DB, lib models.Library, path, release, recording string, opts ...fileOpt) models.LibraryItem {
	t.Helper()
	item := models.LibraryItem{
		LibraryID: lib.ID, Path: path, Size: 1000, ModTime: &stamp,
		MBReleaseID: release, MBRecordingID: recording, MBReleaseTrackID: "track-" + recording,
		Status: models.LibraryItemStatusOK,
	}
	for _, o := range opts {
		o(&item)
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	return item
}

func groupFor(t *testing.T, report DuplicatesReport, recording string) DuplicateGroup {
	t.Helper()
	for _, g := range report.Groups {
		if g.RecordingMBID == recording {
			return g
		}
	}
	t.Fatalf("no group for %s in %+v", recording, report.Groups)
	return DuplicateGroup{}
}

func hasGroup(report DuplicatesReport, recording string) bool {
	for _, g := range report.Groups {
		if g.RecordingMBID == recording {
			return true
		}
	}
	return false
}

// TestDuplicateLevels: within one album the most actionable pair decides; the same
// recording on two albums is an across-release group however alike the copies are.
func TestDuplicateLevels(t *testing.T) {
	db := testDB(t)
	lib := addLibrary(t, db, "music", true)
	album(t, db, "rel-album", "rg-album", "The Album", "Album", "")
	album(t, db, "rel-deluxe", "rg-album", "The Album (Deluxe)", "Album", "")
	album(t, db, "rel-single", "rg-single", "The Single", "Single", "")
	album(t, db, "rel-best", "rg-best", "Best Of", "Album", "Compilation")

	// identical: same hash, two editions of one album
	addItem(t, db, lib, "/music/a/01.flac", "rel-album", "rec-ident", hashed("h1"))
	addItem(t, db, lib, "/music/a2/01.flac", "rel-deluxe", "rec-ident", hashed("h1"))
	// mixed format
	addItem(t, db, lib, "/music/a/02.flac", "rel-album", "rec-mixed")
	addItem(t, db, lib, "/music/a/02.mp3", "rel-album", "rec-mixed")
	// same format, not checked
	addItem(t, db, lib, "/music/a/03.flac", "rel-album", "rec-same")
	addItem(t, db, lib, "/music/a2/03.flac", "rel-deluxe", "rec-same")
	// same format, checked and different
	addItem(t, db, lib, "/music/a/04.flac", "rel-album", "rec-diff", hashed("x"))
	addItem(t, db, lib, "/music/a2/04.flac", "rel-deluxe", "rec-diff", hashed("y"))
	// single also on the album, same audio
	addItem(t, db, lib, "/music/a/05.flac", "rel-album", "rec-single", hashed("s"))
	addItem(t, db, lib, "/music/s/01.flac", "rel-single", "rec-single", hashed("s"))
	// on a compilation
	addItem(t, db, lib, "/music/a/06.flac", "rel-album", "rec-comp")
	addItem(t, db, lib, "/music/b/01.flac", "rel-best", "rec-comp")
	// not a duplicate
	addItem(t, db, lib, "/music/a/07.flac", "rel-album", "rec-alone")

	// The group's title and artist come from the cached release, credit joined.
	modules.ResetMusicBrainzCachesForTest(t)
	named := track("track-rec-ident", "rec-ident", 1000, false)
	named.Title = "Song"
	named.ArtistCredit = []models.ArtistCredit{{Name: "A", Joinphrase: " feat. "}, {Name: "B"}}
	seedRelease(t, "rel-album", "rg-album", "Album", "", "", "", "", named)

	report, err := Duplicates(db, DuplicateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if g := groupFor(t, report, "rec-ident"); g.Title != "Song" || g.Artist != "A feat. B" {
		t.Errorf("identical group named %q by %q, want Song by A feat. B", g.Title, g.Artist)
	}
	want := map[Level]int{LevelIdentical: 1, LevelMixedFormat: 1, LevelSameFormat: 2, LevelAcrossReleases: 2}
	for level, n := range want {
		if report.Counts[level] != n {
			t.Errorf("count %s = %d, want %d", level, report.Counts[level], n)
		}
	}
	if len(report.Groups) != 4 {
		t.Fatalf("default view lists %d groups, want 4 (across-release hidden)", len(report.Groups))
	}
	if report.Groups[0].Level != LevelIdentical {
		t.Errorf("first group is %s, want identical first", report.Groups[0].Level)
	}
	if g := groupFor(t, report, "rec-same"); !g.AudioUnchecked {
		t.Error("an unhashed same-format group is not marked unchecked")
	}
	if g := groupFor(t, report, "rec-diff"); g.AudioUnchecked || g.Level != LevelSameFormat {
		t.Errorf("a hashed, different group = %+v, want checked same_format", g)
	}
	if g := groupFor(t, report, "rec-mixed"); g.AudioUnchecked {
		t.Error("a mixed-format group asks for a hash two formats can never share")
	}
	if g := groupFor(t, report, "rec-ident"); len(g.Files) != 2 || g.Files[0].ReleaseTitle == "" || g.Files[0].ReleaseGroupMBID != "rg-album" {
		t.Errorf("identical group files = %+v", g.Files)
	}

	report, _ = Duplicates(db, DuplicateOptions{IncludeAcrossReleases: true})
	if g := groupFor(t, report, "rec-single"); g.Level != LevelAcrossReleases || !g.IdenticalAudio {
		t.Errorf("single on album = %+v, want across_releases with identical audio", g)
	}
	if hasGroup(report, "rec-comp") || report.HiddenCompilations != 1 {
		t.Errorf("compilation listed without asking (hidden=%d)", report.HiddenCompilations)
	}
	report, _ = Duplicates(db, DuplicateOptions{IncludeAcrossReleases: true, IncludeCompilations: true})
	if g := groupFor(t, report, "rec-comp"); !g.Compilation {
		t.Error("compilation group not marked")
	}

	// The unchecked candidates are the same-format, same-album files without a hash:
	// rec-same's two. Not rec-mixed (two formats), not rec-comp (two albums).
	unchecked, err := UncheckedCandidates(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(unchecked) != 2 || unchecked[0].MBRecordingID != "rec-same" || unchecked[1].MBRecordingID != "rec-same" {
		t.Errorf("unchecked candidates = %+v", unchecked)
	}
}

// TestDuplicatesLibraries: files in different libraries are compared only on request,
// disabled libraries never, and one library's view only sees its own files.
func TestDuplicatesLibraries(t *testing.T) {
	db := testDB(t)
	flac := addLibrary(t, db, "flac", true)
	mp3 := addLibrary(t, db, "mp3", true)
	off := addLibrary(t, db, "off", false)
	album(t, db, "rel", "rg", "Album", "Album", "")
	addItem(t, db, flac, "/flac/01.flac", "rel", "rec")
	addItem(t, db, mp3, "/mp3/01.mp3", "rel", "rec")
	addItem(t, db, off, "/off/01.flac", "rel", "rec-off")
	addItem(t, db, flac, "/flac/02.flac", "rel", "rec-off")

	report, _ := Duplicates(db, DuplicateOptions{})
	if len(report.Groups) != 0 {
		t.Errorf("a lossy mirror library is reported without asking: %+v", report.Groups)
	}
	report, _ = Duplicates(db, DuplicateOptions{AcrossLibraries: true})
	if len(report.Groups) != 1 || report.Groups[0].Level != LevelMixedFormat || report.Groups[0].Libraries != 2 {
		t.Errorf("across libraries = %+v, want one mixed group in 2 libraries", report.Groups)
	}
	report, _ = Duplicates(db, DuplicateOptions{AcrossLibraries: true, Library: &flac.ID})
	if len(report.Groups) != 0 {
		t.Errorf("one library's view sees another's files: %+v", report.Groups)
	}
}

// TestHashCurrent: a hash goes out of date when the file's size or mtime moves —
// the audio is then unchecked, not different.
func TestHashCurrent(t *testing.T) {
	later := stamp.Add(time.Minute)
	item := models.LibraryItem{Size: 10, ModTime: &stamp, AudioHash: "h", AudioHashSize: 10, AudioHashModTime: &stamp}
	if !hashCurrent(item) {
		t.Error("a hash of the file as indexed is not current")
	}
	item.ModTime = &later
	if hashCurrent(item) {
		t.Error("a hash survives an mtime change")
	}
	item.ModTime, item.Size = &stamp, 11
	if hashCurrent(item) {
		t.Error("a hash survives a size change")
	}
}

// TestVerifyDuplicates: a file is hashed once; a hash the tempo analysis already took
// of the file as it is now is reused without reading it; a missing file is left to
// the scan; a read failure is reported; and afterwards the identical pair says so.
func TestVerifyDuplicates(t *testing.T) {
	db := testDB(t)
	lib := addLibrary(t, db, "music", true)
	album(t, db, "rel-a", "rg", "Album", "Album", "")
	album(t, db, "rel-b", "rg", "Album (Deluxe)", "Album", "")
	dir := t.TempDir()

	write := func(name, body string) (string, os.FileInfo) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		info, _ := os.Stat(p)
		return p, info
	}
	index := func(p string, info os.FileInfo, release string) models.LibraryItem {
		mt := info.ModTime()
		item := models.LibraryItem{LibraryID: lib.ID, Path: p, Size: info.Size(), ModTime: &mt, MBReleaseID: release, MBRecordingID: "rec", Status: models.LibraryItemStatusOK}
		if err := db.Create(&item).Error; err != nil {
			t.Fatal(err)
		}
		return item
	}
	pa, ia := write("a.flac", "same audio")
	pb, ib := write("b.flac", "same audio")
	pc, ic := write("c.flac", "unreadable")
	index(pa, ia, "rel-a")
	index(pb, ib, "rel-b")
	index(pc, ic, "rel-b")
	mtB := ib.ModTime()
	if err := db.Create(&models.TrackTempo{Path: pb, Size: ib.Size(), ModTime: &mtB, AudioHash: "hash:same audio"}).Error; err != nil {
		t.Fatal(err)
	}
	gone := models.LibraryItem{Path: filepath.Join(dir, "gone.flac")}
	gone.ID = uuid.New()

	reads := map[string]int{}
	hash := func(p string) (string, error) {
		reads[p]++
		if p == pc {
			return "", errors.New("no audio")
		}
		body, err := os.ReadFile(p)
		return "hash:" + string(body), err
	}

	candidates, err := UncheckedCandidates(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 3 {
		t.Fatalf("candidates = %d, want 3", len(candidates))
	}
	var progressed int
	res := VerifyDuplicates(context.Background(), db, append(candidates, gone), hash, func(string) { progressed++ })
	if res.Hashed != 1 || res.Reused != 1 || res.Gone != 1 || len(res.Failures) != 1 || progressed != 4 {
		t.Errorf("result = %+v (progress %d), want 1 hashed, 1 reused, 1 gone, 1 failed", res, progressed)
	}
	if reads[pb] != 0 {
		t.Error("a file the tempo analysis had already hashed was read again")
	}

	report, _ := Duplicates(db, DuplicateOptions{})
	if len(report.Groups) != 1 || report.Groups[0].Level != LevelIdentical {
		t.Fatalf("after verifying, groups = %+v, want one identical", report.Groups)
	}

	// A second pass reads nothing it already has.
	reads = map[string]int{}
	var stored []models.LibraryItem
	db.Where("path IN ?", []string{pa, pb}).Find(&stored)
	res = VerifyDuplicates(context.Background(), db, stored, hash, nil)
	if res.Fresh != 2 || len(reads) != 0 {
		t.Errorf("second pass = %+v, reads %v; want both fresh, none read", res, reads)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if res := VerifyDuplicates(ctx, db, stored, hash, nil); !res.Cancelled || res.Fresh != 0 {
		t.Errorf("a cancelled pass = %+v", res)
	}
}

func seedRelease(t *testing.T, id, group, primary, date, country, label, genre string, tracks ...models.Track) {
	t.Helper()
	var release models.MusicBrainzReleaseResponse
	release.ID = id
	release.Country = country
	release.Date = date
	release.ReleaseGroup.ID = group
	release.ReleaseGroup.PrimaryType = primary
	release.ReleaseGroup.ArtistCredit = []models.ArtistCredit{{Artist: models.Artist{ID: "artist-" + group}}}
	if genre != "" {
		release.ReleaseGroup.Genres = append(release.ReleaseGroup.Genres, struct {
			Count          int    `json:"count"`
			ID             string `json:"id"`
			Name           string `json:"name"`
			Disambiguation string `json:"disambiguation"`
		}{Count: 3, Name: genre})
	}
	if label != "" {
		release.LabelInfo = make([]struct {
			CatalogNumber string `json:"catalog-number"`
			Label         struct {
				LabelCode      int    `json:"label-code"`
				Type           string `json:"type"`
				TypeID         string `json:"type-id"`
				Disambiguation string `json:"disambiguation"`
				SortName       string `json:"sort-name"`
				Genres         []struct {
					ID             string `json:"id"`
					Disambiguation string `json:"disambiguation"`
					Count          int    `json:"count"`
					Name           string `json:"name"`
				} `json:"genres"`
				Tags []struct {
					Name  string `json:"name"`
					Count int    `json:"count"`
				} `json:"tags"`
				Name string `json:"name"`
				ID   string `json:"id"`
			} `json:"label"`
		}, 1)
		release.LabelInfo[0].Label.Name = label
	}
	release.Media = []models.MusicBrainzMedia{{Tracks: tracks}}
	modules.SeedMusicBrainzReleaseForTest(t, release)
}

func track(id, recording string, length int, isrc bool) models.Track {
	var tr models.Track
	tr.ID, tr.Length = id, length
	tr.Recording.ID = recording
	if isrc {
		tr.Recording.ISRCs = []string{"ISRC"}
	}
	return tr
}

// TestLibraryStats: counts, breakdowns read from the cached release, coverage, and
// the catalogue comparison — which is collection-wide and so absent from one
// library's view.
func TestLibraryStats(t *testing.T) {
	modules.ResetMusicBrainzCachesForTest(t)
	db := testDB(t)
	lib := addLibrary(t, db, "music", true)
	other := addLibrary(t, db, "other", true)
	off := addLibrary(t, db, "off", false)

	seedRelease(t, "rel-1", "rg-1", "Album", "1994-05-01", "GB", "Label A", "rock",
		track("t1", "r1", 200000, true), track("t2", "r2", 100000, false))
	seedRelease(t, "rel-2", "rg-2", "Single", "2003", "US", "", "",
		track("t3", "r3", 60000, true))

	a := addItem(t, db, lib, "/music/1.flac", "rel-1", "r1")
	db.Model(&a).Update("mb_release_track_id", "t1")
	b := addItem(t, db, lib, "/music/2.flac", "rel-1", "r2")
	db.Model(&b).Updates(map[string]any{"mb_release_track_id": "t2", "pinned": true})
	addItem(t, db, other, "/other/3.mp3", "rel-2", "r3")
	addItem(t, db, other, "/other/4.mp3", "rel-uncached", "r4")
	unmatched := addItem(t, db, other, "/other/5.mp3", "", "")
	db.Model(&unmatched).Update("status", models.LibraryItemStatusUnmatched)
	addItem(t, db, off, "/off/6.flac", "rel-1", "r1")

	db.Create(&models.TrackTempo{Path: "/music/1.flac", BPM: 120})
	db.Create(&models.TrackLoudness{Path: "/music/2.flac", Analyser: "rsgain"})
	db.Create(&models.TrackLoudness{Path: "/other/3.mp3", Analyser: "rsgain", Error: "failed"})

	db.Create(&models.CollectionReleaseGroup{MBID: "rg-wanted", ArtistMBID: "x", InCatalog: true, CatalogMonitored: true})
	db.Create(&models.CollectionReleaseGroup{MBID: "rg-part", ArtistMBID: "x", Owned: true, OwnedTracks: 1, TotalTracks: 2, InCatalog: true})
	db.Create(&models.CollectionReleaseGroup{MBID: "rg-loose", ArtistMBID: "x", Owned: true, OwnedTracks: 1, TotalTracks: 1})

	stats, err := LibraryStats(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files != 5 || stats.Identified != 4 || stats.Bytes != 5000 {
		t.Errorf("files/identified/bytes = %d/%d/%d, want 5/4/5000", stats.Files, stats.Identified, stats.Bytes)
	}
	if stats.Uncached != 1 || stats.DurationMS != 360000 {
		t.Errorf("uncached/duration = %d/%d, want 1/360000", stats.Uncached, stats.DurationMS)
	}
	if stats.Releases != 3 || stats.Recordings != 4 || stats.Albums != 2 || stats.Artists != 2 {
		t.Errorf("releases/recordings/albums/artists = %d/%d/%d/%d", stats.Releases, stats.Recordings, stats.Albums, stats.Artists)
	}
	if len(stats.Formats) != 2 || stats.Formats[0].Key != "MP3" || stats.Formats[0].Files != 3 {
		t.Errorf("formats = %+v, want MP3 (3) first", stats.Formats)
	}
	if len(stats.Decades) != 2 || stats.Decades[0].Key != "1990s" || stats.Decades[1].Key != "2000s" {
		t.Errorf("decades = %+v, want 1990s then 2000s", stats.Decades)
	}
	if stats.Genres[0].Key != "rock" || stats.Genres[len(stats.Genres)-1].Key != unknownKey {
		t.Errorf("genres = %+v, want rock first and Unknown last", stats.Genres)
	}
	c := stats.Coverage
	if c.Unmatched != 1 || c.Pinned != 1 || c.ISRC != 2 || c.BPM != 1 || c.ReplayGain != 1 {
		t.Errorf("coverage = %+v", c)
	}
	if stats.Catalogue == nil || *stats.Catalogue != (CatalogueGap{Wanted: 1, Incomplete: 1, Unlisted: 1}) {
		t.Errorf("catalogue = %+v", stats.Catalogue)
	}

	stats, _ = LibraryStats(db, &lib.ID)
	if stats.Files != 2 || stats.Catalogue != nil {
		t.Errorf("one library: files %d, catalogue %+v; want 2 and none", stats.Files, stats.Catalogue)
	}
}

// TestTopBuckets: a long breakdown keeps the largest values and sums the rest into
// Other, so the total still adds up.
func TestTopBuckets(t *testing.T) {
	tl := tally{}
	for i := 0; i < 5; i++ {
		for j := 0; j <= i; j++ {
			tl.add(string(rune('a'+i)), 1)
		}
	}
	tl.add("", 1)
	got := tl.byFiles(2)
	if len(got) != 4 || got[0].Key != "e" || got[2].Key != "Other" || got[2].Files != 6 || got[3].Key != unknownKey {
		t.Errorf("byFiles(2) = %+v", got)
	}
}

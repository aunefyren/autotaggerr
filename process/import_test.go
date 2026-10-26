package process

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/events"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

func importRelease() models.MusicBrainzReleaseResponse {
	credit := []models.ArtistCredit{{Name: "Band", Artist: models.Artist{ID: "art-1", Name: "Band"}}}
	return models.MusicBrainzReleaseResponse{
		ID: "rel-1", Title: "Album", Date: "2001-05-01", ArtistCredit: credit,
		ReleaseGroup: models.ReleaseGroup{ID: "rg-1", Title: "Album", PrimaryType: "Album", FirstReleaseDate: "2001-05-01", ArtistCredit: credit},
		Media: []models.MusicBrainzMedia{{Position: 1, Format: "CD", Tracks: []models.Track{
			{ID: "trk-1", Title: "Song One", Position: 1, Number: "1"},
			{ID: "trk-2", Title: "Song Two", Position: 2, Number: "2"},
		}}},
	}
}

type importWorld struct {
	db      *gorm.DB
	runner  *Runner
	library models.Library
	release models.MusicBrainzReleaseResponse
	root    string // the import source root
}

// newImportWorld is a native library with a write-enabled profile, a cached release,
// and a source folder holding the album as a downloader would leave it.
func newImportWorld(t *testing.T) importWorld {
	t.Helper()
	requireAudioTools(t)
	db := newTestDB(t)
	release := importRelease()
	seedReleaseCache(t, db, release)

	profile := models.TaggerProfile{Name: "Write", WriteTags: true}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatal(err)
	}
	library := models.Library{Name: "Music", Path: t.TempDir(), Enabled: true, TaggerProfileID: &profile.ID}
	if err := db.Create(&library).Error; err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	synthFlacAt(t, filepath.Join(root, "Band - Album", "01 song one.flac"))
	synthFlacAt(t, filepath.Join(root, "Band - Album", "02 song two.flac"))

	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})
	t.Cleanup(r.Wait)
	return importWorld{db: db, runner: r, library: library, release: release, root: root}
}

func (w importWorld) sources() []string {
	return []string{
		filepath.Join(w.root, "Band - Album", "01 song one.flac"),
		filepath.Join(w.root, "Band - Album", "02 song two.flac"),
	}
}

func (w importWorld) run(t *testing.T, mode modules.TransferMode, rows []ImportRow) {
	t.Helper()
	plan := ImportPlan{Library: w.library, Release: w.release, Root: w.root, Mode: mode, Rows: rows}
	if err := w.runner.Import(plan); err != nil {
		t.Fatalf("Import: %v", err)
	}
	w.runner.waitIdle(t)
}

// TestImportMovesTagsAndPins: a reviewed import moves each file to where the profile
// names it, indexes it pinned to the chosen track, tags it, removes the folder the
// move emptied, and reports each file with where it came from.
func TestImportMovesTagsAndPins(t *testing.T) {
	w := newImportWorld(t)
	rows := w.runner.ProposeImport(w.library, w.release, w.sources())
	for _, row := range rows {
		if row.MBReleaseTrackID == "" || row.Conflict != "" || row.Error != "" {
			t.Fatalf("proposal: %+v", row)
		}
	}
	if rows[0].TargetRel != "Band/Album (2001)/01 Song One.flac" {
		t.Errorf("target = %q", rows[0].TargetRel)
	}

	w.run(t, modules.TransferMove, rows)

	for _, row := range rows {
		if _, err := os.Stat(row.Target); err != nil {
			t.Errorf("not at its target: %v", err)
		}
		if _, err := os.Stat(row.Source); !os.IsNotExist(err) {
			t.Errorf("a move must take the source away: %s", row.Source)
		}
		var item models.LibraryItem
		if err := w.db.First(&item, "path = ?", row.Target).Error; err != nil {
			t.Fatalf("not indexed: %v", err)
		}
		if !item.Pinned || item.CorrelationSource != models.CorrelationSourceManual || item.MBReleaseTrackID != row.MBReleaseTrackID {
			t.Errorf("index row = %+v, want a manual pin to %s", item, row.MBReleaseTrackID)
		}
		if item.ProcessedVersion != "test" || item.PinnedAudioHash == "" {
			t.Errorf("want tagged (version stamped) with the audio recorded: version=%q hash=%q", item.ProcessedVersion, item.PinnedAudioHash)
		}
		if id, _ := modules.ExtractMusicBrainzReleaseID(row.Target); id != "rel-1" {
			t.Errorf("tags not written: release id %q", id)
		}
	}
	if _, err := os.Stat(filepath.Join(w.root, "Band - Album")); !os.IsNotExist(err) {
		t.Error("the emptied source album folder should be removed")
	}
	if _, err := os.Stat(w.root); err != nil {
		t.Error("the import root itself stays")
	}

	var event models.Event
	if err := w.db.Where("type = ?", models.EventTypeFileImport).First(&event).Error; err != nil {
		t.Fatalf("no import event: %v", err)
	}
	if event.Status != models.EventStatusOK {
		t.Errorf("event status = %s (%s)", event.Status, event.Summary)
	}
	items, _ := events.Items(w.db, event.ID)
	if len(items) != 2 || items[0].From == "" || items[0].Phase != models.EventItemPhaseImport {
		t.Errorf("event items = %+v", items)
	}
	var album models.CollectionReleaseGroup
	if err := w.db.First(&album, "mb_id = ?", "rg-1").Error; err != nil {
		t.Errorf("the imported album should be in the collection: %v", err)
	}
}

func TestImportCopyLeavesTheSource(t *testing.T) {
	w := newImportWorld(t)
	rows := w.runner.ProposeImport(w.library, w.release, w.sources())
	w.run(t, modules.TransferCopy, rows)
	for _, row := range rows {
		if _, err := os.Stat(row.Source); err != nil {
			t.Errorf("a copy must leave the source: %v", err)
		}
		if _, err := os.Stat(row.Target); err != nil {
			t.Errorf("not copied: %v", err)
		}
	}
}

// TestImportReplacesOnlyWhatWasReviewed: a file at the target and an indexed copy of
// the same track elsewhere are both conflicts. Without consent the plan is refused;
// with it, both go and the target's index row is kept.
func TestImportReplacesOnlyWhatWasReviewed(t *testing.T) {
	w := newImportWorld(t)
	atTarget := synthFlacAt(t, filepath.Join(w.library.Path, "Band", "Album (2001)", "01 Song One.flac"))
	elsewhere := synthFlacAt(t, filepath.Join(w.library.Path, "Band", "Old rip", "1.flac"))
	targetRow := models.LibraryItem{LibraryID: w.library.ID, Path: atTarget, MBReleaseID: "rel-1", MBReleaseTrackID: "trk-1", Status: models.LibraryItemStatusOK}
	oldRow := models.LibraryItem{LibraryID: w.library.ID, Path: elsewhere, MBReleaseID: "rel-1", MBReleaseTrackID: "trk-1", Status: models.LibraryItemStatusOK}
	for _, item := range []*models.LibraryItem{&targetRow, &oldRow} {
		if err := w.db.Create(item).Error; err != nil {
			t.Fatal(err)
		}
	}

	rows := w.runner.ProposeImport(w.library, w.release, w.sources())
	if rows[0].Conflict != ImportConflictExists || len(rows[0].Replaces) != 2 {
		t.Fatalf("row 1 = %+v, want a conflict replacing both copies", rows[0])
	}
	if err := CheckImportPlan(rows); err == nil || !strings.Contains(err.Error(), "Replace") {
		t.Errorf("an unconsented replacement must block the import: %v", err)
	}

	rows[0].Replace = true
	w.run(t, modules.TransferMove, rows)

	if _, err := os.Stat(elsewhere); !os.IsNotExist(err) {
		t.Error("the replaced copy elsewhere should be deleted")
	}
	var count int64
	w.db.Model(&models.LibraryItem{}).Where("path = ?", elsewhere).Count(&count)
	if count != 0 {
		t.Error("the replaced copy's index row should be gone")
	}
	var kept models.LibraryItem
	if err := w.db.First(&kept, "path = ?", atTarget).Error; err != nil || kept.ID != targetRow.ID || !kept.Pinned {
		t.Errorf("the target's row should be re-used and pinned: %+v %v", kept, err)
	}
}

// Two files mapped to one track render one target: a clash, which no consent fixes.
func TestImportClashBlocks(t *testing.T) {
	w := newImportWorld(t)
	rows := []ImportRow{
		{Source: w.sources()[0], MBReleaseTrackID: "trk-1", Replace: true},
		{Source: w.sources()[1], MBReleaseTrackID: "trk-1", Replace: true},
	}
	planned := w.runner.PlanImport(w.library, w.release, rows)
	if planned[0].Conflict != ImportConflictClash || planned[1].Conflict != ImportConflictClash {
		t.Fatalf("planned = %+v", planned)
	}
	if err := CheckImportPlan(planned); err == nil {
		t.Error("a clash must block the import")
	}
	if err := CheckImportPlan([]ImportRow{{Source: "x"}}); err == nil {
		t.Error("an import with every file skipped is not an import")
	}
}

// A second edition of an album rendering into the first one's folder: the default
// album folder names the album, not the edition, so without this the reviewer's only
// way through was Replace — deleting the first edition's same-numbered tracks and
// mixing its others in with the new ones.
func TestImportRefusesToMixEditions(t *testing.T) {
	w := newImportWorld(t)
	other := w.release
	other.ID, other.Date, other.Country, other.Disambiguation = "rel-2", "2011-01-01", "GB", "remaster"
	// Track IDs are release-scoped: another edition's tracks are other tracks.
	other.Media = []models.MusicBrainzMedia{{Position: 1, Format: "CD", Tracks: []models.Track{
		{ID: "trk-2-1", Title: "Song One", Position: 1, Number: "1"},
		{ID: "trk-2-2", Title: "Song Two", Position: 2, Number: "2"},
	}}}
	seedReleaseCache(t, w.db, other)
	existing := filepath.Join(w.library.Path, "Band", "Album (2001)", "02 Song Two.flac")
	if err := w.db.Create(&models.LibraryItem{LibraryID: w.library.ID, Path: existing, MBReleaseID: "rel-2", MBReleaseTrackID: "trk-2-2", Status: models.LibraryItemStatusOK}).Error; err != nil {
		t.Fatal(err)
	}

	rows := w.runner.ProposeImport(w.library, w.release, w.sources())
	for _, row := range rows {
		if row.Conflict != ImportConflictOtherEdition || row.ConflictDetail != "Album (2011, GB, remaster)" {
			t.Errorf("row = %+v, want other_edition naming the 2011 remaster", row)
		}
		row.Replace = true
	}
	err := CheckImportPlan(rows)
	if err == nil || !strings.Contains(err.Error(), "{Edition") {
		t.Errorf("CheckImportPlan = %v, want a refusal naming the edition tokens", err)
	}
	if importRowReady(rows[0], nil) == nil {
		t.Error("the job must refuse it too")
	}

	// An album folder template that tells the two apart takes the conflict away.
	w.library.TaggerProfileID = nil
	profile := models.TaggerProfile{Name: "Editions", WriteTags: true, NamingAlbumFolder: "{Album Title} ({Release Year}) [{Edition Year}]"}
	if err := w.db.Create(&profile).Error; err != nil {
		t.Fatal(err)
	}
	w.library.TaggerProfileID = &profile.ID
	if err := w.db.Save(&w.library).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range w.runner.ProposeImport(w.library, w.release, w.sources()) {
		if row.Conflict != "" {
			t.Errorf("with an edition token, row = %+v, want no conflict", row)
		}
	}
}

// Something that appeared at a target after the review is not replaced.
func TestImportRowReadyRefusesUnreviewedReplacement(t *testing.T) {
	src := filepath.Join(t.TempDir(), "a.flac")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	row := ImportRow{Source: src, MBReleaseTrackID: "trk-1", Target: "/lib/a.flac", Replaces: []string{"/lib/a.flac"}}
	if err := importRowReady(row, map[string]bool{}); err == nil {
		t.Error("an unreviewed replacement must be refused")
	}
	if err := importRowReady(row, map[string]bool{"/lib/a.flac": true}); err != nil {
		t.Errorf("a reviewed one is fine: %v", err)
	}
}

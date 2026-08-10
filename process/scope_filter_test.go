package process

import (
	"path/filepath"
	"sort"
	"testing"

	"github.com/aunefyren/autotaggerr/components"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
)

// TestScopeFilterAdmits: a run that walks no narrowed folders admits every file (the
// scheduled scan, which is what carries the collection's drift), while a narrowed one
// admits only what sits under its roots — in the libraries it actually covers.
func TestScopeFilterAdmits(t *testing.T) {
	root := "/music"
	library := models.Library{Base: models.Base{ID: uuid.New()}, Name: "Main", Path: root}
	other := models.Library{Base: models.Base{ID: uuid.New()}, Name: "Other", Path: "/other"}

	parliament := filepath.Join(root, "Parliament", "Mothership Connection (1975)", "01 track.flac")
	jayZ := filepath.Join(root, "Jay-Z & Chic", "Album (1998)", "01 track.flac")

	full := newScopeFilter(LibraryScope([]models.Library{library}))
	if !full.all() {
		t.Fatal("a whole-library scope must not narrow anything")
	}
	if !full.admits(models.LibraryItem{LibraryID: other.ID, Path: jayZ}) {
		t.Error("an unnarrowed filter must admit everything, including other libraries")
	}

	narrow := newScopeFilter(Scope{Targets: []Target{
		{Library: library, Roots: []string{filepath.Join(root, "Parliament")}},
	}})
	if narrow.all() {
		t.Fatal("a scope with roots must narrow")
	}
	if !narrow.admits(models.LibraryItem{LibraryID: library.ID, Path: parliament}) {
		t.Error("a file under the walked folder must be in scope")
	}
	if narrow.admits(models.LibraryItem{LibraryID: library.ID, Path: jayZ}) {
		t.Error("a file under another artist's folder must be out of scope")
	}
	if narrow.admits(models.LibraryItem{LibraryID: other.ID, Path: parliament}) {
		t.Error("a file in a library this run does not touch must be out of scope")
	}

	// A scope that narrows one library and covers another whole takes all of the
	// second: a library present with no roots is in scope in its entirety.
	mixed := newScopeFilter(Scope{Targets: []Target{
		{Library: library, Roots: []string{filepath.Join(root, "Parliament")}},
		{Library: other},
	}})
	if !mixed.admits(models.LibraryItem{LibraryID: other.ID, Path: "/other/anyone/album/01.flac"}) {
		t.Error("a target with no roots covers its whole library")
	}
	if mixed.admits(models.LibraryItem{LibraryID: library.ID, Path: jayZ}) {
		t.Error("narrowing one library must survive another being covered whole")
	}
}

// TestNarrowDueKeepsOnlyScopeReleases is the rate-limit half of the fix: the due list
// comes off the whole release cache, so without narrowing a one-artist scan force-fetches
// every expired release in the collection — the most expensive action in the app behind
// its cheapest-looking button.
func TestNarrowDueKeepsOnlyScopeReleases(t *testing.T) {
	db := newTestDB(t)
	root := t.TempDir()

	library := models.Library{Name: "L", Path: root, Enabled: true}
	if err := db.Create(&library).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	items := []models.LibraryItem{
		{LibraryID: library.ID, Path: filepath.Join(root, "Parliament", "Album (1975)", "01.flac"),
			MBReleaseID: "rel-parliament", Status: models.LibraryItemStatusOK},
		{LibraryID: library.ID, Path: filepath.Join(root, "Jay-Z & Chic", "Album (1998)", "01.flac"),
			MBReleaseID: "rel-jayz", Status: models.LibraryItemStatusOK},
	}
	for _, item := range items {
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("create item: %v", err)
		}
	}

	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})
	filter := newScopeFilter(Scope{Targets: []Target{
		{Library: library, Roots: []string{filepath.Join(root, "Parliament")}},
	}})

	// "rel-unowned" stands for a release in the cache that no file points at — cached
	// by browsing, say. It belongs to no scope, so a narrowed run leaves it alone.
	got := r.narrowDue([]string{"rel-parliament", "rel-jayz", "rel-unowned"}, filter)
	sort.Strings(got)
	if len(got) != 1 || got[0] != "rel-parliament" {
		t.Errorf("narrowDue = %v, want [rel-parliament]", got)
	}

	// The unnarrowed case is left exactly as it was: a full scan still refreshes
	// everything due, or nothing would ever pick these up.
	all := r.narrowDue([]string{"rel-parliament", "rel-jayz"}, scopeFilter{})
	if len(all) != 2 {
		t.Errorf("an unnarrowed run must keep the whole due list, got %v", all)
	}
}

// TestRetagReleasesHonoursScope is the write half, and the bug as reported: scanning one
// artist rewrote files belonging to another, because the drift stage re-tagged every
// indexed file of a changed release regardless of what the run covered. Both files here
// point at the same changed release and are byte-identical, so the only thing that can
// separate them is the scope.
func TestRetagReleasesHonoursScope(t *testing.T) {
	requireAudioTools(t)

	root := t.TempDir()
	inScope := synthFlacAt(t, filepath.Join(root, "Parliament", "Mothership Connection (1975)", "01 track.flac"))
	outOfScope := synthFlacAt(t, filepath.Join(root, "Jay-Z & Chic", "Album (1998)", "01 track.flac"))

	db := newTestDB(t)
	seedReleaseCache(t, db, models.MusicBrainzReleaseResponse{
		ID: "rel-1", Title: "Album",
		ArtistCredit: []models.ArtistCredit{{Name: "Band", Artist: models.Artist{ID: "art-1", Name: "Band"}}},
		ReleaseGroup: models.ReleaseGroup{ID: "rg-1", Title: "Album", PrimaryType: "Album"},
		Media: []models.MusicBrainzMedia{{
			Position: 1,
			Tracks:   []models.Track{{ID: "trk-1", Title: "Song", Position: 1, Number: "1"}},
		}},
	})

	profile := models.TaggerProfile{Name: "Write", WriteTags: true}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("create profile: %v", err)
	}
	library := models.Library{Name: "L", Path: root, Enabled: true, TaggerProfileID: &profile.ID}
	if err := db.Create(&library).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}

	created := map[string]models.LibraryItem{}
	for _, path := range []string{inScope, outOfScope} {
		item := models.LibraryItem{
			LibraryID: library.ID, Path: path,
			MBReleaseID: "rel-1", MBReleaseTrackID: "trk-1", MBRecordingID: "rec-1",
			Status: models.LibraryItemStatusOK,
		}
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("create item %q: %v", path, err)
		}
		created[path] = item
	}

	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})
	filter := newScopeFilter(Scope{Targets: []Target{
		{Library: library, Roots: []string{filepath.Join(root, "Parliament")}},
	}})
	res := r.retagReleases([]string{"rel-1"}, modules.NewAlbumRefreshSet(nil),
		components.NewDetailCollector(models.DefaultEventDetailRetention), filter)

	if res.retagged != 1 {
		t.Errorf("re-tagged %d file(s), want 1 — only the file inside the scanned folder", res.retagged)
	}
	if len(res.errorFiles) != 0 {
		t.Errorf("unexpected errors: %v", res.errorFiles)
	}

	// The release changed upstream whatever the scope was, so it still gets its row —
	// carrying the count of files this run actually rewrote.
	if len(res.refreshItems) != 1 || res.refreshItems[0].TagsWritten != 1 {
		t.Errorf("refreshItems = %#v, want one row for rel-1 with 1 file re-tagged", res.refreshItems)
	}

	// retagItem stamps the row it touched, so the untouched file is provable rather
	// than inferred from a count.
	reload := func(path string) models.LibraryItem {
		var item models.LibraryItem
		if err := db.First(&item, "id = ?", created[path].ID).Error; err != nil {
			t.Fatalf("reload %q: %v", path, err)
		}
		return item
	}
	if got := reload(inScope).ProcessedVersion; got != "test" {
		t.Errorf("in-scope item processed version = %q, want test", got)
	}
	if got := reload(outOfScope).ProcessedVersion; got != "" {
		t.Errorf("the out-of-scope file was processed (version %q); it must not be touched at all", got)
	}
}

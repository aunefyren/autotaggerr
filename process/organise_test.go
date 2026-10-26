package process

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
)

// organisedWorld is the import world with its album imported into the library, then the
// profile's album folder changed so the files are no longer where it names them.
func organisedWorld(t *testing.T) importWorld {
	t.Helper()
	w := newImportWorld(t)
	w.run(t, modules.TransferCopy, w.runner.ProposeImport(w.library, w.release, w.sources()))
	if err := w.db.Model(&models.TaggerProfile{}).Where("id = ?", *w.library.TaggerProfileID).
		Update("naming_album_folder", "{Album Title}").Error; err != nil {
		t.Fatal(err)
	}
	return w
}

// TestOrganiseMovesWhatWasReviewed: the plan lists each file's move without making it;
// the job makes the reviewed moves, carries the index and the path-keyed rows across,
// and removes the folder it emptied.
func TestOrganiseMovesWhatWasReviewed(t *testing.T) {
	w := organisedWorld(t)
	scope := OrganiseScope{LibraryID: w.library.ID}

	plan, err := w.runner.PlanOrganise(scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Moves) != 2 || plan.InPlace != 0 {
		t.Fatalf("plan = %+v, want two moves", plan)
	}
	m := plan.Moves[0]
	if m.FromRel != "Band/Album (2001)/01 Song One.flac" || m.ToRel != "Band/Album/01 Song One.flac" || m.Conflict != "" {
		t.Fatalf("move = %+v", m)
	}
	if _, err := os.Stat(m.From); err != nil {
		t.Fatal("the plan moved a file")
	}

	// A tempo analysis keyed by the old path follows the file.
	if err := w.db.Create(&models.TrackTempo{Path: m.From, BPM: 120}).Error; err != nil {
		t.Fatal(err)
	}
	// The second move is skipped in review and stays where it is.
	plan.Moves[1].Skip = true
	if err := w.runner.Organise(scope, plan.Moves, true); err != nil {
		t.Fatal(err)
	}
	w.runner.waitIdle(t)

	if _, err := os.Stat(m.To); err != nil {
		t.Errorf("not moved to %s: %v", m.To, err)
	}
	var item models.LibraryItem
	if err := w.db.Where("path = ?", m.To).First(&item).Error; err != nil || item.MBReleaseID == "" {
		t.Errorf("index row at the new path = %+v, %v", item, err)
	}
	var tempo models.TrackTempo
	if err := w.db.Where("path = ?", m.To).First(&tempo).Error; err != nil || tempo.BPM != 120 {
		t.Errorf("tempo at the new path = %+v, %v", tempo, err)
	}
	if _, err := os.Stat(plan.Moves[1].From); err != nil {
		t.Error("a skipped move was made")
	}

	// The rest, in a second pass: the old album folder is left empty and removed.
	again, _ := w.runner.PlanOrganise(scope)
	if len(again.Moves) != 1 || again.InPlace != 1 {
		t.Fatalf("second plan = %+v", again)
	}
	if err := w.runner.Organise(scope, again.Moves, true); err != nil {
		t.Fatal(err)
	}
	w.runner.waitIdle(t)
	if _, err := os.Stat(filepath.Dir(m.From)); !os.IsNotExist(err) {
		t.Errorf("the emptied folder %s is still there: %v", filepath.Dir(m.From), err)
	}
	var events []models.Event
	w.db.Where("type = ?", models.EventTypeOrganise).Find(&events)
	if len(events) != 2 || events[0].Status != models.EventStatusOK {
		t.Errorf("organise events = %+v", events)
	}
}

// TestOrganiseConflictsBlock: two files rendering one path, or a target something else
// already occupies, block the commit; a swap within the plan does not.
func TestOrganiseConflictsBlock(t *testing.T) {
	dir := t.TempDir()
	a, b, stray := filepath.Join(dir, "a.flac"), filepath.Join(dir, "b.flac"), filepath.Join(dir, "stray.flac")
	for _, p := range []string{a, b, stray} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	clash := []OrganiseRow{{From: a, To: filepath.Join(dir, "same.flac")}, {From: b, To: filepath.Join(dir, "same.flac")}}
	if err := CheckOrganisePlan(clash); !errors.Is(err, ErrOrganiseNotReady) || !strings.Contains(err.Error(), "skip one") {
		t.Errorf("clash = %v", err)
	}
	clash[1].Skip = true
	if err := CheckOrganisePlan(clash); err != nil {
		t.Errorf("a clash with one side skipped = %v", err)
	}
	if err := CheckOrganisePlan([]OrganiseRow{{From: a, To: stray}}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("onto a stray file = %v", err)
	}
	if err := CheckOrganisePlan([]OrganiseRow{{From: a, To: b}, {From: b, To: a}}); err != nil {
		t.Errorf("a swap = %v", err)
	}
	if err := CheckOrganisePlan([]OrganiseRow{{From: a, To: b, Skip: true}}); err == nil {
		t.Error("a plan with every move skipped passed")
	}
}

// TestOrganiseSwapsWithinThePlan: two files trading places land where they should —
// the job's first phase frees every source before anything is put into place.
func TestOrganiseSwapsWithinThePlan(t *testing.T) {
	w := organisedWorld(t)
	var items []models.LibraryItem
	w.db.Order("path").Find(&items)
	if len(items) != 2 {
		t.Fatalf("items = %d", len(items))
	}
	one, two := items[0].Path, items[1].Path
	body := func(p string) string { b, _ := os.ReadFile(p); return string(b) }
	before := map[string]string{one: body(one), two: body(two)}
	moved, rows, _ := w.runner.organiseMoves([]OrganiseRow{
		{ItemID: items[0].ID, LibraryID: w.library.ID, From: one, To: two},
		{ItemID: items[1].ID, LibraryID: w.library.ID, From: two, To: one},
	})
	if moved != 2 || body(two) != before[one] || body(one) != before[two] {
		t.Errorf("swap: moved %d, rows %+v — the two files were not exchanged", moved, rows)
	}
	var first models.LibraryItem
	w.db.First(&first, "id = ?", items[0].ID)
	if first.Path != two {
		t.Errorf("the first file's index row is at %s, want %s", first.Path, two)
	}
}

// A library Lidarr names the files of is never organised.
func TestOrganiseRefusesALidarrLibrary(t *testing.T) {
	w := newImportWorld(t)
	lidarr := models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true}
	if err := w.db.Create(&lidarr).Error; err != nil {
		t.Fatal(err)
	}
	w.db.Model(&w.library).Update("manager_id", lidarr.ID)
	if _, err := w.runner.PlanOrganise(OrganiseScope{LibraryID: w.library.ID}); !errors.Is(err, ErrOrganiseManaged) {
		t.Errorf("plan = %v, want ErrOrganiseManaged", err)
	}
	if _, err := w.runner.PlanOrganise(OrganiseScope{}); err == nil {
		t.Error("an empty scope planned")
	}
}

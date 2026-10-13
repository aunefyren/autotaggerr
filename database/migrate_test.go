package database

import (
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TestPinFallbackManagerRunsOnce: an upgraded install keeps the manager its
// unassigned libraries were silently using, and the migration never runs again — a
// library set to "no manager" afterwards is a choice it must not undo.
func TestPinFallbackManagerRunsOnce(t *testing.T) {
	db := testDB(t)
	off := models.Manager{Name: "Off", Type: models.ManagerTypeLidarr, Enabled: false}
	on := models.Manager{Name: "On", Type: models.ManagerTypeLidarr, Enabled: true}
	for _, m := range []*models.Manager{&off, &on} {
		if err := db.Create(m).Error; err != nil {
			t.Fatalf("manager: %v", err)
		}
	}
	unassigned := models.Library{Name: "Default", Path: "/a", Enabled: true}
	assigned := models.Library{Name: "Own", Path: "/b", Enabled: true, ManagerID: &off.ID}
	for _, l := range []*models.Library{&unassigned, &assigned} {
		if err := db.Create(l).Error; err != nil {
			t.Fatalf("library: %v", err)
		}
	}

	if err := ApplyDataMigrations(db); err != nil {
		t.Fatalf("ApplyDataMigrations: %v", err)
	}
	if got := managerOf(t, db, unassigned.ID); got == nil || *got != on.ID {
		t.Fatalf("unassigned library got %v, want the enabled manager %s", got, on.ID)
	}
	if got := managerOf(t, db, assigned.ID); got == nil || *got != off.ID {
		t.Errorf("an assigned library was changed: %v", got)
	}

	// The user now chooses no manager. A second start must leave it alone.
	if err := db.Model(&models.Library{}).Where("id = ?", unassigned.ID).Update("manager_id", nil).Error; err != nil {
		t.Fatal(err)
	}
	if err := ApplyDataMigrations(db); err != nil {
		t.Fatalf("second ApplyDataMigrations: %v", err)
	}
	if got := managerOf(t, db, unassigned.ID); got != nil {
		t.Errorf("the migration ran again and re-assigned %v", got)
	}
}

// TestPinFallbackManagerWithoutManagers: with no enabled manager nothing was ever
// falling back, so nothing is assigned — and the migration is still recorded, so a
// manager created later is never pinned onto libraries left without one on purpose.
func TestPinFallbackManagerWithoutManagers(t *testing.T) {
	db := testDB(t)
	lib := models.Library{Name: "L", Path: "/a", Enabled: true}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatal(err)
	}
	if err := ApplyDataMigrations(db); err != nil {
		t.Fatalf("ApplyDataMigrations: %v", err)
	}
	mgr := models.Manager{Name: "Later", Type: models.ManagerTypeLidarr, Enabled: true}
	if err := db.Create(&mgr).Error; err != nil {
		t.Fatal(err)
	}
	if err := ApplyDataMigrations(db); err != nil {
		t.Fatalf("second ApplyDataMigrations: %v", err)
	}
	if got := managerOf(t, db, lib.ID); got != nil {
		t.Errorf("library was assigned %v", got)
	}
}

func managerOf(t *testing.T, db *gorm.DB, id uuid.UUID) *uuid.UUID {
	t.Helper()
	var l models.Library
	if err := db.First(&l, "id = ?", id).Error; err != nil {
		t.Fatalf("read library: %v", err)
	}
	return l.ManagerID
}

// TestMoveBPMToLibrary: an upgraded install keeps its BPM behaviour. Each library gets
// the settings of the profile that tags it — the first profile for a library with
// none assigned, since that is the one resolveTaggerProfile hands it.
func TestMoveBPMToLibrary(t *testing.T) {
	db := testDB(t)
	// The columns as they were before the move.
	for _, stmt := range []string{
		"ALTER TABLE tagger_profiles ADD COLUMN bpm_write text",
		"ALTER TABLE tagger_profiles ADD COLUMN bpm_min_confidence real",
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatal(err)
		}
	}
	first := models.TaggerProfile{Name: "First", WriteTags: true}
	second := models.TaggerProfile{Name: "Second", WriteTags: true}
	for _, p := range []*models.TaggerProfile{&first, &second} {
		if err := db.Create(p).Error; err != nil {
			t.Fatal(err)
		}
	}
	db.Exec("UPDATE tagger_profiles SET bpm_write = ?, bpm_min_confidence = ? WHERE id = ?", models.BPMWriteFillEmpty, 0.6, first.ID)
	db.Exec("UPDATE tagger_profiles SET bpm_write = ?, bpm_min_confidence = ? WHERE id = ?", models.BPMWriteOverwrite, 0.8, second.ID)
	// Which one is "first" is decided by id order, the rule the resolver uses.
	if second.ID.String() < first.ID.String() {
		first, second = second, first
	}

	none := models.Library{Name: "None", Path: "/a"}
	onSecond := models.Library{Name: "Second", Path: "/b", TaggerProfileID: &second.ID}
	for _, l := range []*models.Library{&none, &onSecond} {
		if err := db.Create(l).Error; err != nil {
			t.Fatal(err)
		}
	}

	if err := ApplyDataMigrations(db); err != nil {
		t.Fatalf("ApplyDataMigrations: %v", err)
	}
	read := func(id uuid.UUID) models.Library {
		var l models.Library
		db.First(&l, "id = ?", id)
		return l
	}
	var want struct {
		mode string
		conf float64
	}
	db.Raw("SELECT bpm_write, bpm_min_confidence FROM tagger_profiles WHERE id = ?", first.ID).Row().Scan(&want.mode, &want.conf)
	if got := read(none.ID); got.BPMWrite != want.mode || got.BPMMinConfidence != want.conf {
		t.Errorf("library without a profile = %q/%v, want the first profile's %q/%v", got.BPMWrite, got.BPMMinConfidence, want.mode, want.conf)
	}
	db.Raw("SELECT bpm_write, bpm_min_confidence FROM tagger_profiles WHERE id = ?", second.ID).Row().Scan(&want.mode, &want.conf)
	if got := read(onSecond.ID); got.BPMWrite != want.mode || got.BPMMinConfidence != want.conf {
		t.Errorf("library on the second profile = %q/%v, want %q/%v", got.BPMWrite, got.BPMMinConfidence, want.mode, want.conf)
	}
}

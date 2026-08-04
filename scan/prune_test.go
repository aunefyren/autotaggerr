package scan

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
)

// seedItem indexes one file path under a library, optionally pinned.
func seedItem(t *testing.T, db *gorm.DB, lib models.Library, path string, pinned bool) models.LibraryItem {
	t.Helper()
	item := models.LibraryItem{
		LibraryID:   lib.ID,
		Path:        path,
		Status:      models.LibraryItemStatusOK,
		MBReleaseID: "rel-1",
		Pinned:      pinned,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("seed item %q: %v", path, err)
	}
	return item
}

// indexedPaths is what the index still holds for a library, for asserting on.
func indexedPaths(t *testing.T, db *gorm.DB, lib models.Library) map[string]bool {
	t.Helper()
	var items []models.LibraryItem
	if err := db.Where("library_id = ?", lib.ID).Find(&items).Error; err != nil {
		t.Fatalf("read items: %v", err)
	}
	out := map[string]bool{}
	for _, item := range items {
		out[item.Path] = true
	}
	return out
}

// TestPruneMissingItemsRemovesGhosts is the bug this exists for: a manager moved the
// files, and because library_items is path-keyed the old rows survived with status ok
// and kept owning their release. The surviving file's row must be untouched.
func TestPruneMissingItemsRemovesGhosts(t *testing.T) {
	db := newTestDB(t)
	root := t.TempDir()

	present := filepath.Join(root, "Ben Folds", "Album (2006)", "01 track.flac")
	if err := os.MkdirAll(filepath.Dir(present), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(present, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	ghost := filepath.Join(root, "Various Artists", "Album (2006)", "01 track.flac")

	lib := models.Library{Name: "L", Path: root}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	seedItem(t, db, lib, present, false)
	seedItem(t, db, lib, ghost, false)

	removed, err := pruneMissingItems(db, lib, nil)
	if err != nil {
		t.Fatalf("pruneMissingItems: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}

	paths := indexedPaths(t, db, lib)
	if !paths[present] {
		t.Error("the file that still exists must keep its index row")
	}
	if paths[ghost] {
		t.Error("the row for the moved-away file should be gone")
	}
}

// TestPruneMissingItemsUnavailableRoot: an unmounted library stats as "every file is
// gone". Pruning on that would empty the index for a library whose files are all
// still there, so a root that cannot be stat'd refuses the whole pass.
func TestPruneMissingItemsUnavailableRoot(t *testing.T) {
	db := newTestDB(t)
	root := filepath.Join(t.TempDir(), "not-mounted")

	lib := models.Library{Name: "L", Path: root}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	seedItem(t, db, lib, filepath.Join(root, "Artist", "Album", "01.flac"), false)

	removed, err := pruneMissingItems(db, lib, nil)
	if err == nil {
		t.Fatal("an unavailable root must be reported as an error, not treated as an empty library")
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0", removed)
	}
	if len(indexedPaths(t, db, lib)) != 1 {
		t.Error("no row may be deleted when the root is unavailable")
	}
}

// TestPruneMissingItemsScoped: a narrowed run (one artist, one release-group) visits a
// subset of the library and must conclude nothing about the rest of it. A missing file
// outside the walked roots keeps its row.
func TestPruneMissingItemsScoped(t *testing.T) {
	db := newTestDB(t)
	root := t.TempDir()

	scoped := filepath.Join(root, "Artist A")
	if err := os.MkdirAll(scoped, 0o755); err != nil {
		t.Fatal(err)
	}
	inScope := filepath.Join(scoped, "Album", "01.flac")
	outOfScope := filepath.Join(root, "Artist B", "Album", "01.flac")

	lib := models.Library{Name: "L", Path: root}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	seedItem(t, db, lib, inScope, false)
	seedItem(t, db, lib, outOfScope, false)

	removed, err := pruneMissingItems(db, lib, []string{scoped})
	if err != nil {
		t.Fatalf("pruneMissingItems: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}

	paths := indexedPaths(t, db, lib)
	if paths[inScope] {
		t.Error("the missing file inside the scope should have been pruned")
	}
	if !paths[outOfScope] {
		t.Error("a scoped run must not prune rows outside the roots it walked")
	}
}

// TestPruneMissingItemsOtherLibrary: the pass is scoped to one library, so another
// library's rows are never candidates even when their paths do not exist.
func TestPruneMissingItemsOtherLibrary(t *testing.T) {
	db := newTestDB(t)
	root := t.TempDir()

	lib := models.Library{Name: "L", Path: root}
	other := models.Library{Name: "Other", Path: filepath.Join(root, "other")}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("other library: %v", err)
	}
	seedItem(t, db, other, filepath.Join(root, "gone.flac"), false)

	removed, err := pruneMissingItems(db, lib, nil)
	if err != nil {
		t.Fatalf("pruneMissingItems: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0", removed)
	}
	if len(indexedPaths(t, db, other)) != 1 {
		t.Error("another library's rows must be left alone")
	}
}

// TestPruneMissingItemsDropsPins: a pin identifies a file, so a pin whose file is gone
// identifies nothing and goes with it. Asserted explicitly because this is the one
// place the pass destroys authored state.
func TestPruneMissingItemsDropsPins(t *testing.T) {
	db := newTestDB(t)
	root := t.TempDir()

	lib := models.Library{Name: "L", Path: root}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	seedItem(t, db, lib, filepath.Join(root, "pinned.flac"), true)

	removed, err := pruneMissingItems(db, lib, nil)
	if err != nil {
		t.Fatalf("pruneMissingItems: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if len(indexedPaths(t, db, lib)) != 0 {
		t.Error("a pinned row whose file is gone should be pruned too")
	}
}

// TestPruneMissingItemsBatching drives more rows than one DELETE carries, so the
// chunking cannot silently drop the tail.
func TestPruneMissingItemsBatching(t *testing.T) {
	db := newTestDB(t)
	root := t.TempDir()

	lib := models.Library{Name: "L", Path: root}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	const count = pruneDeleteBatch*2 + 7
	for i := range count {
		seedItem(t, db, lib, filepath.Join(root, "gone", string(rune('a'+i%26))+string(rune('a'+i/26))+".flac"), false)
	}

	removed, err := pruneMissingItems(db, lib, nil)
	if err != nil {
		t.Fatalf("pruneMissingItems: %v", err)
	}
	if removed != count {
		t.Fatalf("removed = %d, want %d", removed, count)
	}
	if n := len(indexedPaths(t, db, lib)); n != 0 {
		t.Errorf("%d row(s) survived the batched delete", n)
	}
}

package process

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/models"
)

// The production incident, end to end: a manager renamed an album folder after a tag
// write. A plain Scan proves the old path gone and deletes the row, emptying an album
// that is intact on disk. A Scan with the disk walk finds the file first, carries its
// row to the new path, and the scan that follows has nothing to delete.
func TestDiscoverArtistKeepsAMovedAlbum(t *testing.T) {
	db := newTestDB(t)
	root := t.TempDir()
	library, oldPath := seedArtistWithFile(t, db, root)

	// The row as a Process left it: the size and mtime the file had.
	fi, err := os.Stat(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	mtime := fi.ModTime().Truncate(time.Second)
	if err := os.Chtimes(oldPath, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.LibraryItem{}).Where("path = ?", oldPath).
		Updates(map[string]any{"size": fi.Size(), "mod_time": mtime}).Error; err != nil {
		t.Fatal(err)
	}

	// The manager's rename. os.Rename keeps size and mtime, as a same-filesystem move does.
	newPath := filepath.Join(root, "Artist", "Album (2021)", filepath.Base(oldPath))
	if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatal(err)
	}

	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrProcessConcurrency: 2, AutotaggerrVersion: "test"})
	if err := r.DiscoverArtist("artist-1"); err != nil {
		t.Fatalf("DiscoverArtist: %v", err)
	}
	r.waitIdle(t)

	paths := indexedPaths(t, db, library)
	if !paths[newPath] || paths[oldPath] {
		t.Fatalf("index = %v, want the row carried to %q", paths, newPath)
	}
	var item models.LibraryItem
	if err := db.Where("path = ?", newPath).First(&item).Error; err != nil {
		t.Fatal(err)
	}
	if item.MBReleaseID != "rel-1" {
		t.Errorf("carried row release = %q, want rel-1", item.MBReleaseID)
	}

	var ev models.Event
	if err := db.Where("type = ?", models.EventTypeDiscoverFiles).First(&ev).Error; err != nil {
		t.Fatalf("discovery event not recorded: %v", err)
	}
	if ev.Title != "Scan with disk walk for Artist" {
		t.Errorf("title = %q", ev.Title)
	}
	var scan models.Event
	if err := db.Where("type = ? AND parent_id = ?", models.EventTypeCollectionScan, ev.ID).First(&scan).Error; err != nil {
		t.Fatalf("the collection scan should run as a stage of the discovery: %v", err)
	}
	if removed, _ := ev.Details["files_removed"].(float64); removed != 0 {
		t.Errorf("files_removed = %v, want 0 — the moved file was found before the prune", ev.Details["files_removed"])
	}
}

// Without the walk the same rename loses the row. Pinned here so the difference the
// option makes is a test, not a claim in a comment.
func TestPlainScanLosesAMovedFile(t *testing.T) {
	db := newTestDB(t)
	root := t.TempDir()
	library, oldPath := seedArtistWithFile(t, db, root)
	newPath := filepath.Join(root, "Artist", "Album (2021)", filepath.Base(oldPath))
	if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatal(err)
	}

	if _, err := collection.RecordScan(db, "Collection scan", collection.RebuildScope{ArtistMBID: "artist-1"}, nil); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if paths := indexedPaths(t, db, library); len(paths) != 0 {
		t.Errorf("index = %v, want the moved file's row deleted and nothing found", paths)
	}
}

func TestDiscoverRefusesAnEmptyScope(t *testing.T) {
	db := newTestDB(t)
	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})
	if err := r.DiscoverAll(); !errors.Is(err, ErrNothingToProcess) {
		t.Errorf("DiscoverAll with no libraries = %v, want ErrNothingToProcess", err)
	}
	if err := db.Create(&models.CollectionArtist{MBID: "artist-1", Name: "Artist"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DiscoverArtist("artist-1"); !errors.Is(err, ErrNothingToProcess) {
		t.Errorf("DiscoverArtist with no files = %v, want ErrNothingToProcess", err)
	}
	if err := r.DiscoverArtist("nope"); err == nil {
		t.Error("DiscoverArtist of an unknown artist should fail")
	}
}

func TestDiscoverAllWalksEveryEnabledLibrary(t *testing.T) {
	db := newTestDB(t)
	root := t.TempDir()
	writeInvalidFlac(t, filepath.Join(root, "Artist", "Album (2020)"))
	library := models.Library{Name: "L", Path: root, Enabled: true}
	if err := db.Create(&library).Error; err != nil {
		t.Fatal(err)
	}

	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrProcessConcurrency: 2, AutotaggerrVersion: "test"})
	if err := r.DiscoverAll(); err != nil {
		t.Fatalf("DiscoverAll: %v", err)
	}
	r.waitIdle(t)

	var ev models.Event
	if err := db.Where("type = ?", models.EventTypeDiscoverFiles).First(&ev).Error; err != nil {
		t.Fatalf("discovery event not recorded: %v", err)
	}
	// The invalid FLAC cannot be correlated from its tags: one walked, one failed, and
	// the failure is on the index row rather than lost.
	if walked, _ := ev.Details["walked"].(float64); walked != 1 {
		t.Errorf("walked = %v, want 1", ev.Details["walked"])
	}
	if ev.Status != models.EventStatusError {
		t.Errorf("status = %q, want error for the failed lookup", ev.Status)
	}
	if paths := indexedPaths(t, db, library); len(paths) != 1 {
		t.Errorf("index = %v, want the walked file recorded", paths)
	}
}

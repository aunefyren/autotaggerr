package process

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/database"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// twoLibraries is one artist with an album in a Lidarr-shaped library and a native
// one — the case the artist page's library tabs exist for.
func twoLibraries(t *testing.T) (*gorm.DB, models.Library, models.Library) {
	t.Helper()
	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatal(err)
	}
	music := models.Library{Name: "Music", Path: "/music", Enabled: true}
	sandbox := models.Library{Name: "Sandbox", Path: "/sandbox", Enabled: true}
	for _, row := range []any{&music, &sandbox,
		&models.CollectionArtist{MBID: "art-1", Name: "Band"},
		&models.CollectionReleaseGroupArtist{ReleaseGroupMBID: "rg-1", ArtistMBID: "art-1"},
		&models.CollectionRelease{MBID: "rel-1", ReleaseGroupMBID: "rg-1", ArtistMBID: "art-1"},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, it := range []models.LibraryItem{
		{LibraryID: music.ID, Path: "/music/Band/Album/01.flac", MBReleaseID: "rel-1", Status: models.LibraryItemStatusOK},
		{LibraryID: sandbox.ID, Path: "/sandbox/Band/Album/01.flac", MBReleaseID: "rel-1", Status: models.LibraryItemStatusOK},
	} {
		if err := db.Create(&it).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db, music, sandbox
}

// TestArtistScopeInNarrowsToTheLibrary: every library by default, one library when
// asked, and a library holding nothing of the artist is an answer, not an empty run.
func TestArtistScopeInNarrowsToTheLibrary(t *testing.T) {
	db, music, sandbox := twoLibraries(t)
	r := NewRunner(db, models.ConfigStruct{AutotaggerrVersion: "test"})

	all, err := r.ArtistScopeIn("art-1", nil)
	if err != nil || len(all.Targets) != 2 {
		t.Fatalf("every library = %+v, %v", all.Targets, err)
	}
	only, err := r.ArtistScopeIn("art-1", &sandbox.ID)
	if err != nil || len(only.Targets) != 1 || only.Targets[0].Library.ID != sandbox.ID {
		t.Fatalf("sandbox = %+v, %v", only.Targets, err)
	}
	if only.Title != "Processing Band in Sandbox" {
		t.Errorf("title = %q, want the library named", only.Title)
	}
	// The library is in the job key: a run of one library does not collapse into a
	// run of every library, or of the other one.
	if scopeKey("process_artist:", all) == scopeKey("process_artist:", only) {
		t.Error("an artist-wide and a library-scoped run share a job key")
	}
	other, _ := r.ArtistScopeIn("art-1", &music.ID)
	if scopeKey("process_artist:", other) == scopeKey("process_artist:", only) {
		t.Error("two libraries' runs share a job key")
	}

	empty := uuid.New()
	if _, err := r.ArtistScopeIn("art-1", &empty); !errors.Is(err, ErrNothingInLibrary) {
		t.Errorf("a library without the artist = %v, want ErrNothingInLibrary", err)
	}
}

// TestOrganiseScopeNarrowsToTheLibrary: an artist scope with a library plans only that
// library's files; an artist and an album together is not a scope.
func TestOrganiseScopeNarrowsToTheLibrary(t *testing.T) {
	db, _, sandbox := twoLibraries(t)
	r := NewRunner(db, models.ConfigStruct{AutotaggerrVersion: "test"})

	items, err := r.organiseItems(OrganiseScope{ArtistMBID: "art-1", LibraryID: sandbox.ID})
	if err != nil || len(items) != 1 || items[0].LibraryID != sandbox.ID {
		t.Fatalf("organise items = %+v, %v", items, err)
	}
	if all, _ := r.organiseItems(OrganiseScope{ArtistMBID: "art-1"}); len(all) != 2 {
		t.Errorf("every library = %d items, want 2", len(all))
	}
	if (OrganiseScope{ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1"}).valid() {
		t.Error("an artist and an album together was a valid scope")
	}
	if !(OrganiseScope{ReleaseGroupMBID: "rg-1", LibraryID: sandbox.ID}).valid() {
		t.Error("an album in a library was not a valid scope")
	}
}

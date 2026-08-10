package collection

import (
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Locating an artist on disk is derived, not stored, so what these tests pin is the
// derivation: that it follows the credit links (collaborations included), that it
// collapses many files into the few folders a scan should walk, and that it never
// invents a folder for an artist whose files are not there.

// artistOnDisk seeds one artist with one release-group, one owned edition, and an
// item per given file path.
func artistOnDisk(t *testing.T, db *gorm.DB, libraryPath string, paths ...string) (models.Library, string) {
	t.Helper()

	library := models.Library{Name: "L", Path: libraryPath, Enabled: true}
	if err := db.Create(&library).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	artistMBID := uuid.NewString()
	if err := db.Create(&models.CollectionArtist{MBID: artistMBID, Name: "Artist"}).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	rgMBID, releaseMBID := uuid.NewString(), uuid.NewString()
	if err := db.Create(&models.CollectionReleaseGroupArtist{ReleaseGroupMBID: rgMBID, ArtistMBID: artistMBID}).Error; err != nil {
		t.Fatalf("link release-group: %v", err)
	}
	if err := db.Create(&models.CollectionRelease{MBID: releaseMBID, ReleaseGroupMBID: rgMBID, ArtistMBID: artistMBID}).Error; err != nil {
		t.Fatalf("create release: %v", err)
	}
	for _, p := range paths {
		item := models.LibraryItem{
			LibraryID:   library.ID,
			Path:        p,
			MBReleaseID: releaseMBID,
			Status:      models.LibraryItemStatusOK,
		}
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("create item: %v", err)
		}
	}
	return library, artistMBID
}

// A whole discography lives under one artist folder, and that folder — not fifty
// album folders — is what a scan of the artist should walk.
func TestArtistTargetsCollapsesToArtistFolder(t *testing.T) {
	db := testDB(t)
	root := filepath.Join("/music")
	_, artistMBID := artistOnDisk(t, db, root,
		filepath.Join(root, "Artist", "Album (2020)", "01 a.flac"),
		filepath.Join(root, "Artist", "Album (2020)", "02 b.flac"),
		filepath.Join(root, "Artist", "Other (2022)", "01 c.flac"),
	)

	targets, err := ArtistTargets(db, artistMBID)
	if err != nil {
		t.Fatalf("ArtistTargets: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %d (%+v), want 1", len(targets), targets)
	}
	if want := filepath.Join(root, "Artist"); targets[0].Path != want {
		t.Errorf("path = %q, want %q", targets[0].Path, want)
	}
	if targets[0].Library.Path != root {
		t.Errorf("target lost its library: %+v", targets[0].Library)
	}
}

// The folder is read from the files, not from the artist's name: a library that
// spells the folder differently from MusicBrainz still resolves.
func TestArtistTargetsUsesFolderNameNotArtistName(t *testing.T) {
	db := testDB(t)
	root := "/music"
	_, artistMBID := artistOnDisk(t, db, root, filepath.Join(root, "Beatles, The", "Revolver (1966)", "01 a.flac"))

	targets, err := ArtistTargets(db, artistMBID)
	if err != nil {
		t.Fatalf("ArtistTargets: %v", err)
	}
	if len(targets) != 1 || targets[0].Path != filepath.Join(root, "Beatles, The") {
		t.Fatalf("targets = %+v, want the on-disk folder name", targets)
	}
}

// An artist with nothing on disk yields no target at all. The caller turns that into
// a refusal — scanning a guessed folder would either walk the wrong thing or report
// a silent zero.
func TestArtistTargetsEmptyWithoutFiles(t *testing.T) {
	db := testDB(t)
	artistMBID := uuid.NewString()
	if err := db.Create(&models.CollectionArtist{MBID: artistMBID, Name: "Nobody"}).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}

	targets, err := ArtistTargets(db, artistMBID)
	if err != nil {
		t.Fatalf("ArtistTargets: %v", err)
	}
	if len(targets) != 0 {
		t.Errorf("targets = %+v, want none", targets)
	}
}

// ReleaseGroupTargets narrows to each file's own directory (album / per-disc folder),
// the opposite of ArtistTargets collapsing to the artist folder — so re-correlating one
// album does not re-walk the whole discography.
func TestReleaseGroupTargetsNarrowsToAlbumFolders(t *testing.T) {
	db := testDB(t)
	root := "/music"
	library := models.Library{Name: "L", Path: root, Enabled: true}
	if err := db.Create(&library).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	rgMBID, relMBID := uuid.NewString(), uuid.NewString()
	if err := db.Create(&models.CollectionRelease{MBID: relMBID, ReleaseGroupMBID: rgMBID, ArtistMBID: "art"}).Error; err != nil {
		t.Fatalf("create release: %v", err)
	}
	album := filepath.Join(root, "Artist", "Album (2020)")
	disc2 := filepath.Join(album, "CD2")
	for _, p := range []string{
		filepath.Join(album, "01 a.flac"),
		filepath.Join(album, "02 b.flac"),
		filepath.Join(disc2, "01 c.flac"),
	} {
		if err := db.Create(&models.LibraryItem{LibraryID: library.ID, Path: p, MBReleaseID: relMBID, Status: models.LibraryItemStatusOK}).Error; err != nil {
			t.Fatalf("create item: %v", err)
		}
	}

	targets, err := ReleaseGroupTargets(db, rgMBID)
	if err != nil {
		t.Fatalf("ReleaseGroupTargets: %v", err)
	}
	got := map[string]bool{}
	for _, tg := range targets {
		got[tg.Path] = true
	}
	if !got[album] || !got[disc2] {
		t.Errorf("targets = %+v, want album %q and disc %q", targets, album, disc2)
	}
	if got[filepath.Join(root, "Artist")] {
		t.Error("release-group targets must not widen to the artist folder")
	}
	if len(targets) != 2 {
		t.Errorf("targets = %d, want 2 (album + disc)", len(targets))
	}
}

func TestReleaseGroupTargetsEmptyWithoutFiles(t *testing.T) {
	db := testDB(t)
	rgMBID := uuid.NewString()
	if err := db.Create(&models.CollectionRelease{MBID: uuid.NewString(), ReleaseGroupMBID: rgMBID, ArtistMBID: "art"}).Error; err != nil {
		t.Fatalf("create release: %v", err)
	}
	targets, err := ReleaseGroupTargets(db, rgMBID)
	if err != nil {
		t.Fatalf("ReleaseGroupTargets: %v", err)
	}
	if len(targets) != 0 {
		t.Errorf("targets = %+v, want none", targets)
	}
}

// A collaboration belongs to both artists. The second one is credited only through
// the link table, which is exactly why the resolution goes through it.
func TestArtistItemsFollowsCreditLinks(t *testing.T) {
	db := testDB(t)
	root := "/music"
	_, primary := artistOnDisk(t, db, root, filepath.Join(root, "Artist", "Split (2020)", "01 a.flac"))

	var link models.CollectionReleaseGroupArtist
	if err := db.Where("artist_mb_id = ?", primary).First(&link).Error; err != nil {
		t.Fatalf("load link: %v", err)
	}
	guest := uuid.NewString()
	if err := db.Create(&models.CollectionReleaseGroupArtist{
		ReleaseGroupMBID: link.ReleaseGroupMBID, ArtistMBID: guest, Position: 1,
	}).Error; err != nil {
		t.Fatalf("link guest: %v", err)
	}

	items, err := ArtistItems(db, guest)
	if err != nil {
		t.Fatalf("ArtistItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("guest items = %d, want 1 — a collaborator owns the files too", len(items))
	}
}

// A file that failed its last attempt is still one of the artist's files, and is
// included — the inverse of what this asserted before.
//
// Membership followed `status = ok`, which made the failure self-perpetuating: the
// file was dropped from its own artist, so the re-tag that would have fixed it could
// not see it, and the only thing that could clear the error was a verb that refused to
// look at it. Identity is what decides membership; it survives a failure, and the
// error is a separate fact about the last attempt.
func TestArtistItemsIncludesFailedFiles(t *testing.T) {
	db := testDB(t)
	root := "/music"
	library, artistMBID := artistOnDisk(t, db, root, filepath.Join(root, "Artist", "Album (2020)", "01 a.flac"))

	var ok models.LibraryItem
	if err := db.First(&ok).Error; err != nil {
		t.Fatalf("load item: %v", err)
	}
	broken := models.LibraryItem{
		LibraryID:   library.ID,
		Path:        filepath.Join(root, "Artist", "Album (2020)", "02 broken.flac"),
		MBReleaseID: ok.MBReleaseID,
		Status:      models.LibraryItemStatusError,
	}
	if err := db.Create(&broken).Error; err != nil {
		t.Fatalf("create broken item: %v", err)
	}

	ids, err := ArtistItemIDs(db, artistMBID)
	if err != nil {
		t.Fatalf("ArtistItemIDs: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("ids = %d, want 2 — a re-tag that skips the errored file cannot fix it", len(ids))
	}

	// A file with no identity is still excluded, and without a status filter saying
	// so: there is genuinely nothing to write to it.
	unidentified := models.LibraryItem{
		LibraryID: library.ID,
		Path:      filepath.Join(root, "Artist", "Album (2020)", "03 unknown.flac"),
		Status:    models.LibraryItemStatusUnmatched,
	}
	if err := db.Create(&unidentified).Error; err != nil {
		t.Fatalf("create unidentified item: %v", err)
	}
	ids, err = ArtistItemIDs(db, artistMBID)
	if err != nil {
		t.Fatalf("ArtistItemIDs: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("ids = %d, want 2 — a file with no release ID has nothing to write", len(ids))
	}

	// The partial disown, which is where the two failure states have to be told apart:
	// this file still carries the album's release ID, and the album's edition row is
	// still alive because its other files are fine — so identity alone would let it
	// through. `unmatched` is the manager withdrawing that identity, not a failure to
	// act on it, and a write must not stamp a file with an answer that has been taken
	// back.
	disowned := models.LibraryItem{
		LibraryID:   library.ID,
		Path:        filepath.Join(root, "Artist", "Album (2020)", "04 disowned.flac"),
		MBReleaseID: ok.MBReleaseID,
		Status:      models.LibraryItemStatusUnmatched,
	}
	if err := db.Create(&disowned).Error; err != nil {
		t.Fatalf("create disowned item: %v", err)
	}
	ids, err = ArtistItemIDs(db, artistMBID)
	if err != nil {
		t.Fatalf("ArtistItemIDs: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("ids = %d, want 2 — a withdrawn identity is not fit to write from", len(ids))
	}
	// The other half of the asymmetry — that a repair verb still reaches a disowned
	// file's folder — is TestArtistTargetsSurvivesEveryFileGoingUnmatched, which seeds
	// the release cache the folder resolution reads.
}

// The catch-22 the identity rule exists to break: a repair verb that refuses to start
// on exactly the damage it repairs.
//
// One stale Lidarr trackfile cache turns every file of an album `unmatched`. The disk
// view drops them deliberately (the manager has disowned them, see ownedItemRows), the
// next rebuild prunes the owned-edition rows they were reachable through, and the
// artist scope — which resolves folders via those rows — then finds nothing and
// returns ErrNothingToProcess. Force re-correlate, whose entire job is repairing an
// artist whose files diverged from what the manager says, refused to run; the only
// remaining option was the library-wide form, which discards every manager-governed
// pin in the library.
//
// Deciding a folder is worth walking is a far weaker claim than owning a release, so
// the folder resolution admits disowned files, matched to the artist through the
// release they were last correlated to.
func TestArtistTargetsSurvivesEveryFileGoingUnmatched(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)

	root := "/music"
	library := models.Library{Name: "L", Path: root, Enabled: true}
	if err := db.Create(&library).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	artistMBID := "art-disowned"
	if err := db.Create(&models.CollectionArtist{MBID: artistMBID, Name: "Artist"}).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	seedRelease(t, db, "rel-disowned", "rg-disowned", artistMBID, "Album", 2)
	if err := modules.MusicbrainzLoadCache(); err != nil {
		t.Fatalf("load cache: %v", err)
	}

	// The state after the rebuild: the files keep the release they were correlated to,
	// and nothing else points at them — no owned edition, no credit link.
	for _, name := range []string{"01 a.flac", "02 b.flac"} {
		item := models.LibraryItem{
			LibraryID:   library.ID,
			Path:        filepath.Join(root, "Artist", "Album (2020)", name),
			MBReleaseID: "rel-disowned",
			Status:      models.LibraryItemStatusUnmatched,
		}
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("create item: %v", err)
		}
	}

	var editions int64
	db.Model(&models.CollectionRelease{}).Count(&editions)
	if editions != 0 {
		t.Fatalf("editions = %d, want 0 — the premise is that the rebuild pruned them", editions)
	}

	targets, err := ArtistTargets(db, artistMBID)
	if err != nil {
		t.Fatalf("ArtistTargets: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %d (%+v), want 1 — the repair verb cannot start", len(targets), targets)
	}
	if want := filepath.Join(root, "Artist"); targets[0].Path != want {
		t.Errorf("path = %q, want %q", targets[0].Path, want)
	}

	// Narrower scope, same rule.
	rgTargets, err := ReleaseGroupTargets(db, "rg-disowned")
	if err != nil {
		t.Fatalf("ReleaseGroupTargets: %v", err)
	}
	if len(rgTargets) != 1 {
		t.Fatalf("release-group targets = %d, want 1", len(rgTargets))
	}
	if want := filepath.Join(root, "Artist", "Album (2020)"); rgTargets[0].Path != want {
		t.Errorf("path = %q, want %q", rgTargets[0].Path, want)
	}

	// Disowned files widen the *folders* only. A re-tag writes from stored metadata,
	// and an identity the manager has withdrawn is precisely what must not be written.
	ids, err := ArtistItemIDs(db, artistMBID)
	if err != nil {
		t.Fatalf("ArtistItemIDs: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("re-tag ids = %d, want 0 — a disowned identity is not fit to write", len(ids))
	}
}

// The widening is bounded by the same identity rule: another artist's disowned files
// must not drag their folder into this artist's scope.
func TestArtistTargetsIgnoresAnotherArtistsDisownedFiles(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)

	root := "/music"
	library := models.Library{Name: "L", Path: root, Enabled: true}
	if err := db.Create(&library).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}
	if err := db.Create(&models.CollectionArtist{MBID: "art-wanted", Name: "Wanted"}).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	seedRelease(t, db, "rel-other", "rg-other", "art-other", "Other Album", 1)
	if err := modules.MusicbrainzLoadCache(); err != nil {
		t.Fatalf("load cache: %v", err)
	}
	if err := db.Create(&models.LibraryItem{
		LibraryID:   library.ID,
		Path:        filepath.Join(root, "Other", "Other Album (2020)", "01 a.flac"),
		MBReleaseID: "rel-other",
		Status:      models.LibraryItemStatusUnmatched,
	}).Error; err != nil {
		t.Fatalf("create item: %v", err)
	}

	targets, err := ArtistTargets(db, "art-wanted")
	if err != nil {
		t.Fatalf("ArtistTargets: %v", err)
	}
	if len(targets) != 0 {
		t.Errorf("targets = %+v, want none — those files belong to another artist", targets)
	}
}

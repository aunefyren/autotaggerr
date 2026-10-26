package collection

import (
	"errors"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Authority per library: an artist Lidarr governs in one library is still the
// user's to want and follow in a native one.

// enabledLibraries is twoLibraries with both switched on, which is what makes the
// native one a place an unnamed want can go.
func enabledLibraries(t *testing.T, db *gorm.DB) (lidarrLib, nativeLib models.Library) {
	t.Helper()
	lidarrLib, nativeLib = twoLibraries(t, db)
	if err := db.Model(&models.Library{}).Where("id IN ?", []uuid.UUID{lidarrLib.ID, nativeLib.ID}).
		Update("enabled", true).Error; err != nil {
		t.Fatalf("enable: %v", err)
	}
	return lidarrLib, nativeLib
}

func TestWantTargetFollowsTheLibrarysAuthority(t *testing.T) {
	db := testDB(t)
	lidarrLib, nativeLib := enabledLibraries(t, db)
	mixed := models.CollectionArtist{MBID: "art-1", ManagedBy: models.ManagedByMixed}
	native := models.CollectionArtist{MBID: "art-2", ManagedBy: models.ManagedByAutotaggerr}

	if got, err := WantTarget(db, mixed, nil); err != nil || got != nativeLib.ID {
		t.Errorf("unnamed want for a Lidarr artist = %v, %v; want the one native library", got, err)
	}
	if _, err := WantTarget(db, mixed, &lidarrLib.ID); !errors.Is(err, ErrLibraryManaged) {
		t.Errorf("want into Lidarr's library: err = %v, want ErrLibraryManaged", err)
	}
	if got, err := WantTarget(db, mixed, &nativeLib.ID); err != nil || got != nativeLib.ID {
		t.Errorf("want into the native library = %v, %v", got, err)
	}
	if got, err := WantTarget(db, native, nil); err != nil || got != uuid.Nil {
		t.Errorf("unnamed want for a native artist = %v, %v; want no library, as before", got, err)
	}
	missing := uuid.New()
	if _, err := WantTarget(db, mixed, &missing); !errors.Is(err, ErrUnknownLibrary) {
		t.Errorf("unknown library: err = %v", err)
	}

	// Detached, Lidarr's library is native too.
	detached := mixed
	detached.ManagerDetached, detached.ManagedBy = true, models.ManagedByAutotaggerr
	if got, err := WantTarget(db, detached, &lidarrLib.ID); err != nil || got != lidarrLib.ID {
		t.Errorf("detached want into the Lidarr library = %v, %v", got, err)
	}

	// A second native library leaves an unnamed want nowhere to go.
	second := models.Library{Name: "Music 3", Path: "/nonexistent/three", Enabled: true}
	if err := db.Create(&second).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	if _, err := WantTarget(db, mixed, nil); !errors.Is(err, ErrChooseLibrary) {
		t.Errorf("two native libraries: err = %v, want ErrChooseLibrary", err)
	}
}

func TestDesireScopes(t *testing.T) {
	lidarrLib, nativeLib := uuid.New(), uuid.New()
	mixed := models.CollectionArtist{ManagedBy: models.ManagedByMixed}
	native := models.CollectionArtist{ManagedBy: models.ManagedByAutotaggerr}
	unscoped := models.CollectionDesire{Source: models.DesireSourceManual}
	manager := models.CollectionDesire{Source: models.DesireSourceManager}
	forNative := models.CollectionDesire{Source: models.DesireSourceManual, LibraryID: nativeLib}

	cases := []struct {
		name      string
		d         models.CollectionDesire
		artist    models.CollectionArtist
		library   uuid.UUID
		authority string
		want      bool
	}{
		{"overview shows everything", forNative, mixed, uuid.Nil, "", true},
		{"Lidarr's library shows Lidarr's selection", manager, mixed, lidarrLib, AuthorityLidarr, true},
		{"Lidarr's library shows an old unscoped want", unscoped, mixed, lidarrLib, AuthorityLidarr, true},
		{"Lidarr's library hides the native library's want", forNative, mixed, lidarrLib, AuthorityLidarr, false},
		{"the native library shows its own want", forNative, mixed, nativeLib, AuthorityNative, true},
		{"the native library hides Lidarr's selection", manager, mixed, nativeLib, AuthorityNative, false},
		{"the native library hides Lidarr's unscoped want", unscoped, mixed, nativeLib, AuthorityNative, false},
		{"a native artist's unscoped want is every native library's", unscoped, native, nativeLib, AuthorityNative, true},
	}
	for _, c := range cases {
		if got := DesireInScope(c.d, c.artist, c.library, c.authority); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestFollowAppliesWhereItWasPointed(t *testing.T) {
	lib, other := uuid.New(), uuid.New()
	mixed := models.CollectionArtist{ManagedBy: models.ManagedByMixed, Monitored: true}
	if FollowGoverns(mixed) {
		t.Error("a Lidarr artist's stored follow with no library governed")
	}
	mixed.FollowLibraryID = lib
	if !FollowGoverns(mixed) || !FollowAppliesIn(mixed, lib) || FollowAppliesIn(mixed, other) {
		t.Error("a follow pointed at one native library should apply there and nowhere else")
	}
	native := models.CollectionArtist{ManagedBy: models.ManagedByAutotaggerr, Monitored: true}
	if !FollowAppliesIn(native, lib) || !FollowAppliesIn(native, other) {
		t.Error("a native artist's follow with no library should apply in every native library")
	}
}

// TestSetDesireKeepsLibrariesApart: wanting an album in the native library neither
// clears Lidarr's selection nor is cleared by it, and clearing one library's want
// leaves the other's.
func TestSetDesireKeepsLibrariesApart(t *testing.T) {
	db := testDB(t)
	_, nativeLib := enabledLibraries(t, db)
	if err := db.Create(&models.CollectionArtist{MBID: "art-1", ManagedBy: models.ManagedByMixed}).Error; err != nil {
		t.Fatalf("artist: %v", err)
	}
	if err := db.Create(&models.CollectionDesire{
		ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", ReleaseMBID: "rel-lidarr", Source: models.DesireSourceManager,
	}).Error; err != nil {
		t.Fatalf("manager want: %v", err)
	}

	if _, err := SetDesire(db, DesireInput{ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", LibraryID: nativeLib.ID}); err != nil {
		t.Fatalf("SetDesire: %v", err)
	}
	var rows []models.CollectionDesire
	db.Order("source").Find(&rows)
	if len(rows) != 2 {
		t.Fatalf("wants = %+v, want Lidarr's edition and the native any-edition want side by side", rows)
	}

	// Narrowing the native want to an edition replaces its own any-edition row only.
	if _, err := SetDesire(db, DesireInput{ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", ReleaseMBID: "rel-native", LibraryID: nativeLib.ID}); err != nil {
		t.Fatalf("SetDesire edition: %v", err)
	}
	rows = nil
	db.Find(&rows)
	if len(rows) != 2 {
		t.Fatalf("wants = %+v, want two", rows)
	}
	for _, d := range rows {
		if d.Source == models.DesireSourceManual && (d.ReleaseMBID != "rel-native" || d.LibraryID != nativeLib.ID) {
			t.Errorf("native want = %+v", d)
		}
	}

	if err := ClearDesireIn(db, "art-1", "rg-1", "rel-native", nativeLib.ID); err != nil {
		t.Fatalf("ClearDesireIn: %v", err)
	}
	if err := ClearDesireIn(db, "art-1", "rg-1", "rel-lidarr", nativeLib.ID); err != nil {
		t.Fatalf("ClearDesireIn: %v", err)
	}
	rows = nil
	db.Find(&rows)
	if len(rows) != 1 || rows[0].Source != models.DesireSourceManager {
		t.Errorf("after clearing in the native library = %+v, want Lidarr's selection untouched", rows)
	}
}

// TestRebuildNarrowsAWantByItsOwnLibrary: an "any edition" want for the native
// library is not fulfilled by Lidarr's copy, and narrows once the native library
// holds the album.
func TestRebuildNarrowsAWantByItsOwnLibrary(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)
	seedTrackedRelease(t, db, "rel-1", "rg-1", "art-1", 4)
	if err := modules.MusicbrainzLoadCache(); err != nil {
		t.Fatalf("load cache: %v", err)
	}
	lidarrLib, nativeLib := enabledLibraries(t, db)
	ownTracks(t, db, lidarrLib, "rel-1", 4)
	if _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if _, err := SetDesire(db, DesireInput{ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", LibraryID: nativeLib.ID}); err != nil {
		t.Fatalf("SetDesire: %v", err)
	}

	if _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	var rows []models.CollectionDesire
	db.Find(&rows)
	if len(rows) != 1 || rows[0].ReleaseMBID != "" {
		t.Fatalf("wants = %+v, want the any-edition want still open: Lidarr's copy is not the native library's", rows)
	}

	ownTracks(t, db, nativeLib, "rel-1", 4)
	if _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	rows = nil
	db.Find(&rows)
	if len(rows) != 1 || rows[0].ReleaseMBID != "rel-1" || rows[0].Source != models.DesireSourceAuto || rows[0].LibraryID != nativeLib.ID {
		t.Errorf("wants = %+v, want narrowed to rel-1 in the native library", rows)
	}

	h, err := LibraryHoldings(db, nativeLib.ID, []string{"rg-1"})
	if err != nil {
		t.Fatalf("LibraryHoldings: %v", err)
	}
	if got := h["rg-1"]; got.OwnedTracks != 4 || got.TotalTracks != 4 || got.Editions != 1 {
		t.Errorf("holding = %+v, want 4/4 in one edition", got)
	}
}

// TestManagerWantsIgnoreAnotherLibrarysWant: a native library's want is not a veto
// on Lidarr selecting an edition for its own library.
func TestManagerWantsIgnoreAnotherLibrarysWant(t *testing.T) {
	db := testDB(t)
	_, nativeLib := enabledLibraries(t, db)
	if err := db.Create(&models.CollectionArtist{MBID: "art-1", ManagedBy: models.ManagedByMixed}).Error; err != nil {
		t.Fatalf("artist: %v", err)
	}
	if err := db.Create(&models.CollectionReleaseGroup{
		MBID: "rg-1", ArtistMBID: "art-1", InCatalog: true, CatalogMonitored: true, CatalogReleaseMBID: "rel-1",
	}).Error; err != nil {
		t.Fatalf("release-group: %v", err)
	}
	if err := db.Create(&models.CollectionDesire{
		ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", LibraryID: nativeLib.ID, Source: models.DesireSourceManual,
	}).Error; err != nil {
		t.Fatalf("native want: %v", err)
	}

	if err := reconcileManagerDesires(db); err != nil {
		t.Fatalf("reconcileManagerDesires: %v", err)
	}
	var n int64
	db.Model(&models.CollectionDesire{}).Where("source = ? AND release_mb_id = ?", models.DesireSourceManager, "rel-1").Count(&n)
	if n != 1 {
		t.Errorf("manager wants = %d, want Lidarr's selection recorded beside the native want", n)
	}
}

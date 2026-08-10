package collection

import (
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// recacheRelease replaces a cached release in place and reloads, so a test can move
// an album between artists upstream the way MusicBrainz does.
func recacheRelease(t *testing.T, db *gorm.DB, release models.MusicBrainzReleaseResponse) {
	t.Helper()
	if err := db.Where("mb_id = ?", release.ID).Delete(&models.MusicbrainzReleaseCache{}).Error; err != nil {
		t.Fatalf("clear cache row: %v", err)
	}
	seedCachedRelease(t, db, release)
}

// soundtrack builds the migrated-soundtrack release, credited to whoever the
// release-group currently names.
func soundtrack(groupCredit ...models.ArtistCredit) models.MusicBrainzReleaseResponse {
	return models.MusicBrainzReleaseResponse{
		ID:    "rel-1",
		Title: "Over the Hedge",
		ArtistCredit: []models.ArtistCredit{
			{Name: "Various Artists", Artist: models.Artist{ID: models.VariousArtistsMBID, Name: "Various Artists"}},
		},
		ReleaseGroup: models.ReleaseGroup{
			ID: "rg-1", Title: "Over the Hedge", PrimaryType: "Album",
			SecondaryTypes: []string{"Soundtrack"},
			ArtistCredit:   groupCredit,
		},
		Media: []models.MusicBrainzMedia{{Tracks: []models.Track{{ID: "t1"}}}},
	}
}

// seedOwnedSoundtrack wires a library with one owned file of the soundtrack.
func seedOwnedSoundtrack(t *testing.T, db *gorm.DB) {
	t.Helper()
	lib := models.Library{Name: "L", Path: "/m"}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	if err := db.Create(&models.LibraryItem{LibraryID: lib.ID, Path: "/m/01.flac", Status: "ok", MBReleaseID: "rel-1"}).Error; err != nil {
		t.Fatalf("item: %v", err)
	}
}

// linkedArtists is the set of artists a release-group is credited to, by link row.
func linkedArtists(t *testing.T, db *gorm.DB, rgMBID string) map[string]models.CollectionReleaseGroupArtist {
	t.Helper()
	var links []models.CollectionReleaseGroupArtist
	if err := db.Where("release_group_mb_id = ?", rgMBID).Find(&links).Error; err != nil {
		t.Fatalf("read links: %v", err)
	}
	out := map[string]models.CollectionReleaseGroupArtist{}
	for _, l := range links {
		out[l.ArtistMBID] = l
	}
	return out
}

// TestRebuildUnlinksAMigratedAwayArtist is the other half of reading the
// release-group's credit: adding the new link puts the album on the right artist's
// page, but only removing the old one takes it off the wrong one. Links are unioned
// into ReleaseGroupsForArtist, so a stale row keeps the album on a page MusicBrainz
// has moved it off — permanently, since nothing else ever subtracts.
func TestRebuildUnlinksAMigratedAwayArtist(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)

	// Before the migration: the release-group is still the placeholder's.
	seedCachedRelease(t, db, soundtrack(
		models.ArtistCredit{Name: "Various Artists", Artist: models.Artist{ID: models.VariousArtistsMBID, Name: "Various Artists"}},
	))
	seedOwnedSoundtrack(t, db)
	if _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild (before): %v", err)
	}
	if links := linkedArtists(t, db, "rg-1"); !links[models.VariousArtistsMBID].FromDisk {
		t.Fatalf("expected a disk-sourced link to the placeholder first, got %+v", links)
	}

	// Upstream moves the release-group to the composer.
	recacheRelease(t, db, soundtrack(
		models.ArtistCredit{Name: "Ben Folds", Artist: models.Artist{ID: "art-folds", Name: "Ben Folds"}},
	))
	if _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild (after): %v", err)
	}

	links := linkedArtists(t, db, "rg-1")
	if _, still := links[models.VariousArtistsMBID]; still {
		t.Error("the placeholder's link survived the migration — the album stays on their page forever")
	}
	if !links["art-folds"].FromDisk {
		t.Errorf("the composer should hold a disk-sourced link, got %+v", links)
	}

	groups, err := ReleaseGroupsForArtist(db, models.VariousArtistsMBID)
	if err != nil {
		t.Fatalf("ReleaseGroupsForArtist: %v", err)
	}
	if len(groups) != 0 {
		t.Errorf("the placeholder still sees %d release-group(s)", len(groups))
	}
}

// TestRebuildKeepsAManagerClaim: a manager naming this artist is a separate
// authority's answer, and MusicBrainz re-crediting the release-group does not
// overrule it. The row survives and gives up only its disk half.
func TestRebuildKeepsAManagerClaim(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)

	seedCachedRelease(t, db, soundtrack(
		models.ArtistCredit{Name: "Ben Folds", Artist: models.Artist{ID: "art-folds", Name: "Ben Folds"}},
	))
	seedOwnedSoundtrack(t, db)

	// A mirror claimed this album for a different artist, and also left a disk claim
	// behind from before the migration.
	if err := db.Create(&models.CollectionReleaseGroupArtist{
		ReleaseGroupMBID: "rg-1", ArtistMBID: "art-manager",
		FromDisk: true, FromCatalog: true,
	}).Error; err != nil {
		t.Fatalf("seed link: %v", err)
	}

	if _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	links := linkedArtists(t, db, "rg-1")
	claim, ok := links["art-manager"]
	if !ok {
		t.Fatal("a manager's claim must not be deleted by a MusicBrainz credit change")
	}
	if claim.FromDisk {
		t.Error("the disk half of the claim should have been given up")
	}
	if !claim.FromCatalog {
		t.Error("the catalog half of the claim should remain")
	}
}

// TestPartialWriterNeverUnlinks: only a caller holding the full credit may subtract.
// A discography sync knows its own artist is credited and nothing about anyone else,
// so letting it prune would delete the first artist of a collaboration — the exact
// bug the link table exists to fix, from the other end.
func TestPartialWriterNeverUnlinks(t *testing.T) {
	db := testDB(t)

	if err := upsertReleaseGroup(db, rgWrite{
		mbID: "rg-1", artistMBID: "art-1", credits: []string{"art-1", "art-2"},
		title: "Collab", disk: &diskState{owned: true, ownedTracks: 1, totalTracks: 1},
	}); err != nil {
		t.Fatalf("seed authoritative: %v", err)
	}

	// A catalog writer that knows only about art-3 must add, never replace.
	if err := upsertReleaseGroup(db, rgWrite{
		mbID: "rg-1", artistMBID: "art-3", title: "Collab",
		catalog: &catalogState{},
	}); err != nil {
		t.Fatalf("catalog write: %v", err)
	}

	links := linkedArtists(t, db, "rg-1")
	for _, mbid := range []string{"art-1", "art-2", "art-3"} {
		if _, ok := links[mbid]; !ok {
			t.Errorf("artist %s lost their link to a partial writer", mbid)
		}
	}
	if !links["art-3"].FromCatalog || links["art-3"].FromDisk {
		t.Errorf("the catalog writer's link has the wrong provenance: %+v", links["art-3"])
	}
}

// TestLegacyLinkIsCleanable: rows written before the provenance flags carry neither,
// and are read as disk claims. That reading is what makes a stale credit from before
// the columns existed removable at all — the whole point of the change.
func TestLegacyLinkIsCleanable(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)

	seedCachedRelease(t, db, soundtrack(
		models.ArtistCredit{Name: "Ben Folds", Artist: models.Artist{ID: "art-folds", Name: "Ben Folds"}},
	))
	seedOwnedSoundtrack(t, db)

	if err := db.Create(&models.CollectionReleaseGroupArtist{
		ReleaseGroupMBID: "rg-1", ArtistMBID: models.VariousArtistsMBID,
	}).Error; err != nil {
		t.Fatalf("seed legacy link: %v", err)
	}

	if _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if _, still := linkedArtists(t, db, "rg-1")[models.VariousArtistsMBID]; still {
		t.Error("a legacy link to an artist MusicBrainz no longer credits should be removed")
	}
}

// TestPruneOrphanArtists covers the guards. Unlinking can empty an artist, and an
// artist row with nothing on it is not neutral — it sits in the collection list
// claiming to be part of the library. Everything that could still make the page worth
// opening stops the removal.
func TestPruneOrphanArtists(t *testing.T) {
	db := testDB(t)

	seed := func(mbid, origin string, monitored bool) {
		t.Helper()
		if err := db.Create(&models.CollectionArtist{
			MBID: mbid, Name: mbid, Origin: origin, Monitored: monitored,
		}).Error; err != nil {
			t.Fatalf("seed artist %s: %v", mbid, err)
		}
	}

	seed("orphan", models.CollectionOriginLibrary, false)
	seed("manual", models.CollectionOriginManual, false)
	seed("followed", models.CollectionOriginLibrary, true)
	seed("wanted", models.CollectionOriginLibrary, false)
	seed("linked", models.CollectionOriginLibrary, false)
	seed("primary", models.CollectionOriginLibrary, false)
	seed("owns-edition", models.CollectionOriginLibrary, false)

	if err := db.Create(&models.CollectionDesire{ArtistMBID: "wanted", ReleaseGroupMBID: "rg-x"}).Error; err != nil {
		t.Fatalf("seed desire: %v", err)
	}
	if err := db.Create(&models.CollectionReleaseGroupArtist{ReleaseGroupMBID: "rg-y", ArtistMBID: "linked"}).Error; err != nil {
		t.Fatalf("seed link: %v", err)
	}
	if err := db.Create(&models.CollectionReleaseGroup{MBID: "rg-z", ArtistMBID: "primary"}).Error; err != nil {
		t.Fatalf("seed group: %v", err)
	}
	if err := db.Create(&models.CollectionRelease{MBID: "rel-z", ReleaseGroupMBID: "rg-w", ArtistMBID: "owns-edition"}).Error; err != nil {
		t.Fatalf("seed edition: %v", err)
	}

	pruned, err := pruneOrphanArtists(db)
	if err != nil {
		t.Fatalf("pruneOrphanArtists: %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}

	for _, mbid := range []string{"manual", "followed", "wanted", "linked", "primary", "owns-edition"} {
		if err := db.Where("mb_id = ?", mbid).First(&models.CollectionArtist{}).Error; err != nil {
			t.Errorf("artist %q should have been kept: %v", mbid, err)
		}
	}
	if err := db.Where("mb_id = ?", "orphan").First(&models.CollectionArtist{}).Error; err == nil {
		t.Error("an unreferenced library-derived artist should have been removed")
	}
}

// TestCatalogChecked: a manager view exists only once a manager has been asked.
func TestCatalogChecked(t *testing.T) {
	if CatalogChecked(models.CollectionArtist{}) {
		t.Error("an artist nobody synced has no catalog to compare against")
	}
	now := time.Now()
	if !CatalogChecked(models.CollectionArtist{LastSyncedAt: &now}) {
		t.Error("a synced artist has been asked, so their albums are comparable")
	}
}

// TestSyncLidarrStampsLastSynced: the mirror has to record that it asked, or every
// Lidarr artist reads as never-consulted and no album of theirs can ever report a
// discrepancy.
func TestSyncLidarrStampsLastSynced(t *testing.T) {
	albums := []models.LidarrAlbum{monitoredAlbum("rg-1", "rel-1")}
	srv := lidarrServing(t, &albums)

	db := testDB(t)
	if err := db.Create(&models.Manager{
		Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true,
		LidarrBaseURL: srv.URL, LidarrAPIKey: "k",
	}).Error; err != nil {
		t.Fatalf("manager: %v", err)
	}
	if err := db.Create(&models.CollectionArtist{
		MBID: "art-1", Name: "Band", ManagedBy: models.ManagedByLidarr,
	}).Error; err != nil {
		t.Fatalf("artist: %v", err)
	}

	if _, err := SyncLidarr(db); err != nil {
		t.Fatalf("SyncLidarr: %v", err)
	}

	var artist models.CollectionArtist
	if err := db.Where("mb_id = ?", "art-1").First(&artist).Error; err != nil {
		t.Fatalf("reload artist: %v", err)
	}
	if !CatalogChecked(artist) {
		t.Error("SyncLidarr must record that it asked the manager about this artist")
	}
}

// TestRebuildCountsCreditChanges: an upstream re-credit is the one identity change
// that leaves no trace — both IDs survive it, so nothing fails, nothing is queued for
// review, and the album is simply somewhere else next time you look. The count is what
// makes the run that did it say so.
func TestRebuildCountsCreditChanges(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)

	seedCachedRelease(t, db, soundtrack(
		models.ArtistCredit{Name: "Various Artists", Artist: models.Artist{ID: models.VariousArtistsMBID, Name: "Various Artists"}},
	))
	seedOwnedSoundtrack(t, db)

	// First sight of an album is not a change: nothing moved, we just had not looked.
	stats, err := Rebuild(db)
	if err != nil {
		t.Fatalf("Rebuild (first): %v", err)
	}
	if stats.CreditChanges != 0 {
		t.Errorf("first sight of an album reported %d credit change(s), want 0", stats.CreditChanges)
	}

	// A second pass over unchanged data must stay silent, or the number is noise.
	if stats, err = Rebuild(db); err != nil {
		t.Fatalf("Rebuild (idempotent): %v", err)
	} else if stats.CreditChanges != 0 {
		t.Errorf("an unchanged rebuild reported %d credit change(s), want 0", stats.CreditChanges)
	}

	// Now the album moves upstream: the primary credit changes and the old link goes.
	recacheRelease(t, db, soundtrack(
		models.ArtistCredit{Name: "Ben Folds", Artist: models.Artist{ID: "art-folds", Name: "Ben Folds"}},
	))
	if stats, err = Rebuild(db); err != nil {
		t.Fatalf("Rebuild (migrated): %v", err)
	}
	if stats.CreditChanges != 2 {
		t.Errorf("credit changes = %d, want 2 (one regroup + one unlink)", stats.CreditChanges)
	}

	// And it settles: the move is reported once, by the run that made it.
	if stats, err = Rebuild(db); err != nil {
		t.Fatalf("Rebuild (settled): %v", err)
	} else if stats.CreditChanges != 0 {
		t.Errorf("the move was re-reported %d time(s) after settling", stats.CreditChanges)
	}
}

// TestManagerWritesAreNotCreditChanges: a mirror rewriting its own catalog is not
// MusicBrainz changing its mind, and counting it would bury the signal in noise.
func TestManagerWritesAreNotCreditChanges(t *testing.T) {
	db := testDB(t)

	changes := &creditChanges{}
	if err := upsertReleaseGroup(db, rgWrite{
		mbID: "rg-1", artistMBID: "art-1", credits: []string{"art-1"},
		title: "Album", disk: &diskState{owned: true, ownedTracks: 1, totalTracks: 1},
		changes: changes,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if changes.total() != 0 {
		t.Fatalf("creating a release-group counted %d change(s)", changes.total())
	}

	// A catalog writer naming someone else reports nothing, because it passes no
	// accumulator — and it must not move the primary credit either.
	if err := upsertReleaseGroup(db, rgWrite{
		mbID: "rg-1", artistMBID: "art-2", title: "Album", catalog: &catalogState{},
	}); err != nil {
		t.Fatalf("catalog write: %v", err)
	}
	if changes.total() != 0 {
		t.Errorf("a manager write counted %d credit change(s), want 0", changes.total())
	}

	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", "rg-1").First(&rg).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if rg.ArtistMBID != "art-1" {
		t.Errorf("primary credit = %q, want art-1 — a partial writer may not claim it", rg.ArtistMBID)
	}
}

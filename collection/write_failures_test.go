package collection

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
)

// Failures part-way through a pass. A closed database fails at the first query; these
// let the reads succeed and break one table, so the error paths behind them are the
// ones exercised — and so the assertions can check what a half-failed pass left behind.

// blockWrites makes SQLite refuse one kind of write on one table, leaving reads alone.
func blockWrites(t *testing.T, db *gorm.DB, table, op string) {
	t.Helper()
	name := fmt.Sprintf("block_%s_%s", op, table)
	if err := db.Exec(fmt.Sprintf(
		"CREATE TRIGGER %s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT, 'blocked by test'); END",
		name, op, table)).Error; err != nil {
		t.Fatalf("create trigger %s: %v", name, err)
	}
}

// dropTable removes one table so every query against it fails.
func dropTable(t *testing.T, db *gorm.DB, model any) {
	t.Helper()
	if err := db.Migrator().DropTable(model); err != nil {
		t.Fatalf("drop table: %v", err)
	}
}

// TestPruneStopsOnAFailedDelete: a prune that cannot delete reports the error rather
// than counting the row as pruned, and leaves the release-group in place.
func TestPruneStopsOnAFailedDelete(t *testing.T) {
	for _, table := range []string{"collection_release_group_artists", "collection_release_groups"} {
		t.Run(table, func(t *testing.T) {
			db := testDB(t)
			storeGroup(t, db, "rg-gone", "artist-1", nil)
			blockWrites(t, db, table, "DELETE")

			pruned, err := PruneOrphanReleaseGroups(db, "artist-1", live("rg-other"))
			if err == nil || pruned != 0 {
				t.Errorf("pruned = %d, err = %v, want an error and nothing pruned", pruned, err)
			}
			if !groupExists(t, db, "rg-gone") {
				t.Error("the release-group must survive a failed prune")
			}
		})
	}
}

// TestPruneStopsWhenTheClaimCheckFails: an orphan check that cannot be answered is not
// a yes — a prune that guessed would delete albums a want still holds.
func TestPruneStopsWhenTheClaimCheckFails(t *testing.T) {
	db := testDB(t)
	storeGroup(t, db, "rg-gone", "artist-1", nil)
	dropTable(t, db, &models.CollectionDesire{})

	if _, err := PruneOrphanReleaseGroups(db, "artist-1", live("rg-other")); err == nil {
		t.Error("an unanswerable orphan check must fail the prune")
	}
	if !groupExists(t, db, "rg-gone") {
		t.Error("the release-group must survive")
	}
}

// TestOrphanCheckFailsOnEachClaim: each of the three claims a release-group can carry
// outside its own row is checked, and failing to read any of them is an error.
func TestOrphanCheckFailsOnEachClaim(t *testing.T) {
	for name, model := range map[string]any{
		"credits":  &models.CollectionReleaseGroupArtist{},
		"editions": &models.CollectionRelease{},
	} {
		t.Run(name, func(t *testing.T) {
			db := testDB(t)
			dropTable(t, db, model)
			if _, err := isOrphanReleaseGroup(db, "artist-1", "rg-1"); err == nil {
				t.Error("want an error")
			}
		})
	}
}

// TestRetireStopsOnAFailedDelete mirrors the prune case for the confirmed-deletion
// path, and the retirable check fails the same way when the claim check does.
func TestRetireStopsOnAFailedDelete(t *testing.T) {
	for _, table := range []string{"collection_release_group_artists", "collection_release_groups"} {
		t.Run(table, func(t *testing.T) {
			db := testDB(t)
			storeGroup(t, db, "rg-dead", "artist-1", nil)
			blockWrites(t, db, table, "DELETE")

			removed, _, err := RetireReleaseGroup(db, "rg-dead")
			if err == nil || removed {
				t.Errorf("removed = %v, err = %v, want an error", removed, err)
			}
			if !groupExists(t, db, "rg-dead") {
				t.Error("the release-group must survive a failed retirement")
			}
		})
	}

	db := testDB(t)
	storeGroup(t, db, "rg-dead", "artist-1", nil)
	dropTable(t, db, &models.CollectionDesire{})
	if _, _, err := ReleaseGroupRetirable(db, "rg-dead"); err == nil {
		t.Error("ReleaseGroupRetirable must fail when the claim check does")
	}
	if _, _, err := RetireReleaseGroup(db, "rg-dead"); err == nil {
		t.Error("RetireReleaseGroup must fail when the claim check does")
	}
}

// TestPruneOrphanArtistsFailures: the artist prune stops on a claim it cannot read and
// on a delete it cannot make, removing nothing in either case.
func TestPruneOrphanArtistsFailures(t *testing.T) {
	seed := func(t *testing.T) *gorm.DB {
		db := testDB(t)
		if err := db.Create(&models.CollectionArtist{
			MBID: "art-orphan", Name: "Orphan", Origin: models.CollectionOriginLibrary,
		}).Error; err != nil {
			t.Fatalf("artist: %v", err)
		}
		return db
	}
	remaining := func(t *testing.T, db *gorm.DB) int64 {
		var n int64
		db.Model(&models.CollectionArtist{}).Where("mb_id = ?", "art-orphan").Count(&n)
		return n
	}

	db := seed(t)
	dropTable(t, db, &models.CollectionDesire{})
	if _, err := pruneOrphanArtists(db); err == nil {
		t.Error("an unreadable claim must fail the artist prune")
	}
	if remaining(t, db) != 1 {
		t.Error("the artist must survive")
	}

	db = seed(t)
	blockWrites(t, db, "collection_artists", "DELETE")
	if _, err := pruneOrphanArtists(db); err == nil {
		t.Error("a failed delete must fail the artist prune")
	}
	if remaining(t, db) != 1 {
		t.Error("the artist must survive")
	}
}

// TestAllMBIDsFailsOnEachSource: the metadata refresh covers files, editions and
// artists; failing to read any one of them is an error, not a shorter list.
func TestAllMBIDsFailsOnEachSource(t *testing.T) {
	for name, model := range map[string]any{
		"editions": &models.CollectionRelease{},
		"artists":  &models.CollectionArtist{},
	} {
		t.Run(name, func(t *testing.T) {
			db := testDB(t)
			dropTable(t, db, model)
			if _, _, err := AllMBIDs(db); err == nil {
				t.Error("want an error")
			}
		})
	}
}

// TestCreditLinkWrites: an existing link is renumbered only by an authoritative
// writer and gains the disk flag from a disk writer; failed writes are logged and the
// pass carries on to the next artist.
func TestCreditLinkWrites(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionReleaseGroupArtist{
		ReleaseGroupMBID: "rg-1", ArtistMBID: "art-b", Position: 0, FromCatalog: true,
	}).Error; err != nil {
		t.Fatalf("seed link: %v", err)
	}

	// A blank credit is skipped without shifting anyone else's position.
	linkReleaseGroupArtists(db, "rg-1", []string{"art-a", "", "art-b"}, true, creditFromDisk)

	links := linkedArtists(t, db, "rg-1")
	if a, ok := links["art-a"]; !ok || a.Position != 0 || !a.FromDisk {
		t.Errorf("art-a = %+v (present %v), want a new disk link at position 0", a, ok)
	}
	if b := links["art-b"]; b.Position != 2 || !b.FromDisk || !b.FromCatalog {
		t.Errorf("art-b = %+v, want renumbered to 2 and claimed by both disk and catalog", b)
	}

	// Failed writes do not stop the loop or panic.
	blockWrites(t, db, "collection_release_group_artists", "UPDATE")
	blockWrites(t, db, "collection_release_group_artists", "INSERT")
	linkReleaseGroupArtists(db, "rg-1", []string{"art-b", "art-c"}, true, creditFromDisk)
	links = linkedArtists(t, db, "rg-1")
	if _, ok := links["art-c"]; ok {
		t.Error("a blocked insert still produced a link")
	}
	if links["art-b"].Position != 2 {
		t.Error("a blocked update still renumbered the link")
	}
}

// TestCreditPruneWriteFailures: pruneReleaseGroupArtists counts only what it actually
// removed. A manager-claimed link loses only its disk flag; a failed write leaves the
// link exactly as it was.
func TestCreditPruneWriteFailures(t *testing.T) {
	seed := func(t *testing.T) *gorm.DB {
		db := testDB(t)
		for _, l := range []models.CollectionReleaseGroupArtist{
			{ReleaseGroupMBID: "rg-1", ArtistMBID: "art-keep", FromDisk: true},
			{ReleaseGroupMBID: "rg-1", ArtistMBID: "art-managed", FromDisk: true, FromCatalog: true},
			{ReleaseGroupMBID: "rg-1", ArtistMBID: "art-stale", FromDisk: true},
		} {
			if err := db.Create(&l).Error; err != nil {
				t.Fatalf("seed link: %v", err)
			}
		}
		return db
	}

	db := seed(t)
	blockWrites(t, db, "collection_release_group_artists", "UPDATE")
	blockWrites(t, db, "collection_release_group_artists", "DELETE")
	if n := pruneReleaseGroupArtists(db, "rg-1", []string{"art-keep"}); n != 0 {
		t.Errorf("removed = %d with writes blocked, want 0", n)
	}
	links := linkedArtists(t, db, "rg-1")
	if len(links) != 3 || !links["art-managed"].FromDisk {
		t.Errorf("links = %+v, want all three untouched", links)
	}
}

// TestBackfillCreditLinksFailedInsert: an upgrade backfill that cannot write reports
// it, so startup can say the artist pages will be empty rather than silently being so.
func TestBackfillCreditLinksFailedInsert(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionReleaseGroup{MBID: "rg-legacy", ArtistMBID: "art-1"}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	blockWrites(t, db, "collection_release_group_artists", "INSERT")
	if err := BackfillReleaseGroupArtists(db); err == nil {
		t.Error("a failed backfill insert must be an error")
	}
}

// TestCreditedArtistsFallsBackToTheRowArtist: a release-group with no link rows and
// no primary credit is credited to nobody, rather than to an empty-string artist.
func TestCreditedArtistsFallsBackToTheRowArtist(t *testing.T) {
	if got := CreditedArtists(models.CollectionReleaseGroup{MBID: "rg-1"}, nil); len(got) != 0 {
		t.Errorf("CreditedArtists(no credit) = %v, want none", got)
	}
	got := CreditedArtists(models.CollectionReleaseGroup{MBID: "rg-1", ArtistMBID: "art-1"}, nil)
	if len(got) != 1 || got[0] != "art-1" {
		t.Errorf("CreditedArtists(row artist only) = %v, want [art-1]", got)
	}
}

// TestSetDesireWriteFailures: a want that could not be stored is an error, never a
// silently returned zero row the caller would render as saved.
func TestSetDesireWriteFailures(t *testing.T) {
	in := DesireInput{ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1"}

	db := testDB(t)
	blockWrites(t, db, "collection_desires", "INSERT")
	if _, err := SetDesire(db, in); err == nil {
		t.Error("a blocked insert must fail SetDesire")
	}

	db = testDB(t)
	if _, err := SetDesire(db, in); err != nil {
		t.Fatalf("SetDesire: %v", err)
	}
	blockWrites(t, db, "collection_desires", "UPDATE")
	if _, err := SetDesire(db, in); err == nil {
		t.Error("a blocked update of an existing want must fail SetDesire")
	}

	db = testDB(t)
	if _, err := SetDesire(db, in); err != nil {
		t.Fatalf("SetDesire: %v", err)
	}
	blockWrites(t, db, "collection_desires", "DELETE")
	if _, err := SetDesire(db, DesireInput{ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", ReleaseMBID: "rel-1"}); err == nil {
		t.Error("a blocked clear of the opposite want kind must fail SetDesire")
	}
}

// TestDesiresForArtistIncludesCollaborations: the artist page shows its own wants
// plus wants another artist recorded on a group this page is showing, and no others.
func TestDesiresForArtistIncludesCollaborations(t *testing.T) {
	db := testDB(t)
	for _, in := range []DesireInput{
		{ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1"},
		{ArtistMBID: "art-2", ReleaseGroupMBID: "rg-collab"},
		{ArtistMBID: "art-2", ReleaseGroupMBID: "rg-theirs"},
	} {
		if _, err := SetDesire(db, in); err != nil {
			t.Fatalf("SetDesire: %v", err)
		}
	}
	got, err := DesiresForArtist(db, "art-1", []string{"rg-collab"})
	if err != nil {
		t.Fatalf("DesiresForArtist: %v", err)
	}
	if len(got) != 2 || got[0].ReleaseGroupMBID != "rg-1" || got[1].ReleaseGroupMBID != "rg-collab" {
		t.Errorf("desires = %+v, want rg-1 and rg-collab", got)
	}
}

// TestDetachWriteFailures: detaching is all or nothing — when the wants cannot be
// re-labelled the artist stays managed, so no want is left pointing at an authority
// that has stopped governing it.
func TestDetachWriteFailures(t *testing.T) {
	db, manager := managedCollection(t)
	managerWant(t, db)
	blockWrites(t, db, "collection_desires", "UPDATE")

	if _, err := DetachArtist(db, "art-1"); err == nil {
		t.Fatal("a blocked want update must fail the detach")
	}
	a := artistRow(t, db, "art-1")
	if a.ManagerDetached || a.ManagedBy != models.ManagedByLidarr {
		t.Errorf("artist = %+v, want it still managed", a)
	}

	// Deleting the manager logs the artist it could not detach and reports none.
	n, err := DetachManagerArtists(db, manager.ID)
	if err != nil || n != 0 {
		t.Errorf("DetachManagerArtists = %d, %v; want 0 and no error", n, err)
	}
}

// TestReattachWriteFailure: a reattach that cannot be written leaves the artist
// detached.
func TestReattachWriteFailure(t *testing.T) {
	db, _ := managedCollection(t)
	if _, err := DetachArtist(db, "art-1"); err != nil {
		t.Fatalf("DetachArtist: %v", err)
	}
	blockWrites(t, db, "collection_artists", "UPDATE")
	if _, err := ReattachArtist(db, "art-1"); err == nil {
		t.Error("a blocked update must fail the reattach")
	}
	if !artistRow(t, db, "art-1").ManagerDetached {
		t.Error("the artist must stay detached")
	}
}

// TestReattachIsIdempotent: reattaching an artist that was never detached returns it
// unchanged rather than re-deriving anything.
func TestReattachIsIdempotent(t *testing.T) {
	db, _ := managedCollection(t)
	a, err := ReattachArtist(db, "art-1")
	if err != nil {
		t.Fatalf("ReattachArtist: %v", err)
	}
	if a.ManagerDetached || a.ManagedBy != models.ManagedByLidarr {
		t.Errorf("artist = %+v, want unchanged", a)
	}
	if Detachable(models.CollectionArtist{ManagedBy: models.ManagedByLidarr, ManagerDetached: true}) {
		t.Error("an already-detached artist is not detachable again")
	}
}

// TestDeriveArtistManager: the provenance a reattach re-derives follows the files.
// Files in a library that no longer exists give "unknown"; files for another artist or
// for an uncached release say nothing; no files at all is the native manager.
func TestDeriveArtistManager(t *testing.T) {
	db, _ := managedCollection(t)

	if got, err := deriveArtistManager(db, "art-nobody"); err != nil || got != models.ManagedByAutotaggerr {
		t.Errorf("artist with no files = %q, %v; want native", got, err)
	}

	// An uncached release and an orphaned library row join the indexed file.
	var lib models.Library
	if err := db.First(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	if err := db.Create(&models.LibraryItem{
		LibraryID: lib.ID, Path: "/m/b.flac", Status: models.LibraryItemStatusOK, MBReleaseID: "rel-uncached",
	}).Error; err != nil {
		t.Fatalf("item: %v", err)
	}
	if got, err := deriveArtistManager(db, "art-1"); err != nil || got != models.ManagedByLidarr {
		t.Errorf("with an uncached sibling = %q, %v; want lidarr", got, err)
	}

	if err := db.Delete(&lib).Error; err != nil {
		t.Fatalf("delete library: %v", err)
	}
	if got, err := deriveArtistManager(db, "art-1"); err != nil || got != models.ManagedByUnknown {
		t.Errorf("library gone = %q, %v; want unknown", got, err)
	}
}

// TestDetachManagerArtistsScope: only the manager's own libraries are drained, an
// artist already detached is not counted twice, and files whose release is not cached
// contribute no artist.
func TestDetachManagerArtistsScope(t *testing.T) {
	db, manager := managedCollection(t)

	other := models.Library{Name: "Other", Path: "/o"}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	var lib models.Library
	if err := db.Where("path = ?", "/m").First(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	for _, item := range []models.LibraryItem{
		{LibraryID: other.ID, Path: "/o/a.flac", Status: models.LibraryItemStatusOK, MBReleaseID: "rel-1"},
		{LibraryID: lib.ID, Path: "/m/b.flac", Status: models.LibraryItemStatusOK, MBReleaseID: "rel-1"},
		{LibraryID: lib.ID, Path: "/m/c.flac", Status: models.LibraryItemStatusOK, MBReleaseID: "rel-uncached"},
	} {
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("item: %v", err)
		}
	}

	if n, err := DetachManagerArtists(db, manager.ID); err != nil || n != 1 {
		t.Fatalf("first drain = %d, %v; want 1", n, err)
	}
	if n, err := DetachManagerArtists(db, manager.ID); err != nil || n != 0 {
		t.Errorf("second drain = %d, %v; want 0 — already detached", n, err)
	}

	// An artist credited on disk but with no collection row is skipped, not an error.
	if err := db.Where("mb_id = ?", "art-1").Delete(&models.CollectionArtist{}).Error; err != nil {
		t.Fatalf("delete artist: %v", err)
	}
	if n, err := DetachManagerArtists(db, manager.ID); err != nil || n != 0 {
		t.Errorf("drain with no artist row = %d, %v; want 0", n, err)
	}

	// An unreadable index fails the drain outright.
	dropTable(t, db, &models.LibraryItem{})
	if _, err := DetachManagerArtists(db, manager.ID); err == nil {
		t.Error("an unreadable index must fail the drain")
	}
}

// TestRepairFailsWhenManagersCannotBeRead: with ghosts to repair, an unreadable
// manager table is an error rather than an inert pass.
func TestRepairFailsWhenManagersCannotBeRead(t *testing.T) {
	db := testDB(t)
	ghostRow(t, db, "rg-ghost", "artist-1", nil)
	dropTable(t, db, &models.Manager{})
	if _, err := RepairGhostReleaseGroups(db); err == nil {
		t.Error("want an error")
	}
}

// TestMarkRepairAttemptedEdges: an artist with no ghosts stamps nothing, and a stamp
// that cannot be written is logged, not fatal.
func TestMarkRepairAttemptedEdges(t *testing.T) {
	db := testDB(t)
	ghostRow(t, db, "rg-ghost", "artist-1", nil)

	markRepairAttempted(db, "artist-2", []string{"rg-ghost"})
	if attemptStamped(t, db, "rg-ghost") {
		t.Error("another artist's pass stamped this ghost")
	}

	blockWrites(t, db, "musicbrainz_migrations", "UPDATE")
	markRepairAttempted(db, "artist-1", []string{"rg-ghost"})
	if attemptStamped(t, db, "rg-ghost") {
		t.Error("a blocked update still stamped the ghost")
	}
}

// TestTargetsFailWhenLibrariesCannotBeRead: a folder list without the libraries it
// sits in cannot be built, and saying so beats returning nothing to walk.
func TestTargetsFailWhenLibrariesCannotBeRead(t *testing.T) {
	db := testDB(t)
	_, artist := artistOnDisk(t, db, "/music", "/music/Band/Album (2020)/01.flac")
	var rg string
	if err := db.Model(&models.CollectionRelease{}).Limit(1).Pluck("release_group_mb_id", &rg).Error; err != nil || rg == "" {
		t.Fatalf("no owned edition seeded (%v)", err)
	}

	dropTable(t, db, &models.Library{})
	if _, err := ArtistTargets(db, artist); err == nil {
		t.Error("ArtistTargets must fail")
	}
	if _, err := ReleaseGroupTargets(db, rg); err == nil {
		t.Error("ReleaseGroupTargets must fail")
	}
}

// TestArtistReleaseMBIDsFailsOnEditions: the group lookup succeeding is not enough —
// an unreadable editions table is still an error.
func TestArtistReleaseMBIDsFailsOnEditions(t *testing.T) {
	db := testDB(t)
	storeGroup(t, db, "rg-1", "artist-1", nil)
	dropTable(t, db, &models.CollectionRelease{})
	if _, err := ArtistReleaseMBIDs(db, "artist-1"); err == nil {
		t.Error("want an error")
	}
}

// TestRebuildFailsOnAFailedArtistWrite: a rebuild that cannot write an artist row —
// new or existing — fails as a whole, and the transaction leaves the previous
// collection exactly as it was.
func TestRebuildFailsOnAFailedArtistWrite(t *testing.T) {
	for _, op := range []string{"UPDATE", "INSERT"} {
		t.Run(op, func(t *testing.T) {
			db, _ := managedCollection(t)
			if op == "INSERT" {
				if err := db.Where("mb_id = ?", "art-1").Delete(&models.CollectionArtist{}).Error; err != nil {
					t.Fatalf("delete artist: %v", err)
				}
			}
			blockWrites(t, db, "collection_artists", op)

			if _, err := Rebuild(db); err == nil {
				t.Fatal("a blocked artist write must fail the rebuild")
			}
			if !groupExists(t, db, "rg-1") || !ownedFlag(t, db, "rg-1").Owned {
				t.Error("the previous collection must survive a failed rebuild")
			}
		})
	}
}

// TestSyncReportsAFailedWantReconcile: the mirror landed, so the sync succeeds, but a
// want reconcile that could not write is a failure the event shows.
func TestSyncReportsAFailedWantReconcile(t *testing.T) {
	albums := []models.LidarrAlbum{monitoredAlbum("rg-1", "rel-1")}
	db := lidarrCollection(t, &albums)
	blockWrites(t, db, "collection_desires", "INSERT")

	stats, err := SyncLidarr(db)
	if err != nil {
		t.Fatalf("SyncLidarr: %v", err)
	}
	if len(stats.Failures) != 1 || !strings.HasPrefix(stats.Failures[0], "reconciling wants") {
		t.Errorf("failures = %v, want one reconciling-wants failure", stats.Failures)
	}
	if !catalogOf(t, db, "rg-1").InCatalog {
		t.Error("the mirror itself must still have landed")
	}
}

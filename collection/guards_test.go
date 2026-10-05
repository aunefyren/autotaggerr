package collection

import (
	"sync"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// The degenerate inputs every caller can hand these functions — no database, no ID —
// and a database that has gone away underneath them. None of these may panic, and a
// database failure has to come back as an error rather than as an empty answer that
// reads like "nothing there".

// closedDB is a migrated database whose connection has been closed, so every query
// fails.
func closedDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return db
}

// TestNilAndEmptyInputsAreInert: no database or no ID is an empty answer, not an error.
func TestNilAndEmptyInputsAreInert(t *testing.T) {
	db := testDB(t)

	if ids, err := ArtistReleaseMBIDs(nil, "a"); err != nil || ids != nil {
		t.Errorf("ArtistReleaseMBIDs(nil) = %v, %v", ids, err)
	}
	if ids, err := ArtistReleaseMBIDs(db, ""); err != nil || ids != nil {
		t.Errorf("ArtistReleaseMBIDs(\"\") = %v, %v", ids, err)
	}
	if ids, err := ReleaseGroupReleaseMBIDs(nil, "rg"); err != nil || ids != nil {
		t.Errorf("ReleaseGroupReleaseMBIDs(nil) = %v, %v", ids, err)
	}
	if ids, err := ReleaseGroupMBIDsForArtist(nil, "a"); err != nil || ids != nil {
		t.Errorf("ReleaseGroupMBIDsForArtist(nil) = %v, %v", ids, err)
	}
	if rgs, err := ReleaseGroupsForArtist(db, ""); err != nil || rgs != nil {
		t.Errorf("ReleaseGroupsForArtist(\"\") = %v, %v", rgs, err)
	}
	if err := BackfillReleaseGroupArtists(nil); err != nil {
		t.Errorf("BackfillReleaseGroupArtists(nil) = %v", err)
	}
	if err := BackfillDesireSources(nil); err != nil {
		t.Errorf("BackfillDesireSources(nil) = %v", err)
	}
	if counts, err := OwnedReleaseCounts(db, nil); err != nil || len(counts) != 0 {
		t.Errorf("OwnedReleaseCounts(nil ids) = %v, %v", counts, err)
	}
	if n, err := PruneOrphanReleaseGroups(nil, "a", live("rg")); err != nil || n != 0 {
		t.Errorf("PruneOrphanReleaseGroups(nil) = %d, %v", n, err)
	}
	if removed, _, err := RetireReleaseGroup(nil, "rg"); err != nil || removed {
		t.Errorf("RetireReleaseGroup(nil) = %v, %v", removed, err)
	}
	if ok, _, err := ReleaseGroupRetirable(nil, "rg"); err != nil || ok {
		t.Errorf("ReleaseGroupRetirable(nil) = %v, %v", ok, err)
	}
	if ghosts, err := GhostReleaseGroups(nil); err != nil || ghosts != nil {
		t.Errorf("GhostReleaseGroups(nil) = %v, %v", ghosts, err)
	}
	if n, err := pruneOrphanArtists(nil); err != nil || n != 0 {
		t.Errorf("pruneOrphanArtists(nil) = %d, %v", n, err)
	}
	if rels, arts, err := AllMBIDs(nil); err != nil || rels != nil || arts != nil {
		t.Errorf("AllMBIDs(nil) = %v, %v, %v", rels, arts, err)
	}
	if ghosts, err := ghostsForArtist(db, nil, "a"); err != nil || ghosts != nil {
		t.Errorf("ghostsForArtist(no ghosts) = %v, %v", ghosts, err)
	}
	if _, err := DetachArtist(db, ""); err == nil {
		t.Error("DetachArtist(\"\") must refuse")
	}
	if _, err := ReattachArtist(db, ""); err == nil {
		t.Error("ReattachArtist(\"\") must refuse")
	}
	if _, err := DetachArtist(db, "missing"); err == nil {
		t.Error("DetachArtist(unknown artist) must refuse")
	}
	if _, err := ReattachArtist(db, "missing"); err == nil {
		t.Error("ReattachArtist(unknown artist) must refuse")
	}

	// Writes that have nothing to write to stay silent.
	linkReleaseGroupArtists(nil, "rg", []string{"a"}, true, creditFromDisk)
	linkReleaseGroupArtists(db, "", []string{"a"}, true, creditFromDisk)
	if n := pruneReleaseGroupArtists(nil, "rg", []string{"a"}); n != 0 {
		t.Errorf("pruneReleaseGroupArtists(nil) = %d", n)
	}
	var none *creditChanges
	if none.total() != 0 {
		t.Error("a nil credit tally totals zero")
	}
}

// TestRetirableReportsTheBlock: the read-only twin of RetireReleaseGroup answers the
// same question without deleting anything, so the UI can offer the action only where
// it would succeed.
func TestRetirableReportsTheBlock(t *testing.T) {
	db := testDB(t)
	storeGroup(t, db, "rg-free", "artist-1", nil)
	storeGroup(t, db, "rg-listed", "artist-1", func(rg *models.CollectionReleaseGroup) {
		rg.InCatalog = true
	})

	ok, reason, err := ReleaseGroupRetirable(db, "rg-free")
	if err != nil || !ok || reason != "" {
		t.Errorf("unclaimed group: ok=%v reason=%q err=%v, want retirable", ok, reason, err)
	}
	ok, reason, err = ReleaseGroupRetirable(db, "rg-listed")
	if err != nil || ok || reason == "" {
		t.Errorf("listed group: ok=%v reason=%q err=%v, want blocked with a reason", ok, reason, err)
	}
	// An absent row has nothing blocking it, so a retry may stop carrying it as failed.
	if ok, _, err := ReleaseGroupRetirable(db, "rg-missing"); err != nil || !ok {
		t.Errorf("absent group: ok=%v err=%v, want retirable", ok, err)
	}
	if !groupExists(t, db, "rg-free") {
		t.Error("asking must not retire")
	}
}

// TestClosedDatabaseIsAnError: every read and write surfaces the failure instead of
// returning an empty result that looks like an empty collection.
func TestClosedDatabaseIsAnError(t *testing.T) {
	db := closedDB(t)

	errs := map[string]error{}
	record := func(name string, err error) { errs[name] = err }

	_, err := ArtistReleaseMBIDs(db, "a")
	record("ArtistReleaseMBIDs", err)
	_, err = ArtistItems(db, "a")
	record("ArtistItems", err)
	_, err = ArtistItemIDs(db, "a")
	record("ArtistItemIDs", err)
	_, err = ReleaseGroupReleaseMBIDs(db, "rg")
	record("ReleaseGroupReleaseMBIDs", err)
	_, err = ReleaseGroupItems(db, "rg")
	record("ReleaseGroupItems", err)
	_, err = ReleaseGroupTargets(db, "rg")
	record("ReleaseGroupTargets", err)
	_, err = ArtistTargets(db, "a")
	record("ArtistTargets", err)
	_, err = ReleaseGroupMBIDsForArtist(db, "a")
	record("ReleaseGroupMBIDsForArtist", err)
	_, err = ReleaseGroupsForArtist(db, "a")
	record("ReleaseGroupsForArtist", err)
	_, err = ArtistsByReleaseGroup(db, []string{"rg"})
	record("ArtistsByReleaseGroup", err)
	record("BackfillReleaseGroupArtists", BackfillReleaseGroupArtists(db))
	_, err = AddArtist(db, "a", "A")
	record("AddArtist", err)
	_, err = OwnedReleases(db, "rg")
	record("OwnedReleases", err)
	_, err = OwnedReleaseCounts(db, []string{"rg"})
	record("OwnedReleaseCounts", err)
	_, err = SetDesire(db, DesireInput{ArtistMBID: "a", ReleaseGroupMBID: "rg"})
	record("SetDesire(any)", err)
	_, err = SetDesire(db, DesireInput{ArtistMBID: "a", ReleaseGroupMBID: "rg", ReleaseMBID: "rel"})
	record("SetDesire(edition)", err)
	record("BackfillDesireSources", BackfillDesireSources(db))
	record("ClearDesire", ClearDesire(db, "rg", ""))
	_, err = DesiresForArtist(db, "a", []string{"rg"})
	record("DesiresForArtist", err)
	_, err = DetachArtist(db, "a")
	record("DetachArtist", err)
	_, err = ReattachArtist(db, "a")
	record("ReattachArtist", err)
	_, err = DetachManagerArtists(db, uuid.New())
	record("DetachManagerArtists", err)
	_, err = PruneOrphanReleaseGroups(db, "a", live("rg"))
	record("PruneOrphanReleaseGroups", err)
	_, err = GhostReleaseGroups(db)
	record("GhostReleaseGroups", err)
	_, err = pruneOrphanArtists(db)
	record("pruneOrphanArtists", err)
	_, _, err = AllMBIDs(db)
	record("AllMBIDs", err)
	_, err = RepairGhostReleaseGroups(db)
	record("RepairGhostReleaseGroups", err)
	_, err = ArtistIdentityEditable(db, "a")
	record("ArtistIdentityEditable", err)
	_, err = SyncLidarr(db)
	record("SyncLidarr", err)
	_, err = SyncArtist(db, fakeMeta{}, "a")
	record("SyncArtist", err)
	_, err = Rebuild(db)
	record("Rebuild", err)
	_, err = RecordScan(db, "Scan", RebuildScope{}, map[string]any{"by": "test"})
	record("RecordScan", err)
	_, err = artistsHoldingGhosts(db, []string{"rg"})
	record("artistsHoldingGhosts", err)
	_, err = libraryManagerTypes(db)
	record("libraryManagerTypes", err)
	_, err = deriveArtistManager(db, "a")
	record("deriveArtistManager", err)
	_, err = ownedItemRows(db)
	record("ownedItemRows", err)
	_, err = isOrphanReleaseGroup(db, "a", "rg")
	record("isOrphanReleaseGroup", err)
	_, err = isOrphanArtist(db, "a")
	record("isOrphanArtist", err)
	_, err = resolveBounds(db, RebuildScope{ArtistMBID: "a"})
	record("resolveBounds", err)
	_, err = syncEmptyReason(db, SyncOptions{})
	record("syncEmptyReason", err)
	record("reconcileManagerDesires", reconcileManagerDesires(db))

	for name, err := range errs {
		if err == nil {
			t.Errorf("%s on a closed database returned no error", name)
		}
	}

	// The fire-and-forget writes log and carry on.
	linkReleaseGroupArtists(db, "rg", []string{"a"}, true, creditFromDisk)
	if n := pruneReleaseGroupArtists(db, "rg", []string{"a"}); n != 0 {
		t.Errorf("pruneReleaseGroupArtists on a closed database = %d, want 0", n)
	}
	markRepairAttempted(db, "a", []string{"rg"})
	clearCatalogView(db, "a", "A")
	if inCooldown(db, "a") {
		t.Error("an unreadable cooldown must not suppress a repair")
	}
}

// recordingWarmer captures what the artwork hook was asked to warm.
type recordingWarmer struct {
	mu      sync.Mutex
	artists []string
	groups  []string
}

func (w *recordingWarmer) Warm(artists, groups []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.artists = append(w.artists, artists...)
	w.groups = append(w.groups, groups...)
}

// TestArtworkWarmedOnCreate: a newly created artist or release-group row queues its
// artwork, so the collection page does not open onto blank tiles. Rows that already
// existed are not re-queued.
func TestArtworkWarmedOnCreate(t *testing.T) {
	w := &recordingWarmer{}
	SetArtworkWarmer(w)
	t.Cleanup(func() { SetArtworkWarmer(nil) })

	db := testDB(t)
	if _, err := AddArtist(db, "art-1", "Band"); err != nil {
		t.Fatalf("AddArtist: %v", err)
	}
	if _, err := SetDesire(db, DesireInput{ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", Title: "One"}); err != nil {
		t.Fatalf("SetDesire: %v", err)
	}
	// A second want on the same group writes no new row.
	if _, err := SetDesire(db, DesireInput{ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", ReleaseMBID: "rel-1"}); err != nil {
		t.Fatalf("SetDesire: %v", err)
	}
	warmArtistArtwork("")
	warmGroupArtwork("")

	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.artists) != 1 || w.artists[0] != "art-1" {
		t.Errorf("artists warmed = %v, want [art-1]", w.artists)
	}
	if len(w.groups) != 1 || w.groups[0] != "rg-1" {
		t.Errorf("groups warmed = %v, want [rg-1]", w.groups)
	}
}

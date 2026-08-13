package migration

import (
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
)

// The queue is not the only thing that settles an identity change: a manager re-keys an
// album, a prune takes a row, a scan re-correlates the last file. What is left then is a
// migration describing a change to state nobody holds, and applying it would report a
// rewrite of nothing as an application.

func TestPendingAlbumWithNoRowClosesItself(t *testing.T) {
	db := testDB(t)
	// No collection_release_groups row: the album the manager was holding a dead ID for
	// has already gone, which is what a successful repair looks like from here.
	m := pendingDeletion(t, db, models.MigrationEntityReleaseGroup, "rg-gone")

	res, err := ProcessPending(db, Policy{})
	if err != nil {
		t.Fatalf("ProcessPending: %v", err)
	}
	if res.Resolved != 1 || res.Applied != 0 || res.Retired != 0 {
		t.Errorf("resolved/applied/retired = %d/%d/%d, want 1/0/0", res.Resolved, res.Applied, res.Retired)
	}

	row := reload(t, db, m.ID)
	if row.Status != models.MigrationStatusResolved {
		t.Errorf("status = %q, want resolved", row.Status)
	}
	if row.Resolution != models.MigrationResolutionExternal {
		t.Errorf("resolution = %q, want external", row.Resolution)
	}
	if row.ResolutionDetail == "" || row.ResolvedAt == nil {
		t.Errorf("a row closed without applying must say why and when: %+v", row)
	}

	// And it reports itself by name, so the event can list what settled rather than
	// only how many did.
	if len(res.Outcomes) != 1 || res.Outcomes[0].Status != models.EventItemStatusResolved {
		t.Errorf("outcomes = %+v, want one resolved row", res.Outcomes)
	}
}

// The counterpart: an album still in the collection is a real retirement, and the guards
// still decide whether it happens.
func TestPendingAlbumWithARowIsNotClosed(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionReleaseGroup{
		MBID: "rg-1", Title: "Ghost", ArtistMBID: "art-1", InCatalog: true,
	}).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	m := pendingDeletion(t, db, models.MigrationEntityReleaseGroup, "rg-1")
	// Held until the manager has been asked; stamping that is what lets the drain act.
	now := time.Now()
	m.RepairAttemptedAt = &now
	if err := db.Save(&m).Error; err != nil {
		t.Fatalf("save: %v", err)
	}

	res, err := ProcessPending(db, Policy{})
	if err != nil {
		t.Fatalf("ProcessPending: %v", err)
	}
	if res.Resolved != 0 {
		t.Errorf("resolved = %d, want 0: the album is still in the collection", res.Resolved)
	}
	// The manager still lists it, so the retirement is refused rather than performed —
	// and the refusal is the sentence a person acts on.
	row := reload(t, db, m.ID)
	if row.Status != models.MigrationStatusFailed || row.Error == "" {
		t.Errorf("status/error = %q/%q, want a failure that says why", row.Status, row.Error)
	}
}

// A merge nothing points at any more is the same case as the album above: the files were
// re-correlated elsewhere, so there is nothing left to re-point.
func TestUnreferencedReleaseMergeClosesItself(t *testing.T) {
	db := testDB(t)
	m := pendingRedirect(t, db, models.MigrationEntityRelease, "rel-old", "rel-new")

	res, err := ProcessPending(db, Policy{})
	if err != nil {
		t.Fatalf("ProcessPending: %v", err)
	}
	if res.Resolved != 1 || res.Applied != 0 {
		t.Errorf("resolved/applied = %d/%d, want 1/0", res.Resolved, res.Applied)
	}
	if reload(t, db, m.ID).Status != models.MigrationStatusResolved {
		t.Error("an unreferenced merge was not closed")
	}
}

// Malformed is not moot. A redirect with nowhere to point is a data problem, and closing
// it quietly because nothing happens to reference it today would file it under "resolved
// itself" instead of leaving the row saying what is wrong with it.
func TestTargetlessRedirectIsNotClosed(t *testing.T) {
	db := testDB(t)
	m := pendingRedirect(t, db, models.MigrationEntityRelease, "rel-old", "")

	if _, err := ProcessPending(db, Policy{}); err != nil {
		t.Fatalf("ProcessPending: %v", err)
	}
	row := reload(t, db, m.ID)
	if row.Status != models.MigrationStatusFailed {
		t.Errorf("status = %q, want failed", row.Status)
	}
}

// Approving records who decided, so the history can tell a person's decision from an
// unattended run's.
func TestApprovalRecordsItsResolution(t *testing.T) {
	db := testDB(t)
	lib := newLibrary(t, db)
	if err := db.Create(&models.LibraryItem{
		LibraryID: lib.ID, Path: "/m/a.flac", Status: models.LibraryItemStatusOK, MBReleaseID: "rel-old",
	}).Error; err != nil {
		t.Fatalf("create item: %v", err)
	}
	m := pendingRedirect(t, db, models.MigrationEntityRelease, "rel-old", "rel-new")

	if _, err := ApplyByID(db, m.ID); err != nil {
		t.Fatalf("ApplyByID: %v", err)
	}
	row := reload(t, db, m.ID)
	if row.Resolution != models.MigrationResolutionApproved || row.ResolvedAt == nil {
		t.Errorf("resolution/resolvedAt = %q/%v, want approved and stamped", row.Resolution, row.ResolvedAt)
	}
}

// A dismissal used to record nothing but the status, so the history showed an empty
// timestamp column and could not be ordered by when anything happened.
func TestDismissalIsStamped(t *testing.T) {
	db := testDB(t)
	m := pendingRedirect(t, db, models.MigrationEntityRelease, "rel-old", "rel-new")

	if _, err := Dismiss(db, m.ID); err != nil {
		t.Fatalf("Dismiss: %v", err)
	}
	row := reload(t, db, m.ID)
	if row.Resolution != models.MigrationResolutionDismissed || row.ResolvedAt == nil {
		t.Errorf("resolution/resolvedAt = %q/%v, want dismissed and stamped", row.Resolution, row.ResolvedAt)
	}
}

// --- paging and ordering --------------------------------------------------

func TestListPagesAndCounts(t *testing.T) {
	db := testDB(t)
	for _, id := range []string{"rel-1", "rel-2", "rel-3"} {
		pendingRedirect(t, db, models.MigrationEntityRelease, id, id+"-new")
	}

	page, total, err := List(db, ListOptions{Status: StatusOpen, Limit: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page) != 2 || total != 3 {
		t.Errorf("page/total = %d/%d, want 2/3", len(page), total)
	}

	rest, _, err := List(db, ListOptions{Status: StatusOpen, Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("List(offset): %v", err)
	}
	if len(rest) != 1 {
		t.Errorf("second page = %d rows, want 1", len(rest))
	}
}

// The queue and the history are two lists, and a failed row belongs with the work rather
// than with the things that are over.
func TestOpenAndClosedSplit(t *testing.T) {
	db := testDB(t)
	pendingRedirect(t, db, models.MigrationEntityRelease, "rel-open", "rel-new")
	failed := pendingRedirect(t, db, models.MigrationEntityArtist, "art-failed", "art-new")
	failed.Status = models.MigrationStatusFailed
	if err := db.Save(&failed).Error; err != nil {
		t.Fatalf("save: %v", err)
	}
	done := pendingRedirect(t, db, models.MigrationEntityRelease, "rel-done", "rel-newer")
	if _, err := Dismiss(db, done.ID); err != nil {
		t.Fatalf("Dismiss: %v", err)
	}

	open, openTotal, err := List(db, ListOptions{Status: StatusOpen})
	if err != nil {
		t.Fatalf("List(open): %v", err)
	}
	if openTotal != 2 || len(open) != 2 {
		t.Errorf("open = %d rows (total %d), want 2", len(open), openTotal)
	}

	closed, closedTotal, err := List(db, ListOptions{Status: StatusClosed})
	if err != nil {
		t.Fatalf("List(closed): %v", err)
	}
	if closedTotal != 1 || closed[0].OldMBID != "rel-done" {
		t.Errorf("closed = %+v (total %d), want just rel-done", closed, closedTotal)
	}
}

// History is ordered by when a row was settled, not by when it was detected. Detection
// order follows the order a sweep walks artists in, which is why an unordered history
// read as alphabetical.
func TestHistorySortsByResolutionTime(t *testing.T) {
	db := testDB(t)
	first := pendingRedirect(t, db, models.MigrationEntityRelease, "rel-a", "rel-a2")
	second := pendingRedirect(t, db, models.MigrationEntityRelease, "rel-b", "rel-b2")

	// Detected in one order, settled in the other.
	if _, err := Dismiss(db, second.ID); err != nil {
		t.Fatalf("Dismiss: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := Dismiss(db, first.ID); err != nil {
		t.Fatalf("Dismiss: %v", err)
	}

	rows, _, err := List(db, ListOptions{Status: StatusClosed})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 2 || rows[0].OldMBID != "rel-a" {
		t.Errorf("history order = %v, want the most recently settled first", ids(rows))
	}
}

func TestSearchMatchesNameOrIdentifier(t *testing.T) {
	db := testDB(t)
	m := pendingRedirect(t, db, models.MigrationEntityRelease, "rel-old", "rel-new")
	m.Name = "Kid A"
	if err := db.Save(&m).Error; err != nil {
		t.Fatalf("save: %v", err)
	}
	pendingRedirect(t, db, models.MigrationEntityRelease, "other", "other-new")

	byName, _, err := List(db, ListOptions{Query: "kid"})
	if err != nil {
		t.Fatalf("List(name): %v", err)
	}
	if len(byName) != 1 || byName[0].OldMBID != "rel-old" {
		t.Errorf("name search = %v, want rel-old", ids(byName))
	}

	// The other half of why anyone opens this page: a UUID pasted out of a log.
	byID, _, err := List(db, ListOptions{Query: "rel-old"})
	if err != nil {
		t.Fatalf("List(id): %v", err)
	}
	if len(byID) != 1 {
		t.Errorf("id search = %v, want one row", ids(byID))
	}
}

// --- the in-flight mark ---------------------------------------------------

// One refresh answers for every album of an artist's, so every open row of theirs shows
// the work — not only the one that was pressed.
func TestRepairMarkCoversEveryAlbumOfTheArtist(t *testing.T) {
	db := testDB(t)
	for _, id := range []string{"rg-1", "rg-2"} {
		if err := db.Create(&models.CollectionReleaseGroup{
			MBID: id, Title: id, ArtistMBID: "art-1", InCatalog: true,
		}).Error; err != nil {
			t.Fatalf("create group: %v", err)
		}
		pendingDeletion(t, db, models.MigrationEntityReleaseGroup, id)
	}
	// A third album, someone else's, must not be marked.
	if err := db.Create(&models.CollectionReleaseGroup{
		MBID: "rg-3", Title: "rg-3", ArtistMBID: "art-2", InCatalog: true,
	}).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	pendingDeletion(t, db, models.MigrationEntityReleaseGroup, "rg-3")

	n, err := MarkRepairQueued(db, "art-1")
	if err != nil {
		t.Fatalf("MarkRepairQueued: %v", err)
	}
	if n != 2 {
		t.Errorf("marked %d rows, want 2", n)
	}
	if marked(t, db, "rg-3") {
		t.Error("another artist's album was marked as repairing")
	}

	if err := ClearRepairQueued(db, "art-1"); err != nil {
		t.Fatalf("ClearRepairQueued: %v", err)
	}
	if marked(t, db, "rg-1") {
		t.Error("the mark survived the job it belonged to")
	}
}

// Nothing is running at startup, so a surviving mark can only be a row claiming work is
// happening that is not.
func TestReconcileQueuedClearsStaleMarks(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionReleaseGroup{MBID: "rg-1", ArtistMBID: "art-1"}).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	pendingDeletion(t, db, models.MigrationEntityReleaseGroup, "rg-1")
	if _, err := MarkRepairQueued(db, "art-1"); err != nil {
		t.Fatalf("MarkRepairQueued: %v", err)
	}

	ReconcileQueued(db)
	if marked(t, db, "rg-1") {
		t.Error("a mark left by a dead process survived startup")
	}
}

// --- helpers --------------------------------------------------------------

func reload(t *testing.T, db *gorm.DB, id any) models.MusicbrainzMigration {
	t.Helper()
	var row models.MusicbrainzMigration
	if err := db.First(&row, "id = ?", id).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	return row
}

func marked(t *testing.T, db *gorm.DB, oldMBID string) bool {
	t.Helper()
	var row models.MusicbrainzMigration
	if err := db.Where("old_mb_id = ?", oldMBID).First(&row).Error; err != nil {
		t.Fatalf("find %s: %v", oldMBID, err)
	}
	return row.RepairQueuedAt != nil
}

func ids(rows []models.MusicbrainzMigration) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.OldMBID)
	}
	return out
}

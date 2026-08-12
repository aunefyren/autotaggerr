package migration

import (
	"strings"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
)

func storeGroup(t *testing.T, db *gorm.DB, mbID, artistMBID, title string, mutate func(*models.CollectionReleaseGroup)) {
	t.Helper()
	rg := models.CollectionReleaseGroup{MBID: mbID, ArtistMBID: artistMBID, Title: title}
	if mutate != nil {
		mutate(&rg)
	}
	if err := db.Create(&rg).Error; err != nil {
		t.Fatalf("create release-group: %v", err)
	}
	if err := db.Create(&models.CollectionReleaseGroupArtist{
		ReleaseGroupMBID: mbID, ArtistMBID: artistMBID,
	}).Error; err != nil {
		t.Fatalf("create credit link: %v", err)
	}
}

func groupExists(t *testing.T, db *gorm.DB, mbID string) bool {
	t.Helper()
	var n int64
	if err := db.Model(&models.CollectionReleaseGroup{}).Where("mb_id = ?", mbID).Count(&n).Error; err != nil {
		t.Fatalf("count release-groups: %v", err)
	}
	return n > 0
}

// TestReleaseGroupDeletionHeldUntilRepairTried: the deliberate break from the
// zero-value-means-apply convention, and its expiry date.
//
// A release-group ID that resolves nowhere is usually a manager holding a stale key
// for an album MusicBrainz still has under a different ID. Auto-applying that removes
// a repairable album unattended, so it is held whatever the policy says — including
// the all-false policy an old config.json decodes to. Once the manager has been asked
// to refresh and the ID still does not resolve, the objection is spent.
func TestReleaseGroupDeletionHeldUntilRepairTried(t *testing.T) {
	m := models.MusicbrainzMigration{
		EntityType: models.MigrationEntityReleaseGroup,
		Kind:       models.MigrationKindDeleted,
	}
	for _, p := range []Policy{
		{},
		{ReviewDeletions: false, ReviewReleases: false, ReviewArtists: false, ReviewPinned: false},
	} {
		if !p.heldForReview(m) {
			t.Fatalf("policy %+v must hold an un-repaired release-group deletion", p)
		}
	}

	attempted := time.Now()
	m.RepairAttemptedAt = &attempted
	if (Policy{}).heldForReview(m) {
		t.Error("once the manager has been asked, the deletion must stop being held")
	}
	// The pinned override still outranks it: a manual correlation is a human decision
	// and a manager refresh is not an answer to it.
	m.TouchesPinned = true
	if !(Policy{ReviewPinned: true}).heldForReview(m) {
		t.Error("ReviewPinned must still hold a repaired-but-pinned row")
	}

	// The neighbouring category is untouched: a *release* deletion still follows the
	// convention, so this is a carve-out rather than a change of default.
	release := models.MusicbrainzMigration{
		EntityType: models.MigrationEntityRelease,
		Kind:       models.MigrationKindDeleted,
	}
	if (Policy{}).heldForReview(release) {
		t.Error("a release deletion must still auto-apply under the zero policy")
	}
}

// TestProcessPendingHoldsReleaseGroups: the policy rule reaching the drain. A pass
// that measured the row must leave it pending rather than applying it.
func TestProcessPendingHoldsReleaseGroups(t *testing.T) {
	db := testDB(t)
	storeGroup(t, db, "rg-ghost", "artist-1", "Ghost Album", nil)
	pendingDeletion(t, db, models.MigrationEntityReleaseGroup, "rg-ghost")

	res, err := ProcessPending(db, Policy{})
	if err != nil {
		t.Fatalf("ProcessPending: %v", err)
	}
	if res.Applied != 0 {
		t.Errorf("Applied = %d, want 0 — release-group deletions are held", res.Applied)
	}
	if res.Pending != 1 {
		t.Errorf("Pending = %d, want 1", res.Pending)
	}

	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", "rg-ghost").First(&rg).Error; err != nil {
		t.Fatal("the album was removed without approval")
	}
}

// TestProcessPendingNamesReleaseGroups: the title is captured while the row that knows
// it still exists, or the review queue is a list of bare UUIDs by the time anyone
// looks — and applying the migration destroys the only source of the name.
func TestProcessPendingNamesReleaseGroups(t *testing.T) {
	db := testDB(t)
	storeGroup(t, db, "rg-ghost", "artist-1", "DeBÍ TiRAR MáS FOToS", nil)
	pendingDeletion(t, db, models.MigrationEntityReleaseGroup, "rg-ghost")

	if _, err := ProcessPending(db, Policy{}); err != nil {
		t.Fatalf("ProcessPending: %v", err)
	}

	var m models.MusicbrainzMigration
	if err := db.Where("old_mb_id = ?", "rg-ghost").First(&m).Error; err != nil {
		t.Fatalf("load migration: %v", err)
	}
	if m.Name != "DeBÍ TiRAR MáS FOToS" {
		t.Errorf("Name = %q, want the album title", m.Name)
	}
}

// TestApproveRetiresReleaseGroup: approving is the decision the hold was waiting for,
// and it removes the album.
func TestApproveRetiresReleaseGroup(t *testing.T) {
	db := testDB(t)
	storeGroup(t, db, "rg-ghost", "artist-1", "Ghost Album", nil)
	m := pendingDeletion(t, db, models.MigrationEntityReleaseGroup, "rg-ghost")

	row, err := ApplyByID(db, m.ID)
	if err != nil {
		t.Fatalf("ApplyByID: %v", err)
	}
	if row.Status != models.MigrationStatusApplied {
		t.Errorf("status = %q, want applied", row.Status)
	}

	var n int64
	if err := db.Model(&models.CollectionReleaseGroup{}).
		Where("mb_id = ?", "rg-ghost").Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Error("the album is still in the collection after approval")
	}
}

// TestApproveRefusalRecordsReason: a guard blocking the retirement is a real outcome,
// not a transient failure. The row must carry the sentence so the person who approved
// it can read why nothing happened, rather than the approval appearing to succeed.
func TestApproveRefusalRecordsReason(t *testing.T) {
	db := testDB(t)
	storeGroup(t, db, "rg-owned", "artist-1", "Owned Album", func(rg *models.CollectionReleaseGroup) {
		rg.Owned = true
	})
	m := pendingDeletion(t, db, models.MigrationEntityReleaseGroup, "rg-owned")

	if _, err := ApplyByID(db, m.ID); err == nil {
		t.Fatal("want an error when a guard refuses the retirement")
	}

	var row models.MusicbrainzMigration
	if err := db.First(&row, "id = ?", m.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if row.Status != models.MigrationStatusFailed {
		t.Errorf("status = %q, want failed", row.Status)
	}
	if !strings.Contains(row.Error, "files on disk") {
		t.Errorf("Error = %q, want it to name the guard that refused", row.Error)
	}
	var n int64
	db.Model(&models.CollectionReleaseGroup{}).Where("mb_id = ?", "rg-owned").Count(&n)
	if n != 1 {
		t.Error("the album was removed despite the refusal")
	}
}

// TestReleaseGroupDesiresCounted: the impact snapshot has to look at the right column,
// or a want that will block the retirement is invisible in the review UI.
func TestReleaseGroupDesiresCounted(t *testing.T) {
	db := testDB(t)
	storeGroup(t, db, "rg-wanted", "artist-1", "Wanted Album", nil)
	if err := db.Create(&models.CollectionDesire{
		ArtistMBID: "artist-1", ReleaseGroupMBID: "rg-wanted",
	}).Error; err != nil {
		t.Fatalf("create desire: %v", err)
	}
	pendingDeletion(t, db, models.MigrationEntityReleaseGroup, "rg-wanted")

	if _, err := ProcessPending(db, Policy{}); err != nil {
		t.Fatalf("ProcessPending: %v", err)
	}

	var m models.MusicbrainzMigration
	if err := db.Where("old_mb_id = ?", "rg-wanted").First(&m).Error; err != nil {
		t.Fatalf("load migration: %v", err)
	}
	if m.AffectedDesires != 1 {
		t.Errorf("AffectedDesires = %d, want 1", m.AffectedDesires)
	}
}

// TestFailedRetirementRetriedWhenUnblocked: the gap this closes. A retirement refused
// because the manager still listed the album must be re-attempted once a refresh has
// dropped it — not left failed forever waiting on a discography prune or a human.
func TestFailedRetirementRetriedWhenUnblocked(t *testing.T) {
	db := testDB(t)
	storeGroup(t, db, "rg-ghost", "artist-1", "Ghost Album", func(rg *models.CollectionReleaseGroup) {
		rg.InCatalog = true
	})
	m := pendingDeletion(t, db, models.MigrationEntityReleaseGroup, "rg-ghost")
	// The repair pass has run, so policy no longer holds this row.
	attempted := time.Now()
	if err := db.Model(&m).Update("repair_attempted_at", attempted).Error; err != nil {
		t.Fatalf("stamp repair: %v", err)
	}

	// First drain: the manager still lists it, so the retirement is refused.
	if _, err := ProcessPending(db, Policy{}); err != nil {
		t.Fatalf("ProcessPending: %v", err)
	}
	var row models.MusicbrainzMigration
	if err := db.First(&row, "id = ?", m.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if row.Status != models.MigrationStatusFailed {
		t.Fatalf("status = %q, want failed on the first pass", row.Status)
	}
	if !groupExists(t, db, "rg-ghost") {
		t.Fatal("the album was removed even though the manager still listed it")
	}

	// A manager refresh drops the album: the catalog flag clears.
	if err := db.Model(&models.CollectionReleaseGroup{}).
		Where("mb_id = ?", "rg-ghost").Update("in_catalog", false).Error; err != nil {
		t.Fatalf("clear in_catalog: %v", err)
	}

	// Second drain: the blocker is gone, so the failed row is picked up and applied.
	res, err := ProcessPending(db, Policy{})
	if err != nil {
		t.Fatalf("ProcessPending: %v", err)
	}
	if res.Applied != 1 || res.Retired != 1 {
		t.Errorf("Applied=%d Retired=%d, want 1 and 1", res.Applied, res.Retired)
	}
	if err := db.First(&row, "id = ?", m.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if row.Status != models.MigrationStatusApplied {
		t.Errorf("status = %q, want applied after the blocker cleared", row.Status)
	}
	if row.Error != "" {
		t.Errorf("the stale refusal must be cleared, got %q", row.Error)
	}
	if groupExists(t, db, "rg-ghost") {
		t.Error("the album is still in the collection")
	}
}

// TestFailedRetirementNotRetriedWhileBlocked: the other half. A row whose blocker has
// not cleared must be left exactly as it is — no counter movement, no rewritten error,
// no churn on every nightly run.
func TestFailedRetirementNotRetriedWhileBlocked(t *testing.T) {
	db := testDB(t)
	storeGroup(t, db, "rg-owned", "artist-1", "Owned Album", func(rg *models.CollectionReleaseGroup) {
		rg.Owned = true
	})
	m := pendingDeletion(t, db, models.MigrationEntityReleaseGroup, "rg-owned")
	attempted := time.Now()
	if err := db.Model(&m).Update("repair_attempted_at", attempted).Error; err != nil {
		t.Fatalf("stamp repair: %v", err)
	}

	if _, err := ProcessPending(db, Policy{}); err != nil {
		t.Fatalf("ProcessPending: %v", err)
	}
	var first models.MusicbrainzMigration
	if err := db.First(&first, "id = ?", m.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if first.Status != models.MigrationStatusFailed {
		t.Fatalf("status = %q, want failed", first.Status)
	}

	res, err := ProcessPending(db, Policy{})
	if err != nil {
		t.Fatalf("ProcessPending: %v", err)
	}
	if res.Failed != 0 || res.Applied != 0 {
		t.Errorf("a still-blocked row must not move the counters, got Failed=%d Applied=%d",
			res.Failed, res.Applied)
	}
	var second models.MusicbrainzMigration
	if err := db.First(&second, "id = ?", m.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if second.Error != first.Error {
		t.Errorf("the refusal was rewritten: %q -> %q", first.Error, second.Error)
	}
	if !groupExists(t, db, "rg-owned") {
		t.Error("the owned album was removed")
	}
}

// TestFailedReleaseRedirectNotRetried: the retry is scoped to retirements. A redirect
// that failed did so for a reason a retry cannot change, and re-attempting it every run
// would be pure churn.
func TestFailedReleaseRedirectNotRetried(t *testing.T) {
	db := testDB(t)
	m := models.MusicbrainzMigration{
		EntityType: models.MigrationEntityRelease,
		OldMBID:    "rel-dead",
		Kind:       models.MigrationKindRedirect,
		Status:     models.MigrationStatusFailed,
		Error:      "redirect has no target MBID",
	}
	if err := db.Create(&m).Error; err != nil {
		t.Fatalf("create migration: %v", err)
	}

	res, err := ProcessPending(db, Policy{})
	if err != nil {
		t.Fatalf("ProcessPending: %v", err)
	}
	if res.Applied != 0 || res.Failed != 0 || res.Pending != 0 {
		t.Errorf("a failed redirect must not be re-picked, got %+v", res)
	}
}

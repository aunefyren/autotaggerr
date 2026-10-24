package migration

import (
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
)

func rekeyMigration(t *testing.T, db *gorm.DB, old, new string) models.MusicbrainzMigration {
	t.Helper()
	m := models.MusicbrainzMigration{
		EntityType: models.MigrationEntityReleaseGroup, OldMBID: old, NewMBID: new,
		Kind: models.MigrationKindRedirect, Status: models.MigrationStatusPending,
		Source: models.ManagerTypeLidarr, Name: "Debut",
	}
	if err := db.Create(&m).Error; err != nil {
		t.Fatalf("create migration: %v", err)
	}
	return m
}

func wantFor(t *testing.T, db *gorm.DB, artist, group, source string) {
	t.Helper()
	if err := db.Create(&models.CollectionDesire{ArtistMBID: artist, ReleaseGroupMBID: group, Source: source}).Error; err != nil {
		t.Fatalf("create desire: %v", err)
	}
}

// The whole point of a re-key migration: the want follows the album to its new ID,
// and the old row — which would otherwise sit beside the new one forever — goes.
func TestApplyReleaseGroupRedirectCarriesWantsAndRetiresTheOldRow(t *testing.T) {
	db := testDB(t)
	storeGroup(t, db, "rg-old", "art-1", "Debut", nil)
	storeGroup(t, db, "rg-new", "art-1", "Debut", func(rg *models.CollectionReleaseGroup) { rg.InCatalog = true })
	wantFor(t, db, "art-1", "rg-old", models.DesireSourceManual)
	// Already wanted under the new ID too: the two collapse into one.
	wantFor(t, db, "art-1", "rg-new", models.DesireSourceManager)
	m := rekeyMigration(t, db, "rg-old", "rg-new")

	res, err := ProcessPending(db, Policy{})
	if err != nil {
		t.Fatalf("ProcessPending: %v", err)
	}
	if res.Applied != 1 {
		t.Fatalf("result = %+v, want one applied", res)
	}
	if groupExists(t, db, "rg-old") {
		t.Error("the old row was not retired")
	}
	var desires []models.CollectionDesire
	db.Find(&desires)
	if len(desires) != 1 || desires[0].ReleaseGroupMBID != "rg-new" || desires[0].Source != models.DesireSourceManual {
		t.Errorf("desires = %+v, want one hand-authored want on rg-new", desires)
	}
	got := reload(t, db, m.ID)
	if got.Status != models.MigrationStatusApplied || !strings.Contains(got.ResolutionDetail, "Lidarr moved this album") ||
		!strings.Contains(got.ResolutionDetail, "1 want followed it") {
		t.Errorf("row = %s / %q", got.Status, got.ResolutionDetail)
	}
}

// Files still resolving to the old album (their tags carry the old ID until the next
// run re-correlates them) hold the whole migration — the want included — and it goes
// through on a later pass once they have moved, rather than staying failed.
func TestReleaseGroupRedirectWaitsForFilesThenApplies(t *testing.T) {
	db := testDB(t)
	storeGroup(t, db, "rg-old", "art-1", "Debut", func(rg *models.CollectionReleaseGroup) { rg.Owned = true })
	wantFor(t, db, "art-1", "rg-old", models.DesireSourceManual)
	m := rekeyMigration(t, db, "rg-old", "rg-new")

	if res, _ := ProcessPending(db, Policy{}); res.Failed != 1 {
		t.Fatalf("first pass = %+v, want the retirement refused", res)
	}
	var d models.CollectionDesire
	db.First(&d)
	if d.ReleaseGroupMBID != "rg-old" {
		t.Fatalf("want moved to %s by a migration that did not apply", d.ReleaseGroupMBID)
	}

	// The next run re-correlated the files through the manager: nothing owns the old row.
	if err := db.Model(&models.CollectionReleaseGroup{}).Where("mb_id = ?", "rg-old").Update("owned", false).Error; err != nil {
		t.Fatal(err)
	}
	if res, _ := ProcessPending(db, Policy{}); res.Applied != 1 {
		t.Fatalf("second pass = %+v, want it applied — the want must not hold it", res)
	}
	db.First(&d)
	if d.ReleaseGroupMBID != "rg-new" || groupExists(t, db, "rg-old") {
		t.Errorf("want on %s, old row exists %t", d.ReleaseGroupMBID, groupExists(t, db, "rg-old"))
	}
	if got := reload(t, db, m.ID); got.Status != models.MigrationStatusApplied {
		t.Errorf("status = %s", got.Status)
	}
}

// With the old row gone, a re-key still has work while a want points at the old ID —
// it is not closed as moot.
func TestReleaseGroupRedirectIsNotMootWhileAWantRemains(t *testing.T) {
	db := testDB(t)
	wantFor(t, db, "art-1", "rg-old", models.DesireSourceManual)
	m := rekeyMigration(t, db, "rg-old", "rg-new")
	if _, moot := mootReason(db, m); moot {
		t.Fatal("closed as moot with a want still on the old ID")
	}
	if res, _ := ProcessPending(db, Policy{}); res.Applied != 1 {
		t.Fatalf("result = %+v, want applied", res)
	}
	var d models.CollectionDesire
	db.First(&d)
	if d.ReleaseGroupMBID != "rg-new" {
		t.Errorf("want on %s, want rg-new", d.ReleaseGroupMBID)
	}
}

func TestReviewDescribesARekey(t *testing.T) {
	db := testDB(t)
	storeGroup(t, db, "rg-old", "art-1", "Debut", func(rg *models.CollectionReleaseGroup) { rg.Owned = true })
	wantFor(t, db, "art-1", "rg-old", models.DesireSourceManual)
	m := rekeyMigration(t, db, "rg-old", "rg-new")

	r := NewReview(db, m)
	if !strings.HasPrefix(r.Problem, "Lidarr moved this album to another ID") {
		t.Errorf("problem = %q", r.Problem)
	}
	// Files block it; the want, which moves with it, must not be named as the blocker.
	if !strings.Contains(r.Effect, "files on disk still resolve") || strings.Contains(r.Effect, "wanted") {
		t.Errorf("effect = %q", r.Effect)
	}
}

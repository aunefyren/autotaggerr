package collection

import (
	"strings"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// RetireReleaseGroup is prune's rules minus the one guard the evidence overrules.
// These tests pin exactly which guards survived, because the difference between the
// two paths is the whole reason there are two.

// TestRetireRemovesRepairedAwayGroup: the case all of this exists for — an album the
// manager has stopped listing (a refresh dropped it), nobody owns, nobody asked for,
// and whose ID resolves nowhere.
func TestRetireRemovesRepairedAwayGroup(t *testing.T) {
	db := testDB(t)
	storeGroup(t, db, "rg-ghost", "artist-1", nil)

	removed, reason, err := RetireReleaseGroup(db, "rg-ghost")
	if err != nil {
		t.Fatalf("retire: %v", err)
	}
	if !removed {
		t.Fatalf("want the group retired, refused with: %s", reason)
	}
	if groupExists(t, db, "rg-ghost") {
		t.Error("the release-group row is still there")
	}

	var links int64
	if err := db.Model(&models.CollectionReleaseGroupArtist{}).
		Where("release_group_mb_id = ?", "rg-ghost").Count(&links).Error; err != nil {
		t.Fatalf("count links: %v", err)
	}
	if links != 0 {
		t.Errorf("credit links left behind: %d", links)
	}
}

// TestRetireRefusesGroupStillInCatalog: deleting a row the manager still lists is
// futile — SyncManagers upserts a row per manager album, so the next sync restores it.
// The refusal has to name the fix, because the user's next move is in the manager.
func TestRetireRefusesGroupStillInCatalog(t *testing.T) {
	db := testDB(t)
	storeGroup(t, db, "rg-listed", "artist-1", func(rg *models.CollectionReleaseGroup) {
		rg.InCatalog = true
	})

	removed, reason, err := RetireReleaseGroup(db, "rg-listed")
	if err != nil {
		t.Fatalf("retire: %v", err)
	}
	if removed {
		t.Fatal("must not retire a row the manager will restore on the next sync")
	}
	if !strings.Contains(reason, "refresh the artist") {
		t.Errorf("the refusal must name the fix, got %q", reason)
	}
	if !groupExists(t, db, "rg-listed") {
		t.Error("the row was deleted despite the refusal")
	}
}

// TestRetireRefusesOwnedGroup: files on disk outrank a MusicBrainz 404. The ID being
// unreadable does not make the audio stop existing, and removing the row would drop
// the album off the artist page while its files sit in the library.
func TestRetireRefusesOwnedGroup(t *testing.T) {
	db := testDB(t)
	storeGroup(t, db, "rg-owned", "artist-1", func(rg *models.CollectionReleaseGroup) {
		rg.Owned = true
	})

	removed, reason, err := RetireReleaseGroup(db, "rg-owned")
	if err != nil {
		t.Fatalf("retire: %v", err)
	}
	if removed {
		t.Fatal("must not retire a group with files on disk")
	}
	if reason == "" {
		t.Error("a refusal must say why")
	}
	if !groupExists(t, db, "rg-owned") {
		t.Error("the row was deleted despite the refusal")
	}
}

// TestRetireRefusesWantedGroup: authored intent is never collateral damage — the rule
// the deletion path already follows everywhere else.
func TestRetireRefusesWantedGroup(t *testing.T) {
	db := testDB(t)
	storeGroup(t, db, "rg-wanted", "artist-1", nil)
	if err := db.Create(&models.CollectionDesire{
		ArtistMBID: "artist-1", ReleaseGroupMBID: "rg-wanted",
	}).Error; err != nil {
		t.Fatalf("create desire: %v", err)
	}

	removed, reason, err := RetireReleaseGroup(db, "rg-wanted")
	if err != nil {
		t.Fatalf("retire: %v", err)
	}
	if removed {
		t.Fatal("must not retire a group somebody asked for")
	}
	if reason == "" {
		t.Error("a refusal must say why")
	}
}

// TestRetireRefusesCoCreditedGroup: a collaboration is not orphaned by one artist's
// claim lapsing.
func TestRetireRefusesCoCreditedGroup(t *testing.T) {
	db := testDB(t)
	storeGroup(t, db, "rg-collab", "artist-1", nil)
	if err := db.Create(&models.CollectionReleaseGroupArtist{
		ReleaseGroupMBID: "rg-collab", ArtistMBID: "artist-2",
	}).Error; err != nil {
		t.Fatalf("create second credit: %v", err)
	}

	removed, _, err := RetireReleaseGroup(db, "rg-collab")
	if err != nil {
		t.Fatalf("retire: %v", err)
	}
	if removed {
		t.Fatal("must not retire a group another artist is credited on")
	}
}

// TestRetireMissingGroupIsNotAnError: retiring a row that is already gone is the state
// the caller wanted. Approving the same migration twice, or an artist prune having
// taken the row first, must not surface as a failure.
func TestRetireMissingGroupIsNotAnError(t *testing.T) {
	db := testDB(t)

	removed, reason, err := RetireReleaseGroup(db, "rg-absent")
	if err != nil {
		t.Fatalf("retire: %v", err)
	}
	if removed {
		t.Error("nothing was there to remove")
	}
	if reason != "" {
		t.Errorf("an absent row is not a refusal, got %q", reason)
	}
}

// TestGhostReleaseGroupsListsCatalogOnly: the Lidarr-sync finding counts albums the
// manager still lists. A recorded deletion for a group no manager claims is not the
// manager's problem to fix and would be noise on that pass.
func TestGhostReleaseGroupsListsCatalogOnly(t *testing.T) {
	db := testDB(t)
	storeGroup(t, db, "rg-in-catalog", "artist-1", func(rg *models.CollectionReleaseGroup) {
		rg.InCatalog = true
	})
	storeGroup(t, db, "rg-not-in-catalog", "artist-1", nil)
	storeGroup(t, db, "rg-healthy", "artist-1", func(rg *models.CollectionReleaseGroup) {
		rg.InCatalog = true
	})

	for _, id := range []string{"rg-in-catalog", "rg-not-in-catalog"} {
		if err := db.Create(&models.MusicbrainzMigration{
			EntityType: models.MigrationEntityReleaseGroup,
			OldMBID:    id,
			Kind:       models.MigrationKindDeleted,
			Status:     models.MigrationStatusPending,
			DetectedAt: time.Now(),
		}).Error; err != nil {
			t.Fatalf("create migration: %v", err)
		}
	}

	ghosts, err := GhostReleaseGroups(db)
	if err != nil {
		t.Fatalf("ghosts: %v", err)
	}
	if len(ghosts) != 1 || ghosts[0] != "rg-in-catalog" {
		t.Fatalf("ghosts = %v, want [rg-in-catalog]", ghosts)
	}
}

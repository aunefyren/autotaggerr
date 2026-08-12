package collection

import (
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
)

// The repair pass writes to a manager, so what it declines to do matters as much as
// what it does. These cover the gates; the refresh/wait/re-sync sequence itself needs a
// live Lidarr and is exercised against the real thing rather than mocked here.

func ghostRow(t *testing.T, db *gorm.DB, mbID, artistMBID string, attempted *time.Time) {
	t.Helper()
	storeGroup(t, db, mbID, artistMBID, func(rg *models.CollectionReleaseGroup) {
		rg.InCatalog = true
	})
	if err := db.Create(&models.MusicbrainzMigration{
		EntityType:        models.MigrationEntityReleaseGroup,
		OldMBID:           mbID,
		Kind:              models.MigrationKindDeleted,
		Status:            models.MigrationStatusPending,
		DetectedAt:        time.Now(),
		RepairAttemptedAt: attempted,
	}).Error; err != nil {
		t.Fatalf("create migration: %v", err)
	}
}

// TestRepairSkipsDisabledManager: the opt-out has to be checked before any request is
// made, not after. A manager whose key is deliberately read-only must see no write at
// all — and with no manager left to ask, the pass reports its candidates and stops.
func TestRepairSkipsDisabledManager(t *testing.T) {
	db := testDB(t)
	ghostRow(t, db, "rg-ghost", "artist-1", nil)
	if err := db.Create(&models.Manager{
		Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true,
		LidarrBaseURL: "http://127.0.0.1:1", LidarrAPIKey: "k",
		LidarrSkipArtistRefresh: true,
	}).Error; err != nil {
		t.Fatalf("create manager: %v", err)
	}

	stats, err := RepairGhostReleaseGroups(db)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if stats.Candidates != 1 {
		t.Errorf("Candidates = %d, want 1", stats.Candidates)
	}
	if stats.Artists != 0 {
		t.Errorf("Artists = %d, want 0 — the manager opted out", stats.Artists)
	}
	if len(stats.Failures) != 0 {
		t.Errorf("opting out is not a failure, got %v", stats.Failures)
	}
}

// TestRepairNoCandidatesDoesNothing: the common case. A collection with no unresolvable
// IDs must not touch a manager at all, so the pass returns before it even lists them.
func TestRepairNoCandidatesDoesNothing(t *testing.T) {
	db := testDB(t)
	storeGroup(t, db, "rg-healthy", "artist-1", func(rg *models.CollectionReleaseGroup) {
		rg.InCatalog = true
	})
	if err := db.Create(&models.Manager{
		Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true,
		LidarrBaseURL: "http://127.0.0.1:1", LidarrAPIKey: "k",
	}).Error; err != nil {
		t.Fatalf("create manager: %v", err)
	}

	stats, err := RepairGhostReleaseGroups(db)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if stats.Candidates != 0 || stats.Artists != 0 {
		t.Errorf("want an inert pass, got %+v", stats)
	}
}

// TestRepairCooldownSuppressesRecentAttempt: a refresh that did not fix a row will not
// fix it on the retry either, so an artist asked recently is left alone. Without this,
// an album genuinely absent everywhere triggers a manager refresh on every single run.
func TestRepairCooldownSuppressesRecentAttempt(t *testing.T) {
	db := testDB(t)
	recent := time.Now().Add(-time.Hour)
	ghostRow(t, db, "rg-ghost", "artist-1", &recent)

	if !inCooldown(db, "artist-1") {
		t.Error("an artist asked an hour ago must be in cooldown")
	}

	old := time.Now().Add(-repairCooldown - time.Hour)
	if err := db.Model(&models.MusicbrainzMigration{}).
		Where("old_mb_id = ?", "rg-ghost").
		Update("repair_attempted_at", old).Error; err != nil {
		t.Fatalf("age the attempt: %v", err)
	}
	if inCooldown(db, "artist-1") {
		t.Error("an attempt older than the cooldown must not suppress a retry")
	}
}

// TestRepairCooldownIgnoredForNewGhost: a newly discovered dead ID on an artist asked
// about before must still get a refresh — one refresh covers all of that artist's
// albums, so the cooldown cannot be allowed to strand a row nobody has asked about yet.
func TestRepairCooldownIgnoredForNewGhost(t *testing.T) {
	db := testDB(t)
	recent := time.Now().Add(-time.Hour)
	ghostRow(t, db, "rg-old", "artist-1", &recent)
	ghostRow(t, db, "rg-new", "artist-1", nil)

	if inCooldown(db, "artist-1") {
		t.Error("an artist with a never-attempted ghost must not be in cooldown")
	}
}

// TestArtistsHoldingGhostsDeduplicates: one refresh per artist is the unit of work. A
// single artist held eight dead IDs on the instance this was built against; asking
// eight times would be eight times the load for one answer.
func TestArtistsHoldingGhostsDeduplicates(t *testing.T) {
	db := testDB(t)
	ghostRow(t, db, "rg-a", "artist-1", nil)
	ghostRow(t, db, "rg-b", "artist-1", nil)
	ghostRow(t, db, "rg-c", "artist-2", nil)

	artists, err := artistsHoldingGhosts(db, []string{"rg-a", "rg-b", "rg-c"})
	if err != nil {
		t.Fatalf("artistsHoldingGhosts: %v", err)
	}
	if len(artists) != 2 || artists[0] != "artist-1" || artists[1] != "artist-2" {
		t.Fatalf("artists = %v, want [artist-1 artist-2]", artists)
	}
}

// TestMarkRepairAttemptedStampsOnlyThisArtist: the stamp is what stops a retry loop, so
// stamping too widely would silence artists that were never asked about.
func TestMarkRepairAttemptedStampsOnlyThisArtist(t *testing.T) {
	db := testDB(t)
	ghostRow(t, db, "rg-mine", "artist-1", nil)
	ghostRow(t, db, "rg-theirs", "artist-2", nil)

	markRepairAttempted(db, "artist-1", []string{"rg-mine", "rg-theirs"})

	var mine, theirs models.MusicbrainzMigration
	if err := db.Where("old_mb_id = ?", "rg-mine").First(&mine).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := db.Where("old_mb_id = ?", "rg-theirs").First(&theirs).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if mine.RepairAttemptedAt == nil {
		t.Error("the refreshed artist's ghost must be stamped")
	}
	if theirs.RepairAttemptedAt != nil {
		t.Error("another artist's ghost must not be stamped")
	}
}

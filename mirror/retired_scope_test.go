package mirror

import (
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
)

// Recording a deletion is only half of not re-asking. A scope is built from the
// collection's own rows, and a group awaiting review is still one of those rows — so
// these tests pin that a recorded deletion actually removes it from the work list.

func seedGroup(t *testing.T, db *gorm.DB, mbID, artistMBID string) {
	t.Helper()
	if err := db.Create(&models.CollectionReleaseGroup{
		MBID: mbID, ArtistMBID: artistMBID, Title: mbID,
	}).Error; err != nil {
		t.Fatalf("create release-group: %v", err)
	}
}

func seedGroupDeletion(t *testing.T, db *gorm.DB, mbID, status string) {
	t.Helper()
	if err := db.Create(&models.MusicbrainzMigration{
		EntityType: models.MigrationEntityReleaseGroup,
		OldMBID:    mbID,
		Kind:       models.MigrationKindDeleted,
		Status:     status,
		DetectedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("create migration: %v", err)
	}
}

func hasGroup(groups []string, mbID string) bool {
	for _, g := range groups {
		if g == mbID {
			return true
		}
	}
	return false
}

// TestCollectionScopeSkipsRetiredGroups: the loop this closes. Without it the pass
// re-probes and re-confirms a known-dead ID every night, forever.
func TestCollectionScopeSkipsRetiredGroups(t *testing.T) {
	db := testDB(t)
	seedGroup(t, db, "rg-dead", "artist-1")
	seedGroup(t, db, "rg-live", "artist-1")
	seedGroupDeletion(t, db, "rg-dead", models.MigrationStatusPending)

	scope, err := CollectionScope(db, false)
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	if hasGroup(scope.Groups, "rg-dead") {
		t.Error("a group with a recorded deletion must not be re-probed")
	}
	if !hasGroup(scope.Groups, "rg-live") {
		t.Error("an ordinary group must still be in scope")
	}
}

// TestForcedScopeStillSkipsRetiredGroups: force means "do not trust the cache", not
// "re-ask MusicBrainz about IDs it has already said it does not have". A forced pass is
// the expensive one, and re-probing confirmed-dead IDs is the least useful way to spend
// it.
func TestForcedScopeStillSkipsRetiredGroups(t *testing.T) {
	db := testDB(t)
	seedGroup(t, db, "rg-dead", "artist-1")
	seedGroupDeletion(t, db, "rg-dead", models.MigrationStatusPending)

	scope, err := CollectionScope(db, true)
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	if hasGroup(scope.Groups, "rg-dead") {
		t.Error("a forced pass must still skip a confirmed-dead group")
	}
}

// TestDismissedDeletionStillSkipped: dismissing is a decision about the collection —
// "leave this row alone" — not a claim that the ID resolves. Re-reading it cannot
// produce a different answer, so silencing the queue must silence the probe too.
func TestDismissedDeletionStillSkipped(t *testing.T) {
	db := testDB(t)
	seedGroup(t, db, "rg-dismissed", "artist-1")
	seedGroupDeletion(t, db, "rg-dismissed", models.MigrationStatusDismissed)

	scope, err := CollectionScope(db, false)
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	if hasGroup(scope.Groups, "rg-dismissed") {
		t.Error("a dismissed deletion must still stop the nightly re-probe")
	}
}

// TestArtistScopeSkipsRetiredGroups: the per-artist button reads the same rows, so it
// needs the same exclusion — otherwise opening one artist re-probes their dead IDs.
func TestArtistScopeSkipsRetiredGroups(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionArtist{MBID: "artist-1", Name: "Artist"}).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	seedGroup(t, db, "rg-dead", "artist-1")
	seedGroup(t, db, "rg-live", "artist-1")
	seedGroupDeletion(t, db, "rg-dead", models.MigrationStatusPending)

	scope, err := ArtistScope(db, "artist-1", false)
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	if hasGroup(scope.Groups, "rg-dead") {
		t.Error("the per-artist scope must skip a confirmed-dead group")
	}
	if !hasGroup(scope.Groups, "rg-live") {
		t.Error("an ordinary group must still be in scope")
	}
}

// TestArtistDeletionDoesNotSkipGroups: the exclusion keys on entity type. An artist
// deletion sharing an MBID-shaped key must not quietly drop release-groups from scope.
func TestArtistDeletionDoesNotSkipGroups(t *testing.T) {
	db := testDB(t)
	seedGroup(t, db, "shared-id", "artist-1")
	if err := db.Create(&models.MusicbrainzMigration{
		EntityType: models.MigrationEntityArtist,
		OldMBID:    "shared-id",
		Kind:       models.MigrationKindDeleted,
		Status:     models.MigrationStatusPending,
		DetectedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("create migration: %v", err)
	}

	scope, err := CollectionScope(db, false)
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	if !hasGroup(scope.Groups, "shared-id") {
		t.Error("an artist deletion must not exclude a release-group")
	}
}

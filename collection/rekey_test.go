package collection

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

func TestDetectRekeys(t *testing.T) {
	before := []catalogEntry{
		{mbid: "old-1", title: "OK Computer", primaryType: "Album"},
		{mbid: "kept", title: "The Bends", primaryType: "Album"},
		{mbid: "old-single", title: "Creep", primaryType: "Single"},
		{mbid: "twin-a", title: "Live", primaryType: "Album"},
		{mbid: "twin-b", title: "Live", primaryType: "Album"},
	}
	listed := []models.LidarrAlbum{
		{ForeignAlbumID: "new-1", Title: "OK Computer", AlbumType: "album"},
		{ForeignAlbumID: "kept", Title: "The Bends", AlbumType: "Album"},
		// Same title, different type: an album is not a re-keyed single.
		{ForeignAlbumID: "new-creep-album", Title: "Creep", AlbumType: "Album"},
		// Two left with one title and one arrived: which one moved is a guess.
		{ForeignAlbumID: "live-new", Title: "Live", AlbumType: "Album"},
	}

	got := detectRekeys(before, listed)
	if len(got) != 1 || got[0].oldMBID != "old-1" || got[0].newMBID != "new-1" {
		t.Fatalf("rekeys = %+v, want only old-1 -> new-1", got)
	}
}

func TestDetectRekeysFoldsTypography(t *testing.T) {
	before := []catalogEntry{{mbid: "old", title: "Don’t Stop", primaryType: "Album"}}
	listed := []models.LidarrAlbum{{ForeignAlbumID: "new", Title: " Don't Stop", AlbumType: "Album"}}
	if got := detectRekeys(before, listed); len(got) != 1 {
		t.Fatalf("rekeys = %+v, want the apostrophe variant paired", got)
	}
}

// End to end over two passes: Lidarr lists an album under one ID, then under another.
// The second pass records a release-group redirect — the re-key no 404 would ever
// report, because the old ID is a real album.
func TestSyncRecordsAnAlbumLidarrRekeyed(t *testing.T) {
	var mu sync.Mutex
	albumID := "rg-old"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/artist"):
			_ = json.NewEncoder(w).Encode([]models.LidarrArtist{{ID: 1, ForeignArtistID: "art-1", Name: "Artist"}})
		case strings.HasPrefix(r.URL.Path, "/api/v1/album"):
			mu.Lock()
			id := albumID
			mu.Unlock()
			_ = json.NewEncoder(w).Encode([]models.LidarrAlbum{{ID: 10, ForeignAlbumID: id, Title: "Debut", AlbumType: "Album"}})
		}
	}))
	t.Cleanup(srv.Close)

	db := testDB(t)
	if err := db.Create(&models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true, LidarrBaseURL: srv.URL, LidarrAPIKey: "k"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.CollectionArtist{MBID: "art-1", Name: "Artist", ManagedBy: models.ManagedByLidarr}).Error; err != nil {
		t.Fatal(err)
	}

	if stats, err := SyncLidarr(db); err != nil || len(stats.Rekeyed) != 0 {
		t.Fatalf("first pass: %+v, %v", stats, err)
	}
	mu.Lock()
	albumID = "rg-new"
	mu.Unlock()
	stats, err := SyncLidarr(db)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if len(stats.Rekeyed) != 1 || stats.Rekeyed[0] != "rg-old" {
		t.Fatalf("rekeyed = %v, want [rg-old]", stats.Rekeyed)
	}
	if !strings.Contains(SyncSummaryLine(stats), "1 re-keyed") {
		t.Errorf("summary = %q", SyncSummaryLine(stats))
	}

	var m models.MusicbrainzMigration
	if err := db.Where("entity_type = ? AND old_mb_id = ?", models.MigrationEntityReleaseGroup, "rg-old").First(&m).Error; err != nil {
		t.Fatalf("no migration recorded: %v", err)
	}
	if m.Kind != models.MigrationKindRedirect || m.NewMBID != "rg-new" || m.Status != models.MigrationStatusPending || m.SourceLabel() != "Lidarr" {
		t.Errorf("migration = %+v", m)
	}

	// A third pass sees nothing new: the old ID is no longer in the catalog to leave it.
	if stats, _ := SyncLidarr(db); len(stats.Rekeyed) != 0 {
		t.Errorf("third pass re-detected %v", stats.Rekeyed)
	}
}

// A pending removal of the old ID — the repair pass asked Lidarr about a dead ID, and
// Lidarr answered by re-keying the album — becomes the redirect, so the album's wants
// follow it instead of being stranded by the removal.
func TestRecordRekeyTurnsAPendingRemovalIntoARedirect(t *testing.T) {
	db := testDB(t)
	removal := models.MusicbrainzMigration{EntityType: models.MigrationEntityReleaseGroup, OldMBID: "rg-ghost",
		Kind: models.MigrationKindDeleted, Status: models.MigrationStatusFailed, Error: "the manager still lists this album"}
	settled := models.MusicbrainzMigration{EntityType: models.MigrationEntityReleaseGroup, OldMBID: "rg-done",
		Kind: models.MigrationKindDeleted, Status: models.MigrationStatusDismissed}
	for _, m := range []*models.MusicbrainzMigration{&removal, &settled} {
		if err := db.Create(m).Error; err != nil {
			t.Fatal(err)
		}
	}

	if !recordRekey(db, rekey{oldMBID: "rg-ghost", newMBID: "rg-live", title: "X"}) {
		t.Fatal("the pending removal was not turned into a redirect")
	}
	if recordRekey(db, rekey{oldMBID: "rg-done", newMBID: "rg-other", title: "Y"}) {
		t.Error("a dismissed row was re-raised")
	}
	var got models.MusicbrainzMigration
	db.Where("old_mb_id = ?", "rg-ghost").First(&got)
	if got.Kind != models.MigrationKindRedirect || got.NewMBID != "rg-live" || got.Status != models.MigrationStatusPending || got.Error != "" {
		t.Errorf("row = %+v", got)
	}
}

package collection

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
)

// The refresh → wait → re-sync sequence, against a mock Lidarr. repair_test.go covers
// the gates; these cover what happens once a manager is actually asked.

// repairLidarr is a mock Lidarr for the repair flow. `status` is what the refresh
// command reports when polled; an empty string makes the command POST itself fail.
// `albums` is what the re-sync reads back after the refresh.
type repairLidarr struct {
	listFails bool
	status    string
	albums    []models.LidarrAlbum
	refreshes atomic.Int32
}

func (l *repairLidarr) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/command" && r.Method == http.MethodPost:
			l.refreshes.Add(1)
			if l.status == "" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(models.LidarrCommand{ID: 7, Name: "RefreshArtist", Status: "queued"})
		case strings.HasPrefix(r.URL.Path, "/api/v1/command/"):
			_ = json.NewEncoder(w).Encode(models.LidarrCommand{ID: 7, Name: "RefreshArtist", Status: l.status, Message: "boom"})
		case strings.HasPrefix(r.URL.Path, "/api/v1/artist"):
			if l.listFails {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode([]models.LidarrArtist{
				{ID: 1, ForeignArtistID: "artist-1", Name: "Band"},
				{ID: 2, ForeignArtistID: "artist-elsewhere", Name: "Other"},
			})
		case strings.HasPrefix(r.URL.Path, "/api/v1/album"):
			_ = json.NewEncoder(w).Encode(l.albums)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// repairFixture is one Lidarr-managed artist holding one ghost album, with a manager
// pointed at the mock. Extra ghost artists are seeded by the caller.
func repairFixture(t *testing.T, l *repairLidarr) *gorm.DB {
	t.Helper()
	srv := l.serve(t)
	db := testDB(t)
	if err := db.Create(&models.Manager{
		Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true,
		LidarrBaseURL: srv.URL, LidarrAPIKey: "k",
	}).Error; err != nil {
		t.Fatalf("manager: %v", err)
	}
	if err := db.Create(&models.CollectionArtist{
		MBID: "artist-1", Name: "Band", ManagedBy: models.ManagedByLidarr,
	}).Error; err != nil {
		t.Fatalf("artist: %v", err)
	}
	ghostRow(t, db, "rg-ghost", "artist-1", nil)
	return db
}

func attemptStamped(t *testing.T, db *gorm.DB, mbID string) bool {
	t.Helper()
	var m models.MusicbrainzMigration
	if err := db.Where("old_mb_id = ?", mbID).First(&m).Error; err != nil {
		t.Fatalf("load migration: %v", err)
	}
	return m.RepairAttemptedAt != nil
}

// TestRepairRefreshRepairsGhost is the whole point of the pass: the manager re-keys the
// dead album, the re-sync mirrors the corrected catalog, and the dead ID stops being a
// ghost. The pass reports it as repaired without retiring anything itself.
func TestRepairRefreshRepairsGhost(t *testing.T) {
	l := &repairLidarr{status: "completed", albums: []models.LidarrAlbum{
		{ForeignAlbumID: "rg-live", Title: "Heatstroke", AlbumType: "Album", Monitored: true},
	}}
	db := repairFixture(t, l)

	stats, err := RepairGhostReleaseGroups(db)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if l.refreshes.Load() != 1 {
		t.Errorf("refreshes = %d, want exactly one", l.refreshes.Load())
	}
	if stats.Candidates != 1 || stats.Artists != 1 || stats.Repaired != 1 {
		t.Errorf("stats = %+v, want 1 candidate, 1 artist, 1 repaired", stats)
	}
	if len(stats.Failures) != 0 {
		t.Errorf("failures = %v", stats.Failures)
	}
	if !groupExists(t, db, "rg-ghost") {
		t.Error("the repair pass must not retire the ghost row itself")
	}
	if catalogOf(t, db, "rg-ghost").InCatalog {
		t.Error("ghost should have dropped out of the catalog view after the re-sync")
	}
	if !catalogOf(t, db, "rg-live").InCatalog {
		t.Error("the re-keyed album should be mirrored in")
	}
	if !attemptStamped(t, db, "rg-ghost") {
		t.Error("the attempt must be stamped")
	}

	// A second unattended pass finds nothing left to repair.
	again, err := RepairGhostReleaseGroups(db)
	if err != nil {
		t.Fatalf("second repair: %v", err)
	}
	if again.Candidates != 0 || l.refreshes.Load() != 1 {
		t.Errorf("second pass = %+v (refreshes %d), want an inert pass", again, l.refreshes.Load())
	}
}

// TestRepairRefreshFailureIsReportedAndStamped: a manager that rejects the refresh is a
// failure for the event, and is still stamped so it is not re-asked on every pass.
func TestRepairRefreshFailureIsReportedAndStamped(t *testing.T) {
	l := &repairLidarr{status: ""}
	db := repairFixture(t, l)

	stats, err := RepairGhostReleaseGroups(db)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if len(stats.Failures) != 1 || !strings.Contains(stats.Failures[0], "requesting refresh") {
		t.Fatalf("failures = %v, want one requesting-refresh failure", stats.Failures)
	}
	if stats.Artists != 1 || stats.Repaired != 0 {
		t.Errorf("stats = %+v, want the artist counted and nothing repaired", stats)
	}
	if !attemptStamped(t, db, "rg-ghost") {
		t.Error("a failed attempt must still be stamped")
	}

	// The stamp now holds the next unattended pass off...
	next, err := RepairGhostReleaseGroups(db)
	if err != nil {
		t.Fatalf("second repair: %v", err)
	}
	if next.Skipped != 1 || next.Artists != 0 || l.refreshes.Load() != 1 {
		t.Errorf("second pass = %+v (refreshes %d), want skipped in cooldown", next, l.refreshes.Load())
	}

	// ...but not a person pressing approve.
	forced, err := RepairGhostReleaseGroupsWith(db, RepairOptions{ArtistMBID: "artist-1", IgnoreCooldown: true})
	if err != nil {
		t.Fatalf("forced repair: %v", err)
	}
	if forced.Skipped != 0 || forced.Artists != 1 || l.refreshes.Load() != 2 {
		t.Errorf("forced pass = %+v (refreshes %d), want the cooldown ignored", forced, l.refreshes.Load())
	}
}

// TestRepairCommandFailedIsReported: Lidarr accepting the refresh and then failing it
// is reported with Lidarr's own message, and no re-sync follows.
func TestRepairCommandFailedIsReported(t *testing.T) {
	l := &repairLidarr{status: "failed"}
	db := repairFixture(t, l)

	stats, err := RepairGhostReleaseGroups(db)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if len(stats.Failures) != 1 || !strings.Contains(stats.Failures[0], "waiting for refresh") ||
		!strings.Contains(stats.Failures[0], "boom") {
		t.Fatalf("failures = %v, want a waiting-for-refresh failure quoting Lidarr", stats.Failures)
	}
	if !catalogOf(t, db, "rg-ghost").InCatalog {
		t.Error("no re-sync may follow a failed refresh")
	}
}

// TestRepairListingFailureIsReported: a manager whose artist list cannot be read is a
// failure, not a silent skip, and nothing is refreshed through it.
func TestRepairListingFailureIsReported(t *testing.T) {
	l := &repairLidarr{listFails: true, status: "completed"}
	db := repairFixture(t, l)

	stats, err := RepairGhostReleaseGroups(db)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if len(stats.Failures) != 1 || stats.Artists != 0 || l.refreshes.Load() != 0 {
		t.Errorf("stats = %+v (refreshes %d), want one failure and no refresh", stats, l.refreshes.Load())
	}
}

// TestRepairSkipsArtistsTheManagerDoesNotHold: a ghost whose artist no manager lists
// cannot be repaired by asking, and an unconfigured manager row is not asked at all.
func TestRepairSkipsArtistsTheManagerDoesNotHold(t *testing.T) {
	l := &repairLidarr{status: "completed"}
	db := repairFixture(t, l)
	ghostRow(t, db, "rg-stranger", "artist-unlisted", nil)
	if err := db.Create(&models.Manager{
		Name: "Blank", Type: models.ManagerTypeLidarr, Enabled: true,
	}).Error; err != nil {
		t.Fatalf("manager: %v", err)
	}
	// The listed artist was asked recently; only the cooldown stands in its way.
	recent := time.Now().Add(-time.Hour)
	if err := db.Model(&models.MusicbrainzMigration{}).Where("old_mb_id = ?", "rg-ghost").
		Update("repair_attempted_at", recent).Error; err != nil {
		t.Fatalf("stamp: %v", err)
	}

	stats, err := RepairGhostReleaseGroups(db)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if stats.Candidates != 2 || stats.Skipped != 1 || stats.Artists != 0 {
		t.Errorf("stats = %+v, want 2 candidates, 1 skipped, none refreshed", stats)
	}
	if l.refreshes.Load() != 0 {
		t.Errorf("refreshes = %d, want none", l.refreshes.Load())
	}
}

// TestRepairNilDB: the pass is inert without a database.
func TestRepairNilDB(t *testing.T) {
	stats, err := RepairGhostReleaseGroups(nil)
	if err != nil || stats.Candidates != 0 {
		t.Errorf("nil db = %+v, %v", stats, err)
	}
}

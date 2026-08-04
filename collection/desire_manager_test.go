package collection

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"gorm.io/gorm"
)

// Manager-derived wants: Lidarr owns identity for its artists, so the album it
// monitors *and the release it selected* are recorded as Autotaggerr's own desire
// rows. Before this, only the album half reached the collection — an album green in
// Lidarr on a specific release showed no edition wanted here.

// lidarrServing is a mock Lidarr returning one artist and the albums given. The
// albums are re-read per request, so a test can change the selection and sync again.
func lidarrServing(t *testing.T, albums *[]models.LidarrAlbum) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/artist"):
			_ = json.NewEncoder(w).Encode([]models.LidarrArtist{{ID: 1, ForeignArtistID: "art-1", Name: "Band"}})
		case strings.HasPrefix(r.URL.Path, "/api/v1/album"):
			_ = json.NewEncoder(w).Encode(*albums)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// monitoredAlbum is a Lidarr album monitored on one release, the ordinary case.
func monitoredAlbum(rgMBID, relMBID string) models.LidarrAlbum {
	return models.LidarrAlbum{
		ForeignAlbumID: rgMBID, Title: "Album", AlbumType: "Album", Monitored: true,
		Statistics: models.LidarrAlbumStatistics{TrackCount: 10, TrackFileCount: 10},
		Releases: []models.LidarrAlbumRel{
			{ID: 1, Monitored: false, ForeignReleaseID: "rel-other"},
			{ID: 2, Monitored: true, ForeignReleaseID: relMBID},
		},
	}
}

// lidarrCollection wires a mock Lidarr manager and one Lidarr-managed artist.
func lidarrCollection(t *testing.T, albums *[]models.LidarrAlbum) *gorm.DB {
	t.Helper()
	srv := lidarrServing(t, albums)
	db := testDB(t)
	if err := db.Create(&models.Manager{
		Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true,
		LidarrBaseURL: srv.URL, LidarrAPIKey: "k",
	}).Error; err != nil {
		t.Fatalf("manager: %v", err)
	}
	if err := db.Create(&models.CollectionArtist{
		MBID: "art-1", Name: "Band", ManagedBy: models.ManagedByLidarr,
	}).Error; err != nil {
		t.Fatalf("artist: %v", err)
	}
	return db
}

func syncLidarr(t *testing.T, db *gorm.DB) {
	t.Helper()
	if _, _, err := SyncLidarr(db); err != nil {
		t.Fatalf("SyncLidarr: %v", err)
	}
}

// TestSyncLidarrRecordsTheMonitoredRelease is the reported gap: the album want
// reached the collection but the edition want did not, so a Lidarr album was green
// on a release with no edition marked wanted.
func TestSyncLidarrRecordsTheMonitoredRelease(t *testing.T) {
	albums := []models.LidarrAlbum{monitoredAlbum("rg-1", "rel-1")}
	db := lidarrCollection(t, &albums)
	syncLidarr(t, db)

	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", "rg-1").First(&rg).Error; err != nil {
		t.Fatalf("release-group: %v", err)
	}
	if rg.CatalogReleaseMBID != "rel-1" {
		t.Errorf("catalog release = %q, want rel-1", rg.CatalogReleaseMBID)
	}

	got := desiresFor(t, db, "rg-1")
	if len(got) != 1 {
		t.Fatalf("want one manager want, got %+v", got)
	}
	if got[0].ReleaseMBID != "rel-1" || got[0].Source != models.DesireSourceManager {
		t.Errorf("want = %+v, want rel-1 from the manager", got[0])
	}
	if got[0].ArtistMBID != "art-1" {
		t.Errorf("want recorded against %q, want art-1", got[0].ArtistMBID)
	}
}

// TestManagerDesireRepointsWhenLidarrChangesEdition: changing the monitored release
// in Lidarr is the source of truth, so the mirrored want follows it rather than
// accumulating a second row.
func TestManagerDesireRepointsWhenLidarrChangesEdition(t *testing.T) {
	albums := []models.LidarrAlbum{monitoredAlbum("rg-1", "rel-1")}
	db := lidarrCollection(t, &albums)
	syncLidarr(t, db)

	albums = []models.LidarrAlbum{monitoredAlbum("rg-1", "rel-2")}
	syncLidarr(t, db)

	got := desiresFor(t, db, "rg-1")
	if len(got) != 1 || got[0].ReleaseMBID != "rel-2" {
		t.Fatalf("want a single want re-pointed to rel-2, got %+v", got)
	}
}

// TestManagerDesirePrunedWhenLidarrStopsMonitoring: an unmonitored album is not
// wanted, and a mirrored row that outlives its cause is a want nothing can explain.
func TestManagerDesirePrunedWhenLidarrStopsMonitoring(t *testing.T) {
	albums := []models.LidarrAlbum{monitoredAlbum("rg-1", "rel-1")}
	db := lidarrCollection(t, &albums)
	syncLidarr(t, db)
	if got := desiresFor(t, db, "rg-1"); len(got) != 1 {
		t.Fatalf("precondition: want one manager want, got %+v", got)
	}

	unmonitored := monitoredAlbum("rg-1", "rel-1")
	unmonitored.Monitored = false
	albums = []models.LidarrAlbum{unmonitored}
	syncLidarr(t, db)

	if got := desiresFor(t, db, "rg-1"); len(got) != 0 {
		t.Errorf("unmonitored album kept its mirrored want: %+v", got)
	}
}

// TestManagerDesireIsNotWrittenForAnAlbumWithNoSelectedRelease: monitoring an album
// without a monitored release says which album is wanted, not which edition. Making
// one up would be Autotaggerr claiming a decision Lidarr has not taken.
func TestManagerDesireIsNotWrittenForAnAlbumWithNoSelectedRelease(t *testing.T) {
	album := monitoredAlbum("rg-1", "")
	album.Releases = []models.LidarrAlbumRel{{ID: 1, Monitored: false, ForeignReleaseID: "rel-1"}}
	albums := []models.LidarrAlbum{album}
	db := lidarrCollection(t, &albums)
	syncLidarr(t, db)

	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", "rg-1").First(&rg).Error; err != nil {
		t.Fatalf("release-group: %v", err)
	}
	// The album is still wanted — catalog_monitored carries that, as it always did.
	if !rg.CatalogMonitored || rg.CatalogReleaseMBID != "" {
		t.Errorf("release-group = %+v, want monitored with no selected edition", rg)
	}
	if got := desiresFor(t, db, "rg-1"); len(got) != 0 {
		t.Errorf("invented an edition want: %+v", got)
	}
}

// TestManagerDesireNeverOverridesAHandAuthoredWant: an explicit pick outranks
// anything derived, and a group holding both an "any" want and a specific one is the
// contradiction SetDesire exists to prevent. A want from before the artist became
// Lidarr-managed is the way this happens in practice.
func TestManagerDesireNeverOverridesAHandAuthoredWant(t *testing.T) {
	albums := []models.LidarrAlbum{monitoredAlbum("rg-1", "rel-1")}
	db := lidarrCollection(t, &albums)
	if err := db.Create(&models.CollectionDesire{
		ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", ReleaseMBID: "rel-99",
		Source: models.DesireSourceManual,
	}).Error; err != nil {
		t.Fatalf("desire: %v", err)
	}

	syncLidarr(t, db)

	got := desiresFor(t, db, "rg-1")
	if len(got) != 1 || got[0].ReleaseMBID != "rel-99" || got[0].Source != models.DesireSourceManual {
		t.Errorf("the mirror overwrote authored intent: %+v", got)
	}
}

// TestManagerDesireOnlyForManagedArtists: a mirrored want must never appear on a
// natively-managed artist's page, where the user is the authority and would find a
// want they cannot account for.
func TestManagerDesireOnlyForManagedArtists(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionArtist{
		MBID: "art-1", Name: "Band", ManagedBy: models.ManagedByAutotaggerr,
	}).Error; err != nil {
		t.Fatalf("artist: %v", err)
	}
	if err := db.Create(&models.CollectionReleaseGroup{
		MBID: "rg-1", ArtistMBID: "art-1", Title: "Album",
		InCatalog: true, CatalogMonitored: true, CatalogReleaseMBID: "rel-1",
	}).Error; err != nil {
		t.Fatalf("release-group: %v", err)
	}

	if err := reconcileManagerDesires(db); err != nil {
		t.Fatalf("reconcileManagerDesires: %v", err)
	}
	if got := desiresFor(t, db, "rg-1"); len(got) != 0 {
		t.Errorf("native artist got a mirrored want: %+v", got)
	}
}

// TestManagerDesireFollowsTheCreditLinks: a collaboration is stored under its primary
// credit, which need not be the Lidarr artist. Keying off that column alone would
// silently skip the album — the same bug that made collaborations flip between
// artists.
func TestManagerDesireFollowsTheCreditLinks(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionArtist{
		MBID: "art-lidarr", Name: "Band", ManagedBy: models.ManagedByLidarr,
	}).Error; err != nil {
		t.Fatalf("artist: %v", err)
	}
	// Primary credit is a different artist; the Lidarr one is credited second.
	if err := db.Create(&models.CollectionReleaseGroup{
		MBID: "rg-1", ArtistMBID: "art-other", Title: "Collab",
		InCatalog: true, CatalogMonitored: true, CatalogReleaseMBID: "rel-1",
	}).Error; err != nil {
		t.Fatalf("release-group: %v", err)
	}
	for i, artistMBID := range []string{"art-other", "art-lidarr"} {
		if err := db.Create(&models.CollectionReleaseGroupArtist{
			ReleaseGroupMBID: "rg-1", ArtistMBID: artistMBID, Position: i,
		}).Error; err != nil {
			t.Fatalf("link %s: %v", artistMBID, err)
		}
	}

	if err := reconcileManagerDesires(db); err != nil {
		t.Fatalf("reconcileManagerDesires: %v", err)
	}
	got := desiresFor(t, db, "rg-1")
	if len(got) != 1 || got[0].ArtistMBID != "art-lidarr" {
		t.Fatalf("want the mirrored want recorded against the managed artist, got %+v", got)
	}
}

// TestRebuildLeavesManagerDesiresAlone: the two reconciliation passes each own one
// kind of row. The auto pass prunes an edition whose files are gone — a mirrored want
// names an edition that is usually *not* owned yet, so treating them alike would
// delete the manager's selection on the next scan.
func TestRebuildLeavesManagerDesiresAlone(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)

	if err := db.Create(&models.CollectionArtist{
		MBID: "art-1", Name: "Band", ManagedBy: models.ManagedByLidarr,
	}).Error; err != nil {
		t.Fatalf("artist: %v", err)
	}
	if err := db.Create(&models.CollectionDesire{
		ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1", ReleaseMBID: "rel-1",
		Source: models.DesireSourceManager,
	}).Error; err != nil {
		t.Fatalf("desire: %v", err)
	}

	// Nothing is owned, which is the auto pass at its most destructive.
	if _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	got := desiresFor(t, db, "rg-1")
	if len(got) != 1 || got[0].Source != models.DesireSourceManager {
		t.Errorf("rebuild disturbed the mirrored want: %+v", got)
	}
}

// TestBackfillDesireSources: rows written before provenance existed must land on the
// right side of it. The legacy boolean survives AutoMigrate, so it is read where it
// is present; anything else is manual, which is both the old meaning of auto=false
// and the safe reading — a derived row may be re-pointed, an authored one may not.
func TestBackfillDesireSources(t *testing.T) {
	db := testDB(t)
	if err := db.Exec("ALTER TABLE collection_desires ADD COLUMN auto numeric").Error; err != nil {
		t.Fatalf("add legacy column: %v", err)
	}
	now := time.Now()
	if err := db.Exec(
		"INSERT INTO collection_desires (id, created_at, updated_at, artist_mb_id, release_group_mb_id, release_mb_id, auto) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?)",
		"11111111-1111-1111-1111-111111111111", now, now, "art-1", "rg-1", "rel-1", true,
		"22222222-2222-2222-2222-222222222222", now, now, "art-1", "rg-2", "rel-2", false,
	).Error; err != nil {
		t.Fatalf("seed legacy rows: %v", err)
	}

	if err := BackfillDesireSources(db); err != nil {
		t.Fatalf("BackfillDesireSources: %v", err)
	}

	if got := desiresFor(t, db, "rg-1"); len(got) != 1 || got[0].Source != models.DesireSourceAuto {
		t.Errorf("legacy auto row = %+v, want source auto", got)
	}
	if got := desiresFor(t, db, "rg-2"); len(got) != 1 || got[0].Source != models.DesireSourceManual {
		t.Errorf("legacy hand-pinned row = %+v, want source manual", got)
	}

	// Idempotent, and it never re-labels a row that already has a source.
	if err := BackfillDesireSources(db); err != nil {
		t.Fatalf("second BackfillDesireSources: %v", err)
	}
	if got := desiresFor(t, db, "rg-1"); got[0].Source != models.DesireSourceAuto {
		t.Errorf("second run relabelled an auto row: %+v", got)
	}
}

// TestBackfillDesireSourcesWithoutTheLegacyColumn: a fresh install has no `auto`
// column at all, so the backfill must not depend on one existing.
func TestBackfillDesireSourcesWithoutTheLegacyColumn(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionDesire{ArtistMBID: "art-1", ReleaseGroupMBID: "rg-1"}).Error; err != nil {
		t.Fatalf("desire: %v", err)
	}
	if err := BackfillDesireSources(db); err != nil {
		t.Fatalf("BackfillDesireSources: %v", err)
	}
	if got := desiresFor(t, db, "rg-1"); len(got) != 1 || got[0].Source != models.DesireSourceManual {
		t.Errorf("sourceless row = %+v, want source manual", got)
	}
}

package collection

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
)

// Passes narrowed to some Lidarr managers (SyncOptions.ManagerIDs,
// RepairOptions.ManagerIDs): a processing run of one library reaches only the manager
// governing it, so pressing Process on a library one Lidarr governs leaves another
// alone (issue #14).

// untouchable is a Lidarr that fails the test on any request, and counts them.
func untouchable(t *testing.T) string {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(func() {
		srv.Close()
		if n := hits.Load(); n != 0 {
			t.Errorf("a manager outside the pass got %d request(s)", n)
		}
	})
	return srv.URL
}

// TestSyncScopedToOneManagerReadsOnlyIt: a pass named one manager asks no other, and
// what it read is mirrored as usual.
func TestSyncScopedToOneManagerReadsOnlyIt(t *testing.T) {
	db := twoManagers(t,
		lidarrListing(t, bothListArtist, []models.LidarrAlbum{{ID: 10, ForeignAlbumID: "rg-a", Title: "A", AlbumType: "Album"}}, false),
		untouchable(t))

	stats, err := SyncLidarrWith(db, SyncOptions{ManagerIDs: []uuid.UUID{managerID(t, db, "First")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Failures) != 0 {
		t.Errorf("failures = %v — the out-of-scope manager must not even be tried", stats.Failures)
	}
	if stats.ArtistsSynced != 1 || !catalogOf(t, db, "rg-a").InCatalog {
		t.Errorf("stats = %+v; the in-scope listing must still be mirrored", stats)
	}
}

// TestSyncScopedKeepsWhatAnotherManagerHolds: an artist the out-of-scope manager was
// last seen holding may have albums only it lists, so a partial pass writes what it
// read and retires nothing. An artist only the in-scope manager holds is retired
// exactly as an unscoped pass would.
func TestSyncScopedKeepsWhatAnotherManagerHolds(t *testing.T) {
	for _, held := range []bool{true, false} {
		db := twoManagers(t,
			lidarrListing(t, bothListArtist, []models.LidarrAlbum{{ID: 10, ForeignAlbumID: "rg-a", Title: "A", AlbumType: "Album"}}, false),
			untouchable(t))
		if held {
			if err := db.Create(&models.ManagerLink{ManagerID: managerID(t, db, "Second"),
				EntityType: models.ManagerLinkArtist, MBID: "art-1", ExternalID: 1}).Error; err != nil {
				t.Fatal(err)
			}
		}
		if err := db.Create(&models.CollectionReleaseGroup{MBID: "rg-b", ArtistMBID: "art-1", Title: "B",
			InCatalog: true, CatalogOwnedTracks: 4, CatalogTotalTracks: 4}).Error; err != nil {
			t.Fatal(err)
		}

		if _, err := SyncLidarrWith(db, SyncOptions{ManagerIDs: []uuid.UUID{managerID(t, db, "First")}}); err != nil {
			t.Fatal(err)
		}
		if !catalogOf(t, db, "rg-a").InCatalog {
			t.Errorf("held=%v: the in-scope listing was not written", held)
		}
		if got := catalogOf(t, db, "rg-b").InCatalog; got != held {
			t.Errorf("held=%v: rg-b in catalog = %v; want it kept only while the other manager holds the artist", held, got)
		}
	}
}

// TestSyncScopedReportsNoUnknownArtists: an artist the in-scope manager does not list
// may be in the one the pass never asked, so a partial pass marks nobody missing and
// clears no catalog — the next pass over every manager decides.
func TestSyncScopedReportsNoUnknownArtists(t *testing.T) {
	db := twoManagers(t, lidarrListing(t, []models.LidarrArtist{}, nil, false), untouchable(t))
	if err := db.Create(&models.CollectionReleaseGroup{MBID: "rg-b", ArtistMBID: "art-1", Title: "B",
		InCatalog: true, CatalogOwnedTracks: 4, CatalogTotalTracks: 4}).Error; err != nil {
		t.Fatal(err)
	}

	stats, err := SyncLidarrWith(db, SyncOptions{ManagerIDs: []uuid.UUID{managerID(t, db, "First")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Unknown) != 0 {
		t.Errorf("unknown = %v, want none from a partial pass", stats.Unknown)
	}
	var artist models.CollectionArtist
	if err := db.First(&artist, "mb_id = ?", "art-1").Error; err != nil {
		t.Fatal(err)
	}
	if artist.ManagerMissingAt != nil {
		t.Error("a partial pass marked the artist missing from the managers")
	}
	if !catalogOf(t, db, "rg-b").InCatalog {
		t.Error("a partial pass cleared the catalog of an artist it could not judge")
	}
}

// TestSyncScopedToEveryManagerIsUnscoped: naming every enabled manager leaves nothing
// out, so the pass draws its usual conclusions — here, that the artist is unknown.
func TestSyncScopedToEveryManagerIsUnscoped(t *testing.T) {
	db := twoManagers(t, lidarrListing(t, []models.LidarrArtist{}, nil, false), lidarrListing(t, []models.LidarrArtist{}, nil, false))

	stats, err := SyncLidarrWith(db, SyncOptions{ManagerIDs: []uuid.UUID{managerID(t, db, "First"), managerID(t, db, "Second")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Unknown) != 1 || stats.Unknown[0] != "art-1" {
		t.Errorf("unknown = %v, want [art-1]", stats.Unknown)
	}
}

// TestSyncScopedToNoEnabledManager: a pass naming only managers that are not enabled
// Lidarrs has nothing to read, and says so the way an install without one does.
func TestSyncScopedToNoEnabledManager(t *testing.T) {
	db := twoManagers(t, untouchable(t), untouchable(t))
	stats, err := SyncLidarrWith(db, SyncOptions{ManagerIDs: []uuid.UUID{uuid.New()}})
	if err != nil {
		t.Fatal(err)
	}
	if stats.EmptyReason != SyncEmptyNoManager {
		t.Errorf("empty reason = %q, want %q", stats.EmptyReason, SyncEmptyNoManager)
	}
}

// TestRepairScopedToOneManager: the repair asks only the managers it was given — and
// its re-sync after the refresh reads only those too.
func TestRepairScopedToOneManager(t *testing.T) {
	l := &repairLidarr{status: "completed", albums: []models.LidarrAlbum{
		{ForeignAlbumID: "rg-live", Title: "Heatstroke", AlbumType: "Album", Monitored: true},
	}}
	db := repairFixture(t, l)
	if err := db.Create(&models.Manager{Name: "Other", Type: models.ManagerTypeLidarr, Enabled: true,
		LidarrBaseURL: untouchable(t), LidarrAPIKey: "k"}).Error; err != nil {
		t.Fatal(err)
	}

	stats, err := RepairGhostReleaseGroupsWith(db, RepairOptions{ManagerIDs: []uuid.UUID{managerID(t, db, "Lidarr")}})
	if err != nil {
		t.Fatal(err)
	}
	if l.refreshes.Load() != 1 || stats.Artists != 1 {
		t.Errorf("stats = %+v (refreshes %d), want the in-scope manager asked once", stats, l.refreshes.Load())
	}
	if len(stats.Failures) != 0 {
		t.Errorf("failures = %v", stats.Failures)
	}
	if !catalogOf(t, db, "rg-live").InCatalog {
		t.Error("the re-sync after the refresh must still mirror the in-scope listing")
	}
}

package collection

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
)

// departedFixture is syncFixture's counterpart for the artist Lidarr has *stopped*
// listing: both collection artists carry a mirrored album, and the mock lists only
// one of them. `listed` is what the artist endpoint returns.
func departedFixture(t *testing.T, listed []models.LidarrArtist) *gorm.DB {
	t.Helper()
	db := syncFixture(t, listed, false)
	for _, rg := range []models.CollectionReleaseGroup{
		{MBID: "rg-1", ArtistMBID: "art-known", Title: "One",
			InCatalog: true, CatalogOwnedTracks: 3, CatalogTotalTracks: 3, CatalogMonitored: true, CatalogReleaseMBID: "rel-1"},
		{MBID: "rg-departed", ArtistMBID: "art-stranger", Title: "Gone",
			InCatalog: true, CatalogOwnedTracks: 7, CatalogTotalTracks: 7, CatalogMonitored: true, CatalogReleaseMBID: "rel-2"},
	} {
		if err := db.Create(&rg).Error; err != nil {
			t.Fatalf("release-group: %v", err)
		}
	}
	return db
}

func catalogOf(t *testing.T, db *gorm.DB, mbid string) models.CollectionReleaseGroup {
	t.Helper()
	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", mbid).First(&rg).Error; err != nil {
		t.Fatalf("%s: %v", mbid, err)
	}
	return rg
}

// TestSyncClearsTheCatalogOfAnArtistLidarrDropped: deleting an artist from Lidarr
// must retire the catalog view Lidarr wrote for them.
//
// The reset lives inside the per-Lidarr-artist loop, so it only ever runs for artists
// the manager still lists. An artist the manager has *deleted* never reaches it: the
// pass correctly reports them as Unknown ("not in Lidarr") and correctly leaves the
// disk columns alone — and leaves `in_catalog`, the have/total counts and the
// monitored edition exactly as they were, forever. The artist page then says the
// manager holds files for an album the manager has never heard of.
func TestSyncClearsTheCatalogOfAnArtistLidarrDropped(t *testing.T) {
	db := departedFixture(t, []models.LidarrArtist{{ID: 1, ForeignArtistID: "art-known", Name: "Known"}})

	stats, err := SyncLidarr(db)
	if err != nil {
		t.Fatalf("SyncLidarr: %v", err)
	}
	if len(stats.Unknown) != 1 || stats.Unknown[0] != "art-stranger" {
		t.Fatalf("unknown = %v, want [art-stranger]", stats.Unknown)
	}

	gone := catalogOf(t, db, "rg-departed")
	if gone.InCatalog || gone.CatalogOwnedTracks != 0 || gone.CatalogTotalTracks != 0 ||
		gone.CatalogMonitored || gone.CatalogReleaseMBID != "" {
		t.Errorf("catalog state survived the artist's deletion from Lidarr: %+v", gone)
	}

	// The artist Lidarr still holds keeps everything, re-mirrored from the response.
	if kept := catalogOf(t, db, "rg-1"); !kept.InCatalog {
		t.Errorf("the listed artist's catalog was cleared: %+v", kept)
	}
}

// TestSyncScopedToADepartedArtistClearsIt is the same failure from the button the user
// actually presses: *Sync with Lidarr* on the artist page, scoped to one artist that
// the manager no longer has. A pass that reaches no artist at all must still retire
// what it was asked about.
func TestSyncScopedToADepartedArtistClearsIt(t *testing.T) {
	db := departedFixture(t, []models.LidarrArtist{{ID: 1, ForeignArtistID: "art-known", Name: "Known"}})

	if _, err := SyncLidarrWith(db, SyncOptions{ArtistMBID: "art-stranger", IgnoreCache: true}); err != nil {
		t.Fatalf("SyncLidarrWith: %v", err)
	}
	if gone := catalogOf(t, db, "rg-departed"); gone.InCatalog || gone.CatalogOwnedTracks != 0 {
		t.Errorf("scoped sync left the catalog view of an artist Lidarr does not have: %+v", gone)
	}
	// Out of scope, untouched.
	if kept := catalogOf(t, db, "rg-1"); !kept.InCatalog || kept.CatalogOwnedTracks != 3 {
		t.Errorf("scoped sync touched another artist: %+v", kept)
	}
}

// TestSyncKeepsTheCatalogWhenLidarrCouldNotBeAsked is the guard the fix above must not
// break, and it is the same rule stats.Unknown already follows: an artist missing from
// a list that was never fetched is not missing. Wiping their catalog because Lidarr was
// restarting would turn an outage into "your manager forgot your library".
func TestSyncKeepsTheCatalogWhenLidarrCouldNotBeAsked(t *testing.T) {
	db := departedFixture(t, nil)
	// A second manager that fails, so the pass records a failure while the first one
	// still lists nobody.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/artist") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode([]models.LidarrAlbum{})
	}))
	defer srv.Close()
	if err := db.Create(&models.Manager{Name: "Down", Type: models.ManagerTypeLidarr, Enabled: true,
		LidarrBaseURL: srv.URL, LidarrAPIKey: "k"}).Error; err != nil {
		t.Fatalf("manager: %v", err)
	}

	stats, err := SyncLidarr(db)
	if err != nil {
		t.Fatalf("SyncLidarr: %v", err)
	}
	if len(stats.Failures) == 0 {
		t.Fatalf("expected the failing manager to be reported")
	}
	if len(stats.Unknown) != 0 {
		t.Errorf("unknown = %v, want none while a manager failed", stats.Unknown)
	}
	if gone := catalogOf(t, db, "rg-departed"); !gone.InCatalog || gone.CatalogOwnedTracks != 7 {
		t.Errorf("a failed lookup cleared a catalog view: %+v", gone)
	}
}

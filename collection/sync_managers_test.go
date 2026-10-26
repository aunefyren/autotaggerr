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

// lidarrListing is one fake Lidarr instance: the artists it lists, and the albums it
// returns for every one of them. failAlbums makes the album listing answer 500.
func lidarrListing(t *testing.T, artists []models.LidarrArtist, albums []models.LidarrAlbum, failAlbums bool) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/artist"):
			_ = json.NewEncoder(w).Encode(artists)
		case strings.HasPrefix(r.URL.Path, "/api/v1/album"):
			if failAlbums {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(albums)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// twoManagers is a collection with one Lidarr-managed artist and two Lidarr managers
// at the given URLs.
func twoManagers(t *testing.T, first, second string) *gorm.DB {
	t.Helper()
	db := testDB(t)
	for _, m := range []models.Manager{
		{Name: "First", Type: models.ManagerTypeLidarr, Enabled: true, LidarrBaseURL: first, LidarrAPIKey: "k"},
		{Name: "Second", Type: models.ManagerTypeLidarr, Enabled: true, LidarrBaseURL: second, LidarrAPIKey: "k"},
	} {
		if err := db.Create(&m).Error; err != nil {
			t.Fatalf("manager: %v", err)
		}
	}
	if err := db.Create(&models.CollectionArtist{MBID: "art-1", Name: "Artist", ManagedBy: models.ManagedByLidarr}).Error; err != nil {
		t.Fatalf("artist: %v", err)
	}
	return db
}

var bothListArtist = []models.LidarrArtist{{ID: 1, ForeignArtistID: "art-1", Name: "Artist"}}

// TestSyncKeepsEveryManagersCatalog: two Lidarr managers both listing an artist each
// contribute to its catalog. The view used to be cleared per manager, so the second
// manager's pass wiped what the first had just mirrored, and an album only the first
// one held dropped out of the catalog on every sync.
//
// Both albums share a title and type on purpose: the first manager's album also looked
// like it had "left" from the second's point of view, and paired with the second's as
// a re-key — a redirect queued between two albums that both exist.
func TestSyncKeepsEveryManagersCatalog(t *testing.T) {
	first := lidarrListing(t, bothListArtist, []models.LidarrAlbum{
		{ID: 10, ForeignAlbumID: "rg-a", Title: "Live", AlbumType: "Album", Monitored: true,
			Statistics: models.LidarrAlbumStatistics{TrackFileCount: 5, TrackCount: 10}},
	}, false)
	second := lidarrListing(t, bothListArtist, []models.LidarrAlbum{
		{ID: 20, ForeignAlbumID: "rg-b", Title: "Live", AlbumType: "Album"},
	}, false)
	db := twoManagers(t, first, second)

	for pass := 1; pass <= 2; pass++ {
		stats, err := SyncLidarr(db)
		if err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		if len(stats.Rekeyed) != 0 {
			t.Fatalf("pass %d recorded re-keys %v between albums both managers list", pass, stats.Rekeyed)
		}
		for _, id := range []string{"rg-a", "rg-b"} {
			if rg := catalogOf(t, db, id); !rg.InCatalog {
				t.Errorf("pass %d: %s dropped out of the catalog: %+v", pass, id, rg)
			}
		}
		if rg := catalogOf(t, db, "rg-a"); rg.CatalogOwnedTracks != 5 || !rg.CatalogMonitored {
			t.Errorf("pass %d: the first manager's counts were overwritten: %+v", pass, rg)
		}
	}
	var n int64
	db.Model(&models.MusicbrainzMigration{}).Count(&n)
	if n != 0 {
		t.Errorf("%d migrations recorded, want none", n)
	}
}

// TestSyncMergesAnAlbumBothManagersList: the same album in two managers is one
// release-group row, so one listing has to win. The one holding more files does —
// the order the managers happen to be read in must not decide what the page says.
func TestSyncMergesAnAlbumBothManagersList(t *testing.T) {
	full := models.LidarrAlbum{ID: 10, ForeignAlbumID: "rg-a", Title: "Album", AlbumType: "Album", Monitored: true,
		Statistics: models.LidarrAlbumStatistics{TrackFileCount: 10, TrackCount: 10}}
	empty := models.LidarrAlbum{ID: 20, ForeignAlbumID: "rg-a", Title: "Album", AlbumType: "Album",
		Statistics: models.LidarrAlbumStatistics{TrackFileCount: 0, TrackCount: 10}}

	for _, order := range [][2]models.LidarrAlbum{{full, empty}, {empty, full}} {
		db := twoManagers(t,
			lidarrListing(t, bothListArtist, []models.LidarrAlbum{order[0]}, false),
			lidarrListing(t, bothListArtist, []models.LidarrAlbum{order[1]}, false))
		if _, err := SyncLidarr(db); err != nil {
			t.Fatal(err)
		}
		if rg := catalogOf(t, db, "rg-a"); rg.CatalogOwnedTracks != 10 || !rg.CatalogMonitored {
			t.Errorf("the emptier listing won: %+v", rg)
		}
	}
}

// TestSyncDoesNotClearOnAPartialListing: when one manager could not list an artist's
// albums, what the others listed is written but nothing is retired — an album missing
// from a listing that was never fetched is not missing.
func TestSyncDoesNotClearOnAPartialListing(t *testing.T) {
	db := twoManagers(t,
		lidarrListing(t, bothListArtist, []models.LidarrAlbum{{ID: 10, ForeignAlbumID: "rg-a", Title: "A", AlbumType: "Album"}}, false),
		lidarrListing(t, bothListArtist, nil, true))
	if err := db.Create(&models.CollectionReleaseGroup{MBID: "rg-b", ArtistMBID: "art-1", Title: "B",
		InCatalog: true, CatalogOwnedTracks: 4, CatalogTotalTracks: 4}).Error; err != nil {
		t.Fatal(err)
	}

	stats, err := SyncLidarr(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.FailedArtists) != 1 {
		t.Errorf("failed artists = %+v, want the second manager's", stats.FailedArtists)
	}
	if rg := catalogOf(t, db, "rg-a"); !rg.InCatalog {
		t.Errorf("the listing that succeeded was not written: %+v", rg)
	}
	if rg := catalogOf(t, db, "rg-b"); !rg.InCatalog || rg.CatalogOwnedTracks != 4 {
		t.Errorf("an album the failed manager may hold was retired: %+v", rg)
	}
}

// TestSyncOfOneArtistReachesTheirCollaborators: an album by X & Y that Lidarr holds
// under X. Syncing Y used to ask Lidarr about Y alone, which does not list the album,
// so its counts never moved — the same button worked on X's page and silently did
// nothing on Y's.
func TestSyncOfOneArtistReachesTheirCollaborators(t *testing.T) {
	// Albums per Lidarr artist: X holds the collaboration, Y holds nothing.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/artist"):
			_ = json.NewEncoder(w).Encode([]models.LidarrArtist{{ID: 1, ForeignArtistID: "art-x", Name: "X"}, {ID: 2, ForeignArtistID: "art-y", Name: "Y"}})
		case strings.HasPrefix(r.URL.Path, "/api/v1/album"):
			if r.URL.Query().Get("artistId") == "1" {
				_ = json.NewEncoder(w).Encode([]models.LidarrAlbum{{ID: 10, ForeignAlbumID: "rg-shared", Title: "Together", AlbumType: "Album",
					Statistics: models.LidarrAlbumStatistics{TrackFileCount: 16, TrackCount: 16}}})
				return
			}
			_ = json.NewEncoder(w).Encode([]models.LidarrAlbum{})
		}
	}))
	t.Cleanup(srv.Close)

	db := testDB(t)
	if err := db.Create(&models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true, LidarrBaseURL: srv.URL, LidarrAPIKey: "k"}).Error; err != nil {
		t.Fatal(err)
	}
	for _, a := range []models.CollectionArtist{
		{MBID: "art-x", Name: "X", ManagedBy: models.ManagedByLidarr},
		{MBID: "art-y", Name: "Y", ManagedBy: models.ManagedByLidarr},
		{MBID: "art-z", Name: "Unrelated", ManagedBy: models.ManagedByLidarr},
	} {
		if err := db.Create(&a).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&models.CollectionReleaseGroup{MBID: "rg-shared", ArtistMBID: "art-x", Title: "Together",
		InCatalog: true, CatalogOwnedTracks: 1, CatalogTotalTracks: 16}).Error; err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"art-x", "art-y"} {
		if err := db.Create(&models.CollectionReleaseGroupArtist{ReleaseGroupMBID: "rg-shared", ArtistMBID: id, Position: i}).Error; err != nil {
			t.Fatal(err)
		}
	}

	stats, err := SyncLidarrWith(db, SyncOptions{ArtistMBID: "art-y"})
	if err != nil {
		t.Fatal(err)
	}
	if rg := catalogOf(t, db, "rg-shared"); rg.CatalogOwnedTracks != 16 {
		t.Errorf("the shared album still reads %d/16 after syncing Y", rg.CatalogOwnedTracks)
	}
	if len(stats.CoCredited) != 1 || stats.CoCredited[0] != "art-x" {
		t.Errorf("co-credited = %v, want [art-x] and nobody unrelated", stats.CoCredited)
	}
	if !strings.Contains(SyncSummaryLine(stats), "with 1 co-credited artist") {
		t.Errorf("summary = %q", SyncSummaryLine(stats))
	}
}

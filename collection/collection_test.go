package collection

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/database"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func init() {
	logger.Log = logrus.New()
	logger.Log.SetOutput(io.Discard)
}

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return db
}

// TestFollowWantsDefaults: with no follow settings, following an artist wants
// studio albums and EPs only — a full discography is mostly singles and reissues,
// which would make the missing list unreadable.
func TestFollowWantsDefaults(t *testing.T) {
	artist := models.CollectionArtist{}
	cases := []struct {
		primary   string
		secondary []string
		want      bool
	}{
		{"Album", nil, true},
		{"EP", nil, true},
		{"Album", []string{"Live"}, false},        // live album filtered out
		{"Album", []string{"Compilation"}, false}, // compilation filtered out
		{"Single", nil, false},
		{"Broadcast", nil, false},
	}
	for _, c := range cases {
		if got := FollowWants(artist, c.primary, c.secondary); got != c.want {
			t.Errorf("FollowWants(default, %q,%v) = %v, want %v", c.primary, c.secondary, got, c.want)
		}
	}
}

// TestFollowWantsConfigured: the per-artist settings actually govern. Someone who
// follows an artist for their singles must get singles.
func TestFollowWantsConfigured(t *testing.T) {
	singles := models.CollectionArtist{FollowTypes: "Single"}
	if !FollowWants(singles, "Single", nil) {
		t.Error("configured Single should be wanted")
	}
	if FollowWants(singles, "Album", nil) {
		t.Error("Album should not be wanted when only Single is followed")
	}

	// Secondary types stay excluded until explicitly allowed.
	live := models.CollectionArtist{FollowTypes: "Album"}
	if FollowWants(live, "Album", []string{"Live"}) {
		t.Error("live album should be excluded by default")
	}
	live.FollowSecondary = true
	if !FollowWants(live, "Album", []string{"Live"}) {
		t.Error("live album should be included once secondary types are allowed")
	}

	// Case and spacing in the stored CSV must not matter.
	messy := models.CollectionArtist{FollowTypes: " album , ep "}
	if !FollowWants(messy, "Album", nil) || !FollowWants(messy, "EP", nil) {
		t.Error("follow types should be matched case- and space-insensitively")
	}
}

func TestFollowWantsStored(t *testing.T) {
	artist := models.CollectionArtist{}
	if FollowWantsStored(artist, "Album", "Live") {
		t.Error("stored secondary types should exclude by default")
	}
	if !FollowWantsStored(artist, "Album", "") {
		t.Error("stored album with no secondary types should be wanted")
	}
}

func TestManagedByLabel(t *testing.T) {
	if got := managedByLabel(map[string]bool{"lidarr": true, "autotaggerr": true}); got != models.ManagedByMixed {
		t.Errorf("mixed = %q", got)
	}
	if got := managedByLabel(map[string]bool{"lidarr": true}); got != models.ManagedByLidarr {
		t.Errorf("lidarr = %q", got)
	}
	if got := managedByLabel(map[string]bool{"autotaggerr": true}); got != models.ManagedByAutotaggerr {
		t.Errorf("autotaggerr = %q", got)
	}
}

// TestRebuildPresent seeds a cached release (via the DB) and an owned item, then
// asserts Rebuild materializes the artist + owned release-group.
func TestRebuildPresent(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)

	release := models.MusicBrainzReleaseResponse{
		ID:           "rel-1",
		Title:        "Album One",
		ArtistCredit: []models.ArtistCredit{{Name: "The Band", Artist: models.Artist{ID: "art-1", Name: "The Band"}}},
		ReleaseGroup: models.ReleaseGroup{ID: "rg-1", Title: "Album One", PrimaryType: "Album"},
		// Three tracks on the release; we'll own only one -> a partial album.
		Media: []models.MusicBrainzMedia{{Tracks: []models.Track{{ID: "t1"}, {ID: "t2"}, {ID: "t3"}}}},
	}
	payload, _ := json.Marshal(release)
	if err := db.Create(&models.MusicbrainzReleaseCache{MBID: "rel-1", Payload: string(payload), FetchedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}).Error; err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	if err := modules.MusicbrainzLoadCache(); err != nil {
		t.Fatalf("load cache: %v", err)
	}

	lib := models.Library{Name: "L", Path: "/m"}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	if err := db.Create(&models.LibraryItem{LibraryID: lib.ID, Path: "/m/a.flac", Status: "ok", MBReleaseID: "rel-1"}).Error; err != nil {
		t.Fatalf("item: %v", err)
	}

	artists, owned, err := Rebuild(db)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if artists != 1 || owned != 1 {
		t.Fatalf("Rebuild counts = %d artists, %d owned; want 1, 1", artists, owned)
	}

	var a models.CollectionArtist
	if err := db.Where("mb_id = ?", "art-1").First(&a).Error; err != nil {
		t.Fatalf("artist not created: %v", err)
	}
	if a.Name != "The Band" || a.ManagedBy != models.ManagedByAutotaggerr {
		t.Errorf("artist = %+v", a)
	}

	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", "rg-1").First(&rg).Error; err != nil {
		t.Fatalf("release-group not created: %v", err)
	}
	if !rg.Owned || rg.Title != "Album One" || rg.ArtistMBID != "art-1" {
		t.Errorf("release-group = %+v", rg)
	}
	// Partial: we own 1 of the release's 3 tracks.
	if rg.OwnedTracks != 1 || rg.TotalTracks != 3 {
		t.Errorf("track completeness = %d/%d, want 1/3", rg.OwnedTracks, rg.TotalTracks)
	}
}

// TestSyncLidarr mirrors a mock Lidarr's albums onto the collection: owned,
// missing, and partial albums map to the right ownership + track counts.
func TestSyncLidarr(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/artist"):
			_ = json.NewEncoder(w).Encode([]models.LidarrArtist{{ID: 1, ForeignArtistID: "art-1", Name: "Band"}})
		case strings.HasPrefix(r.URL.Path, "/api/v1/album"):
			_ = json.NewEncoder(w).Encode([]models.LidarrAlbum{
				{ForeignAlbumID: "rg-owned", Title: "Owned", AlbumType: "Album", Statistics: models.LidarrAlbumStatistics{TrackCount: 10, TrackFileCount: 10}},
				{ForeignAlbumID: "rg-missing", Title: "Missing", AlbumType: "Album", Statistics: models.LidarrAlbumStatistics{TrackCount: 12, TrackFileCount: 0}},
				{ForeignAlbumID: "rg-partial", Title: "Partial", AlbumType: "Album", Statistics: models.LidarrAlbumStatistics{TrackCount: 12, TrackFileCount: 5}},
			})
		}
	}))
	defer srv.Close()

	db := testDB(t)
	if err := db.Create(&models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true, LidarrBaseURL: srv.URL, LidarrAPIKey: "k"}).Error; err != nil {
		t.Fatalf("manager: %v", err)
	}
	if err := db.Create(&models.CollectionArtist{MBID: "art-1", Name: "Band", ManagedBy: models.ManagedByLidarr}).Error; err != nil {
		t.Fatalf("artist: %v", err)
	}

	artists, groups, err := SyncLidarr(db)
	if err != nil {
		t.Fatalf("SyncLidarr: %v", err)
	}
	if artists != 1 || groups != 3 {
		t.Fatalf("SyncLidarr = %d artists, %d groups; want 1, 3", artists, groups)
	}

	get := func(mbid string) models.CollectionReleaseGroup {
		var rg models.CollectionReleaseGroup
		if err := db.Where("mb_id = ?", mbid).First(&rg).Error; err != nil {
			t.Fatalf("%s: %v", mbid, err)
		}
		return rg
	}
	// Lidarr's counts land in the catalog columns only; the disk columns stay zero
	// because no scan has seen these files.
	if o := get("rg-owned"); !o.InCatalog || o.CatalogOwnedTracks != 10 || o.CatalogTotalTracks != 10 {
		t.Errorf("owned catalog = %+v", o)
	} else if o.Owned || o.OwnedTracks != 0 {
		t.Errorf("Lidarr must not write the disk view: %+v", o)
	}
	if m := get("rg-missing"); !m.InCatalog || m.CatalogOwnedTracks != 0 || m.CatalogTotalTracks != 12 {
		t.Errorf("missing = %+v", m)
	}
	if p := get("rg-partial"); p.CatalogOwnedTracks != 5 || p.CatalogTotalTracks != 12 {
		t.Errorf("partial = %+v", p)
	}
}

// TestRebuildPreservesCatalog is the ordering guarantee: a Rebuild after a Lidarr
// sync must not wipe the catalog view, and vice versa. Before the split these two
// wrote the same columns, so whichever ran last decided what the UI showed.
func TestRebuildPreservesCatalog(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)

	if err := db.Create(&models.CollectionReleaseGroup{
		MBID: "rg-1", ArtistMBID: "art-1", Title: "Album One",
		InCatalog: true, CatalogOwnedTracks: 3, CatalogTotalTracks: 21, CatalogMonitored: true,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", "rg-1").First(&rg).Error; err != nil {
		t.Fatalf("row gone: %v", err)
	}
	if !rg.InCatalog || rg.CatalogOwnedTracks != 3 || rg.CatalogTotalTracks != 21 || !rg.CatalogMonitored {
		t.Errorf("Rebuild clobbered the catalog view: %+v", rg)
	}
}

// TestRebuildClearsStaleDiskCounts: a row that stops being owned must not keep its
// old track counts, or it renders as "missing" while still showing 10/12.
func TestRebuildClearsStaleDiskCounts(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)

	if err := db.Create(&models.CollectionReleaseGroup{
		MBID: "rg-1", ArtistMBID: "art-1", Title: "Gone",
		Owned: true, OwnedTracks: 10, TotalTracks: 12,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, _, err := Rebuild(db); err != nil { // no library_items -> nothing owned
		t.Fatalf("Rebuild: %v", err)
	}

	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", "rg-1").First(&rg).Error; err != nil {
		t.Fatalf("row gone: %v", err)
	}
	if rg.Owned || rg.OwnedTracks != 0 || rg.TotalTracks != 0 {
		t.Errorf("stale disk counts survived: %+v", rg)
	}
}

// TestDiscrepancy covers the disagreements worth surfacing, including the real case
// that motivated the split: Lidarr reporting 3/21 for an album whose files are all
// present because Lidarr had not rescanned the disk.
func TestDiscrepancy(t *testing.T) {
	cases := []struct {
		name       string
		rg         models.CollectionReleaseGroup
		hasCatalog bool
		want       string
	}{
		{
			name: "lidarr needs a rescan",
			rg: models.CollectionReleaseGroup{
				Owned: true, OwnedTracks: 21, TotalTracks: 21,
				InCatalog: true, CatalogOwnedTracks: 3, CatalogTotalTracks: 21,
			},
			hasCatalog: true,
			want:       models.DiscrepancyStaleCatalog,
		},
		{
			name:       "files lidarr has no album for",
			rg:         models.CollectionReleaseGroup{Owned: true, OwnedTracks: 5, TotalTracks: 5},
			hasCatalog: true,
			want:       models.DiscrepancyUnmapped,
		},
		{
			name: "lidarr has files we never indexed",
			rg: models.CollectionReleaseGroup{
				Owned: true, OwnedTracks: 2, TotalTracks: 12,
				InCatalog: true, CatalogOwnedTracks: 12, CatalogTotalTracks: 12,
			},
			hasCatalog: true,
			want:       models.DiscrepancyNotIndexed,
		},
		{
			name: "agreement",
			rg: models.CollectionReleaseGroup{
				Owned: true, OwnedTracks: 12, TotalTracks: 12,
				InCatalog: true, CatalogOwnedTracks: 12, CatalogTotalTracks: 12,
			},
			hasCatalog: true,
			want:       models.DiscrepancyNone,
		},
		{
			name:       "wanted album nobody owns",
			rg:         models.CollectionReleaseGroup{InCatalog: true, CatalogTotalTracks: 12},
			hasCatalog: true,
			want:       models.DiscrepancyNone,
		},
		{
			name:       "native discovery has no counts to compare",
			rg:         models.CollectionReleaseGroup{Owned: true, OwnedTracks: 3, TotalTracks: 10, InCatalog: true},
			hasCatalog: true,
			want:       models.DiscrepancyNone,
		},
		{
			// An unmonitored native artist has no catalog at all; flagging every
			// album it owns as unmapped would make the whole signal noise.
			name:       "no catalog to compare against",
			rg:         models.CollectionReleaseGroup{Owned: true, OwnedTracks: 5, TotalTracks: 5},
			hasCatalog: false,
			want:       models.DiscrepancyNone,
		},
	}
	for _, c := range cases {
		if got := c.rg.Discrepancy(c.hasCatalog); got != c.want {
			t.Errorf("%s: Discrepancy() = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestSyncLidarrDropsRemovedAlbums: albums deleted in Lidarr leave the catalog view,
// but files on disk stay owned (they become unmapped, not deleted).
func TestSyncLidarrDropsRemovedAlbums(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/artist"):
			_ = json.NewEncoder(w).Encode([]models.LidarrArtist{{ID: 1, ForeignArtistID: "art-1", Name: "Band"}})
		case strings.HasPrefix(r.URL.Path, "/api/v1/album"):
			_ = json.NewEncoder(w).Encode([]models.LidarrAlbum{})
		}
	}))
	defer srv.Close()

	db := testDB(t)
	if err := db.Create(&models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true, LidarrBaseURL: srv.URL, LidarrAPIKey: "k"}).Error; err != nil {
		t.Fatalf("manager: %v", err)
	}
	if err := db.Create(&models.CollectionArtist{MBID: "art-1", Name: "Band", ManagedBy: models.ManagedByLidarr}).Error; err != nil {
		t.Fatalf("artist: %v", err)
	}
	if err := db.Create(&models.CollectionReleaseGroup{
		MBID: "rg-1", ArtistMBID: "art-1", Title: "Dropped",
		Owned: true, OwnedTracks: 9, TotalTracks: 9,
		InCatalog: true, CatalogOwnedTracks: 9, CatalogTotalTracks: 9,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, _, err := SyncLidarr(db); err != nil {
		t.Fatalf("SyncLidarr: %v", err)
	}

	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", "rg-1").First(&rg).Error; err != nil {
		t.Fatalf("row gone: %v", err)
	}
	if rg.InCatalog || rg.CatalogOwnedTracks != 0 {
		t.Errorf("catalog state should be cleared: %+v", rg)
	}
	if !rg.Owned || rg.OwnedTracks != 9 {
		t.Errorf("disk view must survive a Lidarr removal: %+v", rg)
	}
	if got := rg.Discrepancy(true); got != models.DiscrepancyUnmapped {
		t.Errorf("Discrepancy() = %q, want unmapped", got)
	}
}

// TestManagedByLabelUnknown: an artist whose provenance cannot be determined must
// report unknown, not native. Reporting native would turn missing information into
// a positive claim about who manages the files.
func TestManagedByLabelUnknown(t *testing.T) {
	if got := managedByLabel(map[string]bool{}); got != models.ManagedByUnknown {
		t.Errorf("empty = %q, want unknown", got)
	}
	if got := managedByLabel(map[string]bool{models.ManagedByUnknown: true}); got != models.ManagedByUnknown {
		t.Errorf("unknown-only = %q, want unknown", got)
	}
}

// TestRebuildReportsUnknownProvenance: a library whose manager row has been deleted
// leaves a dangling ManagerID; its artists must surface as unknown rather than
// silently becoming "native".
func TestRebuildReportsUnknownProvenance(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)

	release := models.MusicBrainzReleaseResponse{
		ID:           "rel-1",
		ArtistCredit: []models.ArtistCredit{{Artist: models.Artist{ID: "art-1", Name: "The Band"}}},
		ReleaseGroup: models.ReleaseGroup{ID: "rg-1", Title: "Album", PrimaryType: "Album"},
		Media:        []models.MusicBrainzMedia{{Tracks: []models.Track{{ID: "t1"}}}},
	}
	payload, _ := json.Marshal(release)
	if err := db.Create(&models.MusicbrainzReleaseCache{
		MBID: "rel-1", Payload: string(payload),
		FetchedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	if err := modules.MusicbrainzLoadCache(); err != nil {
		t.Fatalf("load cache: %v", err)
	}

	dangling := uuid.New() // no Manager row with this ID
	lib := models.Library{Name: "L", Path: "/m", ManagerID: &dangling}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	if err := db.Create(&models.LibraryItem{
		LibraryID: lib.ID, Path: "/m/a.flac", Status: "ok", MBReleaseID: "rel-1",
	}).Error; err != nil {
		t.Fatalf("item: %v", err)
	}

	if _, _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	var a models.CollectionArtist
	if err := db.Where("mb_id = ?", "art-1").First(&a).Error; err != nil {
		t.Fatalf("artist: %v", err)
	}
	if a.ManagedBy != models.ManagedByUnknown {
		t.Errorf("managed_by = %q, want unknown", a.ManagedBy)
	}
}

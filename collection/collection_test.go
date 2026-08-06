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

func TestIdentityEditable(t *testing.T) {
	cases := []struct {
		managedBy string
		want      bool
	}{
		{models.ManagedByAutotaggerr, true},
		{models.ManagedByUnknown, true},
		{models.ManagedByLidarr, false},
		{models.ManagedByMixed, false},
	}
	for _, tc := range cases {
		if got := IdentityEditable(models.CollectionArtist{ManagedBy: tc.managedBy}); got != tc.want {
			t.Errorf("IdentityEditable(%q) = %v, want %v", tc.managedBy, got, tc.want)
		}
	}
}

func TestArtistIdentityEditable(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&models.CollectionArtist{MBID: "lidarr-art", Name: "L", ManagedBy: models.ManagedByLidarr}).Error; err != nil {
		t.Fatalf("create lidarr artist: %v", err)
	}
	if err := db.Create(&models.CollectionArtist{MBID: "native-art", Name: "N", ManagedBy: models.ManagedByAutotaggerr}).Error; err != nil {
		t.Fatalf("create native artist: %v", err)
	}

	// Lidarr-managed: not editable.
	if editable, err := ArtistIdentityEditable(db, "lidarr-art"); err != nil || editable {
		t.Errorf("lidarr artist: editable=%v err=%v, want false/nil", editable, err)
	}
	// Native: editable.
	if editable, err := ArtistIdentityEditable(db, "native-art"); err != nil || !editable {
		t.Errorf("native artist: editable=%v err=%v, want true/nil", editable, err)
	}
	// Unknown artist: nothing governs it yet, so editable.
	if editable, err := ArtistIdentityEditable(db, "ghost"); err != nil || !editable {
		t.Errorf("unknown artist: editable=%v err=%v, want true/nil", editable, err)
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

	stats, err := Rebuild(db)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if stats.Artists != 1 || stats.Owned != 1 {
		t.Fatalf("Rebuild counts = %d artists, %d owned; want 1, 1", stats.Artists, stats.Owned)
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

	if _, err := Rebuild(db); err != nil {
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

	if _, err := Rebuild(db); err != nil { // no library_items -> nothing owned
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
				// A healthy Lidarr album always names the edition it monitors; without
				// one the disagreement is explained by that instead (see models).
				CatalogReleaseMBID: "rel-1",
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
				CatalogReleaseMBID: "rel-1",
			},
			hasCatalog: true,
			want:       models.DiscrepancyNotIndexed,
		},
		{
			// Lidarr with no release selected for the album: its statistics describe an
			// edition nobody chose, so the counts disagree with the files for a reason
			// no rescan changes.
			name: "lidarr has no edition selected",
			rg: models.CollectionReleaseGroup{
				Owned: true, OwnedTracks: 44, TotalTracks: 44,
				InCatalog: true, CatalogOwnedTracks: 7, CatalogTotalTracks: 7,
			},
			hasCatalog: true,
			want:       models.DiscrepancyNoEdition,
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

	if _, err := Rebuild(db); err != nil {
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

// TestFollowGoverns: a stored follow flag only governs while the artist is natively
// managed. This is the guard for the bug where a stale Monitored flag — set before
// the artist became Lidarr-managed — kept producing automatic wants that the artist
// page offered no control to turn off.
func TestFollowGoverns(t *testing.T) {
	cases := map[string]bool{
		models.ManagedByAutotaggerr: true,
		models.ManagedByLidarr:      false,
		models.ManagedByMixed:       false,
		// Unresolvable provenance is not a manager's claim, so the native follow
		// settings are still the only thing that could decide.
		models.ManagedByUnknown: true,
		"":                      true,
	}
	for managedBy, want := range cases {
		artist := models.CollectionArtist{ManagedBy: managedBy, Monitored: true}
		if got := FollowGoverns(artist); got != want {
			t.Errorf("FollowGoverns(managed_by=%q) = %v, want %v", managedBy, got, want)
		}
	}
}

// seedCachedRelease stores a MusicBrainz release in the DB-backed cache and reloads
// it, so Rebuild (which reads only cached releases) can see it.
func seedCachedRelease(t *testing.T, db *gorm.DB, release models.MusicBrainzReleaseResponse) {
	t.Helper()
	payload, err := json.Marshal(release)
	if err != nil {
		t.Fatalf("marshal release: %v", err)
	}
	if err := db.Create(&models.MusicbrainzReleaseCache{
		MBID: release.ID, Payload: string(payload),
		FetchedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	if err := modules.MusicbrainzLoadCache(); err != nil {
		t.Fatalf("load cache: %v", err)
	}
}

// TestRebuildCreditsEveryArtist is the multi-artist case: a release credited to two
// artists belongs to both. Before the link table it belonged to whichever sync wrote
// last — it appeared on one artist's page and vanished from the other's.
func TestRebuildCreditsEveryArtist(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)

	seedCachedRelease(t, db, models.MusicBrainzReleaseResponse{
		ID:    "rel-collab",
		Title: "The Collaboration",
		ArtistCredit: []models.ArtistCredit{
			{Name: "First Artist", Artist: models.Artist{ID: "art-1", Name: "First Artist"}},
			{Name: "Second Artist", Artist: models.Artist{ID: "art-2", Name: "Second Artist"}},
		},
		ReleaseGroup: models.ReleaseGroup{ID: "rg-collab", Title: "The Collaboration", PrimaryType: "Single"},
		Media:        []models.MusicBrainzMedia{{Tracks: []models.Track{{ID: "t1"}}}},
	})

	lib := models.Library{Name: "L", Path: "/m"}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	if err := db.Create(&models.LibraryItem{LibraryID: lib.ID, Path: "/m/collab.flac", Status: "ok", MBReleaseID: "rel-collab"}).Error; err != nil {
		t.Fatalf("item: %v", err)
	}

	stats, err := Rebuild(db)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if stats.Artists != 2 {
		t.Errorf("Rebuild created %d artists, want 2 — the second credit owns these files too", stats.Artists)
	}
	if stats.Owned != 1 {
		t.Errorf("owned release-groups = %d, want 1", stats.Owned)
	}

	// Both artists exist with provenance, not just the first credit.
	for _, mbid := range []string{"art-1", "art-2"} {
		var a models.CollectionArtist
		if err := db.Where("mb_id = ?", mbid).First(&a).Error; err != nil {
			t.Fatalf("artist %s not created: %v", mbid, err)
		}
		if a.ManagedBy != models.ManagedByAutotaggerr {
			t.Errorf("artist %s managed_by = %q, want autotaggerr", mbid, a.ManagedBy)
		}
	}

	// The release-group is on both artists' pages, owned in both cases.
	for _, mbid := range []string{"art-1", "art-2"} {
		groups, err := ReleaseGroupsForArtist(db, mbid)
		if err != nil {
			t.Fatalf("ReleaseGroupsForArtist(%s): %v", mbid, err)
		}
		if len(groups) != 1 || groups[0].MBID != "rg-collab" {
			t.Fatalf("artist %s sees %d release-groups, want the collaboration: %+v", mbid, len(groups), groups)
		}
		if !groups[0].Owned || groups[0].OwnedTracks != 1 {
			t.Errorf("artist %s sees the collaboration as not owned: %+v", mbid, groups[0])
		}
	}

	// The primary credit is still the first artist — used for display and sorting.
	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", "rg-collab").First(&rg).Error; err != nil {
		t.Fatalf("release-group: %v", err)
	}
	if rg.ArtistMBID != "art-1" {
		t.Errorf("primary credit = %q, want art-1", rg.ArtistMBID)
	}

	// And the credit order is recorded, so a featured artist is not presented as the
	// album's author.
	credits, err := ArtistsByReleaseGroup(db, []string{"rg-collab"})
	if err != nil {
		t.Fatalf("ArtistsByReleaseGroup: %v", err)
	}
	if got := credits["rg-collab"]; len(got) != 2 || got[0] != "art-1" || got[1] != "art-2" {
		t.Errorf("credit order = %v, want [art-1 art-2]", got)
	}
}

// TestSyncArtistDoesNotStealPrimaryCredit is the other half of the bug: syncing the
// second artist's discography must not rewrite the release-group's primary credit to
// them, and must not drop the first artist's claim. That overwrite is what made the
// album flip between the two pages depending on which sync ran last.
func TestSyncArtistDoesNotStealPrimaryCredit(t *testing.T) {
	db := testDB(t)

	// A collaboration already known, credited art-1 then art-2.
	upsertReleaseGroup(db, rgWrite{
		mbID: "rg-collab", artistMBID: "art-1", credits: []string{"art-1", "art-2"},
		title: "The Collaboration", primary: "Single",
		disk: &diskState{owned: true, ownedTracks: 1, totalTracks: 1},
	})

	// Now a writer that only knows about art-2 — what a discography sync is.
	upsertReleaseGroup(db, rgWrite{
		mbID: "rg-collab", artistMBID: "art-2",
		title: "The Collaboration", primary: "Single",
		catalog: &catalogState{},
	})

	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", "rg-collab").First(&rg).Error; err != nil {
		t.Fatalf("release-group: %v", err)
	}
	if rg.ArtistMBID != "art-1" {
		t.Errorf("primary credit = %q, want art-1 — a partial writer must not claim it", rg.ArtistMBID)
	}
	// The catalog state it did know about was still applied.
	if !rg.InCatalog {
		t.Error("catalog state from the partial writer was lost")
	}
	// Disk state survives untouched.
	if !rg.Owned || rg.OwnedTracks != 1 {
		t.Errorf("disk state clobbered: %+v", rg)
	}

	// Both artists still see it.
	for _, mbid := range []string{"art-1", "art-2"} {
		groups, err := ReleaseGroupsForArtist(db, mbid)
		if err != nil {
			t.Fatalf("ReleaseGroupsForArtist(%s): %v", mbid, err)
		}
		if len(groups) != 1 {
			t.Errorf("artist %s sees %d release-groups, want 1", mbid, len(groups))
		}
	}
}

// TestBackfillReleaseGroupArtists covers the upgrade path: rows written before the
// link table get a link from the primary-credit column they already carry, exactly
// once.
func TestBackfillReleaseGroupArtists(t *testing.T) {
	db := testDB(t)

	// Rows as they existed before the link table.
	for _, rg := range []models.CollectionReleaseGroup{
		{MBID: "rg-1", ArtistMBID: "art-1", Title: "One"},
		{MBID: "rg-2", ArtistMBID: "art-2", Title: "Two"},
		{MBID: "rg-orphan", Title: "No artist"}, // nothing to link from
	} {
		if err := db.Create(&rg).Error; err != nil {
			t.Fatalf("seed release-group: %v", err)
		}
	}

	if err := BackfillReleaseGroupArtists(db); err != nil {
		t.Fatalf("BackfillReleaseGroupArtists: %v", err)
	}

	var links int64
	if err := db.Model(&models.CollectionReleaseGroupArtist{}).Count(&links).Error; err != nil {
		t.Fatalf("count links: %v", err)
	}
	if links != 2 {
		t.Fatalf("created %d links, want 2 (the artist-less row has nothing to link)", links)
	}

	// Idempotent: running again creates nothing.
	if err := BackfillReleaseGroupArtists(db); err != nil {
		t.Fatalf("second BackfillReleaseGroupArtists: %v", err)
	}
	if err := db.Model(&models.CollectionReleaseGroupArtist{}).Count(&links).Error; err != nil {
		t.Fatalf("recount links: %v", err)
	}
	if links != 2 {
		t.Errorf("after a second run there are %d links, want 2", links)
	}
}

// TestReleaseGroupsForArtistUnionsUnlinkedRows: a row with a primary credit but no
// link — an un-backfilled upgrade, or any writer that only set the column — must
// still appear, so a missing link can never empty an artist page.
func TestReleaseGroupsForArtistUnionsUnlinkedRows(t *testing.T) {
	db := testDB(t)

	if err := db.Create(&models.CollectionReleaseGroup{MBID: "rg-unlinked", ArtistMBID: "art-1", Title: "Unlinked"}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	// A linked one where the column names someone else, to prove the union is a
	// union and not just the column.
	if err := db.Create(&models.CollectionReleaseGroup{MBID: "rg-collab", ArtistMBID: "art-9", Title: "Collab"}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	linkReleaseGroupArtists(db, "rg-collab", []string{"art-9", "art-1"}, true, creditFromDisk)

	groups, err := ReleaseGroupsForArtist(db, "art-1")
	if err != nil {
		t.Fatalf("ReleaseGroupsForArtist: %v", err)
	}
	seen := map[string]bool{}
	for _, g := range groups {
		seen[g.MBID] = true
	}
	if !seen["rg-unlinked"] || !seen["rg-collab"] {
		t.Errorf("artist saw %v, want both the unlinked row and the linked collaboration", seen)
	}
}

// Rebuild clears the disk view before re-establishing it, so it has to be atomic:
// a failure partway through must not leave the collection reporting that it owns
// less than it does.
func TestRebuildRollsBackOnFailure(t *testing.T) {
	db := testDB(t)

	modules.SetDB(db)
	defer modules.SetDB(nil)

	release := models.MusicBrainzReleaseResponse{
		ID:           "rel-1",
		Title:        "Album One",
		ArtistCredit: []models.ArtistCredit{{Name: "The Band", Artist: models.Artist{ID: "art-1", Name: "The Band"}}},
		ReleaseGroup: models.ReleaseGroup{ID: "rg-1", Title: "Album One", PrimaryType: "Album"},
		Media:        []models.MusicBrainzMedia{{Tracks: []models.Track{{ID: "t1"}, {ID: "t2"}}}},
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

	if _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	var before models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", "rg-1").First(&before).Error; err != nil {
		t.Fatalf("expected an owned release-group after the first rebuild: %v", err)
	}
	if !before.Owned {
		t.Fatal("release-group should be owned before the failing rebuild")
	}

	// Fail the re-establish half while leaving the clear to succeed, by removing the
	// table the second half writes to.
	if err := db.Migrator().DropTable(&models.CollectionRelease{}); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if _, err := Rebuild(db); err == nil {
		t.Fatal("expected the rebuild to fail once its writes cannot land")
	}

	// The clear must have rolled back with it.
	if err := db.Migrator().AutoMigrate(&models.CollectionRelease{}); err != nil {
		t.Fatalf("restore table: %v", err)
	}
	var after models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", "rg-1").First(&after).Error; err != nil {
		t.Fatalf("release-group row vanished: %v", err)
	}
	if !after.Owned || after.OwnedTracks != before.OwnedTracks {
		t.Fatalf("ownership was lost by a failed rebuild: before %+v, after %+v", before, after)
	}
}

func TestRebuildWithoutDB(t *testing.T) {
	stats, err := Rebuild(nil)
	if err != nil || stats.Artists != 0 || stats.Owned != 0 {
		t.Fatalf("Rebuild(nil) = %+v, %v", stats, err)
	}
}

// A burst of attaches — one per track of a folder — must not queue one full
// re-derivation per file. The pass already in flight covers work that arrived
// before it started, so a burst collapses to at most two.
func TestRebuilderCoalescesBursts(t *testing.T) {
	db := testDB(t)
	rb := NewRebuilder(db)

	for i := 0; i < 12; i++ {
		rb.Request()
	}

	rb.Wait()

	// Drain whatever the coalescing tail produced, bounded so a regression that
	// starts one pass per request fails here rather than hanging.
	passes := 1
	for {
		select {
		case <-rb.done:
			passes++
			if passes > 3 {
				t.Fatalf("12 requests produced %d passes; expected them to coalesce", passes)
			}
		case <-time.After(500 * time.Millisecond):
			return
		}
	}
}

// A rebuilder with no database is inert rather than a panic: the one-shot --file
// invocation and most tests run without one.
func TestRebuilderWithoutDB(t *testing.T) {
	NewRebuilder(nil).Request()
	var nilRebuilder *Rebuilder
	nilRebuilder.Request()
}

// TestRebuildCreditsTheReleaseGroup is the production case that motivated reading the
// release-group's own credit: a soundtrack whose group was migrated from "Various
// Artists" to its composers, while the pressing on disk kept the old credit. Crediting
// ownership from the release filed the album under an artist the group no longer named,
// and re-scanning never fixed it because the release was being read correctly.
func TestRebuildCreditsTheReleaseGroup(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)

	seedCachedRelease(t, db, models.MusicBrainzReleaseResponse{
		ID:    "rel-va-pressing",
		Title: "Over the Hedge",
		// The edition on disk is still credited to the placeholder.
		ArtistCredit: []models.ArtistCredit{
			{Name: "Various Artists", Artist: models.Artist{ID: models.VariousArtistsMBID, Name: "Various Artists"}},
		},
		ReleaseGroup: models.ReleaseGroup{
			ID: "rg-soundtrack", Title: "Over the Hedge", PrimaryType: "Album",
			SecondaryTypes: []string{"Soundtrack"},
			// The group has moved on to the composers.
			ArtistCredit: []models.ArtistCredit{
				{Name: "Ben Folds", Artist: models.Artist{ID: "art-folds", Name: "Ben Folds"}},
				{Name: "Rupert Gregson-Williams", Artist: models.Artist{ID: "art-rgw", Name: "Rupert Gregson-Williams"}},
			},
		},
		Media: []models.MusicBrainzMedia{{Tracks: []models.Track{{ID: "t1"}, {ID: "t2"}}}},
	})

	lib := models.Library{Name: "L", Path: "/m"}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	if err := db.Create(&models.LibraryItem{LibraryID: lib.ID, Path: "/m/01.flac", Status: "ok", MBReleaseID: "rel-va-pressing"}).Error; err != nil {
		t.Fatalf("item: %v", err)
	}

	if _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	// The composers are the collection artists; the placeholder is not one of them.
	for _, mbid := range []string{"art-folds", "art-rgw"} {
		if err := db.Where("mb_id = ?", mbid).First(&models.CollectionArtist{}).Error; err != nil {
			t.Errorf("artist %s not created from the release-group credit: %v", mbid, err)
		}
	}
	if err := db.Where("mb_id = ?", models.VariousArtistsMBID).First(&models.CollectionArtist{}).Error; err == nil {
		t.Error("Various Artists must not be created from a pressing whose release-group has moved off it")
	}

	// The album is on the composer's page, headed by the group's first credit.
	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", "rg-soundtrack").First(&rg).Error; err != nil {
		t.Fatalf("release-group not created: %v", err)
	}
	if rg.ArtistMBID != "art-folds" {
		t.Errorf("release-group primary credit = %q, want art-folds", rg.ArtistMBID)
	}
	groups, err := ReleaseGroupsForArtist(db, "art-folds")
	if err != nil {
		t.Fatalf("ReleaseGroupsForArtist: %v", err)
	}
	if len(groups) != 1 || !groups[0].Owned {
		t.Errorf("the composer should own the soundtrack, got %+v", groups)
	}
	if vaGroups, err := ReleaseGroupsForArtist(db, models.VariousArtistsMBID); err != nil {
		t.Fatalf("ReleaseGroupsForArtist(VA): %v", err)
	} else if len(vaGroups) != 0 {
		t.Errorf("the placeholder should own nothing, got %+v", vaGroups)
	}

	// The owned edition follows the same credit, so a per-artist re-tag or
	// re-correlate scoped to the placeholder cannot reach the composer's files.
	var edition models.CollectionRelease
	if err := db.Where("mb_id = ?", "rel-va-pressing").First(&edition).Error; err != nil {
		t.Fatalf("owned edition not created: %v", err)
	}
	if edition.ArtistMBID != "art-folds" {
		t.Errorf("owned edition artist = %q, want art-folds", edition.ArtistMBID)
	}
	if ids, err := ArtistReleaseMBIDs(db, models.VariousArtistsMBID); err != nil {
		t.Fatalf("ArtistReleaseMBIDs(VA): %v", err)
	} else if len(ids) != 0 {
		t.Errorf("the placeholder still claims %v", ids)
	}
}

// TestRebuildFallsBackToTheReleaseCredit: a release-group with no credit of its own —
// an older cache entry, or a payload that omits it — must behave exactly as before
// rather than dropping the album out of the collection.
func TestRebuildFallsBackToTheReleaseCredit(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)

	seedCachedRelease(t, db, models.MusicBrainzReleaseResponse{
		ID:           "rel-legacy",
		Title:        "Legacy Cache Entry",
		ArtistCredit: []models.ArtistCredit{{Name: "The Band", Artist: models.Artist{ID: "art-1", Name: "The Band"}}},
		ReleaseGroup: models.ReleaseGroup{ID: "rg-legacy", Title: "Legacy Cache Entry", PrimaryType: "Album"},
		Media:        []models.MusicBrainzMedia{{Tracks: []models.Track{{ID: "t1"}}}},
	})

	lib := models.Library{Name: "L", Path: "/m"}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	if err := db.Create(&models.LibraryItem{LibraryID: lib.ID, Path: "/m/legacy.flac", Status: "ok", MBReleaseID: "rel-legacy"}).Error; err != nil {
		t.Fatalf("item: %v", err)
	}

	stats, err := Rebuild(db)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if stats.Artists != 1 || stats.Owned != 1 {
		t.Fatalf("Rebuild counts = %d artists, %d owned; want 1, 1", stats.Artists, stats.Owned)
	}
	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", "rg-legacy").First(&rg).Error; err != nil {
		t.Fatalf("release-group not created: %v", err)
	}
	if rg.ArtistMBID != "art-1" || !rg.Owned {
		t.Errorf("release-group = %+v, want owned and credited to art-1", rg)
	}
}

// seedThreeTrackRelease caches one release with three tracks and returns a library
// to hang files off. Shared by the disk-view tests below, which differ only in what
// state their files are in.
func seedThreeTrackRelease(t *testing.T, db *gorm.DB) models.Library {
	t.Helper()
	release := models.MusicBrainzReleaseResponse{
		ID:           "rel-outage",
		Title:        "Album One",
		ArtistCredit: []models.ArtistCredit{{Name: "The Band", Artist: models.Artist{ID: "art-outage", Name: "The Band"}}},
		ReleaseGroup: models.ReleaseGroup{ID: "rg-outage", Title: "Album One", PrimaryType: "Album"},
		Media:        []models.MusicBrainzMedia{{Tracks: []models.Track{{ID: "t1"}, {ID: "t2"}, {ID: "t3"}}}},
	}
	payload, _ := json.Marshal(release)
	if err := db.Create(&models.MusicbrainzReleaseCache{
		MBID: "rel-outage", Payload: string(payload), FetchedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	if err := modules.MusicbrainzLoadCache(); err != nil {
		t.Fatalf("load cache: %v", err)
	}

	lib := models.Library{Name: "L", Path: "/m"}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("library: %v", err)
	}
	return lib
}

// TestRebuildCountsFilesThatFailedToTag is the end of the reported bug: a scan
// interrupted by a MusicBrainz outage must not empty the album. The files are on
// disk and the index knows exactly what they are — only the last attempt to write
// tags failed, which is a fact about the attempt, not about the disk.
func TestRebuildCountsFilesThatFailedToTag(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)
	lib := seedThreeTrackRelease(t, db)

	// One file tagged cleanly before the outage started, two caught by it.
	files := []struct {
		path   string
		status string
	}{
		{"/m/a.flac", models.LibraryItemStatusOK},
		{"/m/b.flac", models.LibraryItemStatusError},
		{"/m/c.flac", models.LibraryItemStatusError},
	}
	for _, f := range files {
		item := models.LibraryItem{LibraryID: lib.ID, Path: f.path, Status: f.status, MBReleaseID: "rel-outage"}
		if f.status == models.LibraryItemStatusError {
			now := time.Now()
			item.Error = "failed to get MB release data: MusicBrainz unavailable"
			item.LastErrorAt = &now
			item.LastErrorTransient = true
		}
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("item %s: %v", f.path, err)
		}
	}

	if _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	var rg models.CollectionReleaseGroup
	if err := db.Where("mb_id = ?", "rg-outage").First(&rg).Error; err != nil {
		t.Fatalf("release-group not created: %v", err)
	}
	if !rg.Owned {
		t.Error("the album left the disk view because tagging failed; the files never left the disk")
	}
	if rg.OwnedTracks != 3 || rg.TotalTracks != 3 {
		t.Errorf("track completeness = %d/%d, want 3/3 — a failed tag write is not a missing file",
			rg.OwnedTracks, rg.TotalTracks)
	}
}

// Unmatched is the one state that still leaves the disk view, and for the opposite
// reason: the manager has withdrawn its answer about what this file is. The stale MB
// ID left on the row is not identity, it is a leftover, and counting it would put the
// album back on the strength of an answer nobody stands behind.
func TestRebuildIgnoresUnmatchedFiles(t *testing.T) {
	db := testDB(t)
	modules.SetDB(db)
	defer modules.SetDB(nil)
	lib := seedThreeTrackRelease(t, db)

	if err := db.Create(&models.LibraryItem{
		LibraryID: lib.ID, Path: "/m/a.flac",
		Status: models.LibraryItemStatusUnmatched, MBReleaseID: "rel-outage",
	}).Error; err != nil {
		t.Fatalf("item: %v", err)
	}

	if _, err := Rebuild(db); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	var rg models.CollectionReleaseGroup
	err := db.Where("mb_id = ?", "rg-outage").First(&rg).Error
	if err == nil && rg.Owned {
		t.Error("an unmatched file was counted as owned on the strength of a withdrawn correlation")
	}
}

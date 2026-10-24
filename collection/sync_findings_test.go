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

// syncFixture wires a mock Lidarr to a collection holding two Lidarr-managed artists.
// `listed` is what the mock's artist endpoint returns; `fail` makes it error instead.
func syncFixture(t *testing.T, listed []models.LidarrArtist, fail bool) *gorm.DB {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/artist"):
			if fail {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(listed)
		case strings.HasPrefix(r.URL.Path, "/api/v1/album"):
			_ = json.NewEncoder(w).Encode([]models.LidarrAlbum{
				{ForeignAlbumID: "rg-1", Title: "One", AlbumType: "Album"},
			})
		}
	}))
	t.Cleanup(srv.Close)

	db := testDB(t)
	if err := db.Create(&models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true, LidarrBaseURL: srv.URL, LidarrAPIKey: "k"}).Error; err != nil {
		t.Fatalf("manager: %v", err)
	}
	for _, a := range []models.CollectionArtist{
		{MBID: "art-known", Name: "Known", ManagedBy: models.ManagedByLidarr},
		{MBID: "art-stranger", Name: "Stranger", ManagedBy: models.ManagedByLidarr},
	} {
		if err := db.Create(&a).Error; err != nil {
			t.Fatalf("artist: %v", err)
		}
	}
	return db
}

// TestSyncReportsArtistsLidarrDoesNotHave is the pass's one real finding, and the one
// its counters structurally could not carry: the artists it means are precisely the
// ones missing from "artists synced". Their wanted view has nothing behind it until
// they are matched or detached, and nothing anywhere said so.
func TestSyncReportsArtistsLidarrDoesNotHave(t *testing.T) {
	db := syncFixture(t, []models.LidarrArtist{{ID: 1, ForeignArtistID: "art-known", Name: "Known"}}, false)

	stats, err := SyncLidarr(db)
	if err != nil {
		t.Fatalf("SyncLidarr: %v", err)
	}
	if stats.ArtistsSynced != 1 {
		t.Fatalf("artists synced = %d, want 1", stats.ArtistsSynced)
	}
	if len(stats.Unknown) != 1 || stats.Unknown[0] != "art-stranger" {
		t.Fatalf("unknown = %v, want [art-stranger]", stats.Unknown)
	}

	// Identifiers, not names: the rows are entity rows, resolved to a name and a link
	// where every other entity row in the feed is.
	items := SyncEventItems(stats)
	if len(items) != 1 {
		t.Fatalf("detail rows = %d, want 1", len(items))
	}
	if items[0].Kind != models.EventItemKindEntity || items[0].Status != models.EventItemStatusUnknown {
		t.Errorf("row = %+v, want an unknown entity row", items[0])
	}

	// The counter has to select those rows, or it is a number over an unrelated list.
	var found bool
	for _, s := range SyncEventStats(stats) {
		if s.Label == "Not in Lidarr" {
			found = s.Value == 1 && s.Filter == models.EventItemStatusUnknown
		}
	}
	if !found {
		t.Errorf("the finding did not reach the counters: %+v", SyncEventStats(stats))
	}
	if !strings.Contains(SyncSummaryLine(stats), "1 not in Lidarr") {
		t.Errorf("summary did not state the finding: %q", SyncSummaryLine(stats))
	}
}

// TestSyncSuppressesUnknownsWhenTheLookupFailed is the same distinction the app draws
// between a MusicBrainz 404 and a MusicBrainz timeout. An artist missing from a list
// that was never fetched is not missing, and reporting the whole collection as unknown
// because Lidarr was restarting is the most alarming possible way to say "try again".
func TestSyncSuppressesUnknownsWhenTheLookupFailed(t *testing.T) {
	db := syncFixture(t, nil, true)

	stats, err := SyncLidarr(db)
	if err != nil {
		t.Fatalf("SyncLidarr: %v", err)
	}
	if len(stats.Unknown) != 0 {
		t.Errorf("unknown = %v; a failed listing must not be read as an answer", stats.Unknown)
	}
	if len(stats.Failures) != 1 {
		t.Fatalf("failures = %v, want the manager listing failure", stats.Failures)
	}
	if !strings.Contains(SyncSummaryLine(stats), "1 lookup(s) failed") {
		t.Errorf("summary hid the failure: %q", SyncSummaryLine(stats))
	}
	if SyncEventDetails(stats)["failures"] == nil {
		t.Error("details dropped the failures; they have no detail rows, so this is the only place they appear")
	}
}

// TestSyncSaysNothingWhenNothingIsWrong: an ordinary pass must read exactly as it
// always did. Two findings appended as zeroes to every nightly row is how the runs
// that found something get missed.
func TestSyncSaysNothingWhenNothingIsWrong(t *testing.T) {
	db := syncFixture(t, []models.LidarrArtist{
		{ID: 1, ForeignArtistID: "art-known", Name: "Known"},
		{ID: 2, ForeignArtistID: "art-stranger", Name: "Stranger"},
	}, false)

	stats, err := SyncLidarr(db)
	if err != nil {
		t.Fatalf("SyncLidarr: %v", err)
	}
	if line := SyncSummaryLine(stats); line != "2 artists synced · 2 albums" {
		t.Errorf("summary = %q; a clean pass should carry no findings", line)
	}
	details := SyncEventDetails(stats)
	if details["unknown_artists"] != nil || details["failures"] != nil {
		t.Errorf("details carried empty findings: %+v", details)
	}
}

// TestSyncNamesTheArtistWhoseAlbumsFailed: one artist's albums failing to load is a
// failure *about* that artist, so it is a row naming them rather than a line in the
// entity-less list — and, since the artist listing itself succeeded, it does not stop
// the pass reporting who Lidarr does not have.
func TestSyncNamesTheArtistWhoseAlbumsFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/artist"):
			_ = json.NewEncoder(w).Encode([]models.LidarrArtist{{ID: 1, ForeignArtistID: "art-known", Name: "Known"}})
		case strings.HasPrefix(r.URL.Path, "/api/v1/album"):
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)

	db := testDB(t)
	if err := db.Create(&models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true, LidarrBaseURL: srv.URL, LidarrAPIKey: "k"}).Error; err != nil {
		t.Fatalf("manager: %v", err)
	}
	for _, a := range []models.CollectionArtist{
		{MBID: "art-known", Name: "Known", ManagedBy: models.ManagedByLidarr},
		{MBID: "art-stranger", Name: "Stranger", ManagedBy: models.ManagedByLidarr},
	} {
		if err := db.Create(&a).Error; err != nil {
			t.Fatalf("artist: %v", err)
		}
	}

	stats, err := SyncLidarr(db)
	if err != nil {
		t.Fatalf("SyncLidarr: %v", err)
	}
	if len(stats.Failures) != 0 || len(stats.FailedArtists) != 1 || stats.FailedArtists[0].ArtistMBID != "art-known" {
		t.Fatalf("failures = %v / %v, want one row naming art-known", stats.Failures, stats.FailedArtists)
	}
	if len(stats.Unknown) != 1 || stats.Unknown[0] != "art-stranger" {
		t.Errorf("unknown = %v; the artist listing succeeded, so who is missing is still known", stats.Unknown)
	}

	var row *models.EventItem
	for _, it := range SyncEventItems(stats) {
		if it.Status == models.EventItemStatusError {
			it := it
			row = &it
		}
	}
	if row == nil || row.Path != "art-known" || row.Kind != models.EventItemKindEntity || !strings.Contains(row.Error, "could not list their albums") {
		t.Errorf("failure row = %+v, want an entity row naming the artist and the cause", row)
	}
	for _, s := range SyncEventStats(stats) {
		if s.Label == "Lookups failed" && (s.Value != 1 || s.Filter != models.EventItemStatusError) {
			t.Errorf("failure counter = %+v, want 1 and selectable", s)
		}
	}
	if !strings.Contains(SyncSummaryLine(stats), "1 lookup(s) failed") {
		t.Errorf("summary hid the failure: %q", SyncSummaryLine(stats))
	}
}

// TestSyncRecordsAnArtistTheManagerReKeyed: an artist Lidarr stopped listing, beside a
// same-named artist it lists that the collection does not hold, is Lidarr having moved
// them to a new ID. That is recorded as an artist redirect and explained, rather than
// reported as unknown; a name shared by two candidates is not paired.
func TestSyncRecordsAnArtistTheManagerReKeyed(t *testing.T) {
	db := syncFixture(t, []models.LidarrArtist{
		{ID: 1, ForeignArtistID: "art-known", Name: "Known"},
		{ID: 2, ForeignArtistID: "art-stranger-new", Name: "Stranger"},
	}, false)

	stats, err := SyncLidarr(db)
	if err != nil {
		t.Fatalf("SyncLidarr: %v", err)
	}
	if len(stats.RekeyedArtists) != 1 || stats.RekeyedArtists[0] != "art-stranger" {
		t.Fatalf("re-keyed artists = %v, want art-stranger", stats.RekeyedArtists)
	}
	if len(stats.Unknown) != 0 {
		t.Errorf("unknown = %v; a re-keyed artist is accounted for", stats.Unknown)
	}
	var m models.MusicbrainzMigration
	if err := db.Where("entity_type = ? AND old_mb_id = ?", models.MigrationEntityArtist, "art-stranger").First(&m).Error; err != nil {
		t.Fatalf("no redirect recorded: %v", err)
	}
	if m.NewMBID != "art-stranger-new" || m.Kind != models.MigrationKindRedirect || m.Source != models.ManagerTypeLidarr {
		t.Errorf("migration = %+v", m)
	}
	if !strings.Contains(SyncSummaryLine(stats), "1 artist(s) re-keyed") {
		t.Errorf("summary = %q", SyncSummaryLine(stats))
	}

	// Two same-named candidates: either could be the one, so neither is.
	db2 := syncFixture(t, []models.LidarrArtist{
		{ID: 1, ForeignArtistID: "art-known", Name: "Known"},
		{ID: 2, ForeignArtistID: "art-s1", Name: "Stranger"},
		{ID: 3, ForeignArtistID: "art-s2", Name: "Stranger"},
	}, false)
	stats, err = SyncLidarr(db2)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.RekeyedArtists) != 0 || len(stats.Unknown) != 1 {
		t.Errorf("ambiguous name: re-keyed %v, unknown %v; want no pairing and the artist still unknown", stats.RekeyedArtists, stats.Unknown)
	}
}

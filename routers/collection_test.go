package routers

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// The collection endpoints derive nearly everything they return — complete,
// discrepancy, wanted, wanted_source, and five counts per artist — precisely so the
// rules have one definition instead of being reimplemented in TypeScript. That makes
// the derivation the thing worth testing: the UI is only ever as correct as these
// numbers.
//
// Nothing here touches MusicBrainz. The handlers that do (discography, info,
// editions, and following, which syncs) are exercised only on the paths that return
// before any external call — a test that depends on musicbrainz.org being reachable
// is a test that fails for reasons unrelated to this code.

// artistFixture creates an artist with the given follow state.
func artistFixture(t *testing.T, db *gorm.DB, mbid, name, managedBy string, monitored bool) models.CollectionArtist {
	t.Helper()
	// Synced, because a fixture artist stands for one a manager has answered about:
	// the catalog columns on their release-groups only ever come from a sync, and
	// collection.CatalogChecked reads this to decide whether a disk/catalog
	// disagreement may be reported at all.
	synced := time.Now()
	artist := models.CollectionArtist{
		MBID: mbid, Name: name, ManagedBy: managedBy, Monitored: monitored,
		Origin: models.CollectionOriginLibrary, FollowTypes: models.DefaultFollowTypes,
		LastSyncedAt: &synced,
	}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist %s: %v", name, err)
	}
	return artist
}

type rgFixture struct {
	mbid, title, primary, secondary, date string
	owned                                 bool
	ownedTracks, totalTracks              int
	inCatalog                             bool
	catalogOwned, catalogTotal            int
	catalogMonitored                      bool
	catalogRelease                        string
}

func releaseGroupFixture(t *testing.T, db *gorm.DB, artistMBID string, f rgFixture) models.CollectionReleaseGroup {
	t.Helper()
	rg := models.CollectionReleaseGroup{
		MBID: f.mbid, ArtistMBID: artistMBID, Title: f.title,
		PrimaryType: f.primary, SecondaryTypes: f.secondary, FirstReleaseDate: f.date,
		Owned: f.owned, OwnedTracks: f.ownedTracks, TotalTracks: f.totalTracks,
		InCatalog: f.inCatalog, CatalogOwnedTracks: f.catalogOwned,
		CatalogTotalTracks: f.catalogTotal, CatalogMonitored: f.catalogMonitored,
		CatalogReleaseMBID: f.catalogRelease,
	}
	if err := db.Create(&rg).Error; err != nil {
		t.Fatalf("create release group %s: %v", f.title, err)
	}
	return rg
}

func decodeJSON[T any](t *testing.T, r *gin.Engine, method, path, token string, body any) T {
	t.Helper()
	w := do(r, method, path, token, body)
	if w.Code != http.StatusOK {
		t.Fatalf("%s %s = %d, want 200: %s", method, path, w.Code, w.Body.String())
	}
	var out T
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v (body %s)", path, err, w.Body.String())
	}
	return out
}

// TestListArtistsCounts: one artist holding one of each shape, so every count in the
// summary is distinguishable from the others. Counts computed from the same row set
// would pass a weaker fixture even when two of them were swapped.
func TestListArtistsCounts(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	artist := artistFixture(t, api.DB, "artist-1", "Kate Bush", models.ManagedByLidarr, true)
	// Complete on disk, and the catalog agrees.
	releaseGroupFixture(t, api.DB, artist.MBID, rgFixture{
		mbid: "rg-complete", title: "Hounds of Love", primary: "Album", date: "1985-09-16",
		owned: true, ownedTracks: 12, totalTracks: 12,
		inCatalog: true, catalogOwned: 12, catalogTotal: 12,
	})
	// Partial: owned, but not every track.
	releaseGroupFixture(t, api.DB, artist.MBID, rgFixture{
		mbid: "rg-partial", title: "The Dreaming", primary: "Album", date: "1982-09-13",
		owned: true, ownedTracks: 5, totalTracks: 10,
		inCatalog: true, catalogOwned: 5, catalogTotal: 10,
	})
	// Missing: the catalog knows it, nothing on disk.
	releaseGroupFixture(t, api.DB, artist.MBID, rgFixture{
		mbid: "rg-missing", title: "Aerial", primary: "Album", date: "2005-11-07",
		inCatalog: true, catalogTotal: 16, catalogMonitored: true,
	})
	// Mismatch: on disk, but the manager has no matching album at all.
	releaseGroupFixture(t, api.DB, artist.MBID, rgFixture{
		mbid: "rg-unmapped", title: "Live at Hammersmith", primary: "Album", secondary: "Live",
		date: "1994-01-01", owned: true, ownedTracks: 9, totalTracks: 9,
	})

	list := decodeJSON[[]map[string]any](t, r, "GET", "/api/v1/artists", token, nil)
	if len(list) != 1 {
		t.Fatalf("artists = %d, want 1", len(list))
	}
	got := list[0]

	for _, tc := range []struct {
		field string
		want  float64
	}{
		{"owned_count", 3},    // complete + partial + unmapped
		{"complete_count", 2}, // complete + unmapped (9/9)
		{"partial_count", 1},
		{"missing_count", 1},
		{"mismatch_count", 1},
		{"picked_count", 0},
	} {
		if got[tc.field] != tc.want {
			t.Errorf("%s = %v, want %v", tc.field, got[tc.field], tc.want)
		}
	}
	// A Lidarr-managed artist's stored follow flag must not read as governing.
	if got["follow_governs"] != false {
		t.Errorf("follow_governs = %v, want false for a Lidarr-managed artist", got["follow_governs"])
	}
}

// TestListArtistsCountsPickedAlbumsNotDesireRows: wanting an album and one of its
// editions is one pick, not two.
func TestListArtistsCountsPickedAlbumsNotDesireRows(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	artist := artistFixture(t, api.DB, "artist-1", "Slint", models.ManagedByAutotaggerr, false)
	releaseGroupFixture(t, api.DB, artist.MBID, rgFixture{mbid: "rg-1", title: "Spiderland", primary: "Album"})

	for _, release := range []string{"", "release-a", "release-b"} {
		if err := api.DB.Create(&models.CollectionDesire{
			ArtistMBID: artist.MBID, ReleaseGroupMBID: "rg-1", ReleaseMBID: release,
		}).Error; err != nil {
			t.Fatalf("create desire: %v", err)
		}
	}

	list := decodeJSON[[]map[string]any](t, r, "GET", "/api/v1/artists", token, nil)
	if got := list[0]["picked_count"]; got != float64(1) {
		t.Errorf("picked_count = %v, want 1 — three desire rows for one album is one pick", got)
	}
}

// TestGetArtistReportsTheManagersChosenEdition is the handler half of the manager
// trickle-down: Lidarr's monitored release has to survive the trip to the client as a
// wanted edition, or the release-group page still shows a green album with nothing
// marked. A field is not wired until the handler is tested.
func TestGetArtistReportsTheManagersChosenEdition(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	artist := artistFixture(t, api.DB, "artist-1", "6LACK", models.ManagedByLidarr, false)
	releaseGroupFixture(t, api.DB, artist.MBID, rgFixture{
		mbid: "rg-1", title: "Album", primary: "Album", owned: true,
		inCatalog: true, catalogMonitored: true, catalogRelease: "rel-1",
	})
	if err := api.DB.Create(&models.CollectionDesire{
		ArtistMBID: artist.MBID, ReleaseGroupMBID: "rg-1", ReleaseMBID: "rel-1",
		Source: models.DesireSourceManager,
	}).Error; err != nil {
		t.Fatalf("create desire: %v", err)
	}

	detail := decodeJSON[struct {
		ReleaseGroups []map[string]any `json:"release_groups"`
	}](t, r, "GET", "/api/v1/artists/"+artist.MBID, token, nil)

	if len(detail.ReleaseGroups) != 1 {
		t.Fatalf("release groups = %+v", detail.ReleaseGroups)
	}
	rg := detail.ReleaseGroups[0]
	if rg["wanted"] != true || rg["wanted_source"] != "manager" {
		t.Errorf("wanted=%v source=%v, want a manager want", rg["wanted"], rg["wanted_source"])
	}
	editions, _ := rg["desired_releases"].([]any)
	if len(editions) != 1 || editions[0] != "rel-1" {
		t.Errorf("desired_releases = %v, want the monitored edition", rg["desired_releases"])
	}
	if rg["identity_editable"] != false {
		t.Errorf("identity_editable = %v, want false under Lidarr", rg["identity_editable"])
	}

	// And it is not a pick: nobody chose this here, so the artist list must not
	// report one.
	list := decodeJSON[[]map[string]any](t, r, "GET", "/api/v1/artists", token, nil)
	if got := list[0]["picked_count"]; got != float64(0) {
		t.Errorf("picked_count = %v, want 0 — a mirrored want is not a pick", got)
	}
}

// TestGetArtistDerivesWantedSource: the same album is wanted for three different
// reasons, and only the explicit one is the row's to edit.
func TestGetArtistDerivesWantedSource(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	// Natively managed and followed, so the follow governs.
	artist := artistFixture(t, api.DB, "artist-1", "Talk Talk", models.ManagedByAutotaggerr, true)
	// Album: auto-wanted by the follow (Album is in the default follow types).
	releaseGroupFixture(t, api.DB, artist.MBID, rgFixture{
		mbid: "rg-auto", title: "Spirit of Eden", primary: "Album", date: "1988-09-12", inCatalog: true,
	})
	// Explicitly picked.
	releaseGroupFixture(t, api.DB, artist.MBID, rgFixture{
		mbid: "rg-explicit", title: "Laughing Stock", primary: "Album", date: "1991-09-16", inCatalog: true,
	})
	if err := api.DB.Create(&models.CollectionDesire{
		ArtistMBID: artist.MBID, ReleaseGroupMBID: "rg-explicit",
		RecordingMBIDs: []string{"rec-1", "rec-2"},
	}).Error; err != nil {
		t.Fatalf("create desire: %v", err)
	}
	// A single: not in the default follow types, so not wanted at all.
	releaseGroupFixture(t, api.DB, artist.MBID, rgFixture{
		mbid: "rg-single", title: "It's My Life", primary: "Single", date: "1984-01-01", inCatalog: true,
	})

	detail := decodeJSON[struct {
		Artist        map[string]any   `json:"artist"`
		ReleaseGroups []map[string]any `json:"release_groups"`
	}](t, r, "GET", "/api/v1/artists/"+artist.MBID, token, nil)

	if detail.Artist["follow_governs"] != true {
		t.Errorf("follow_governs = %v, want true for a natively managed artist", detail.Artist["follow_governs"])
	}

	bySource := map[string]string{}
	for _, rg := range detail.ReleaseGroups {
		mbid, _ := rg["mb_id"].(string)
		source, _ := rg["wanted_source"].(string)
		bySource[mbid] = source
	}
	want := map[string]string{"rg-auto": "auto", "rg-explicit": "explicit", "rg-single": ""}
	for mbid, expected := range want {
		if bySource[mbid] != expected {
			t.Errorf("%s wanted_source = %q, want %q", mbid, bySource[mbid], expected)
		}
	}

	// The recordings a desire carries must reach the row: they were once dropped
	// silently between the model and the handler.
	for _, rg := range detail.ReleaseGroups {
		if rg["mb_id"] == "rg-explicit" {
			recordings, _ := rg["desired_recordings"].([]any)
			if len(recordings) != 2 {
				t.Errorf("desired_recordings = %v, want 2 entries", rg["desired_recordings"])
			}
		}
	}
}

func TestGetArtistUnknown(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)
	if w := do(r, "GET", "/api/v1/artists/nope", token, nil); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestUpdateFollowStoresSettings: the unfollow path, which is the one that does not
// trigger a discography sync — so it is the one testable without MusicBrainz.
func TestUpdateFollowStoresSettings(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	artist := artistFixture(t, api.DB, "artist-1", "Bark Psychosis", models.ManagedByAutotaggerr, true)

	w := do(r, "POST", "/api/v1/artists/"+artist.MBID+"/follow", token, map[string]any{
		"monitored":        false,
		"follow_types":     " Album,EP,Single ",
		"follow_secondary": true,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var stored models.CollectionArtist
	if err := api.DB.Where("mb_id = ?", artist.MBID).First(&stored).Error; err != nil {
		t.Fatalf("reload artist: %v", err)
	}
	if stored.Monitored {
		t.Error("monitored is still set after unfollowing")
	}
	if stored.FollowTypes != "Album,EP,Single" {
		t.Errorf("follow_types = %q, want the trimmed value", stored.FollowTypes)
	}
	if !stored.FollowSecondary {
		t.Error("follow_secondary was not stored")
	}
}

func TestUpdateFollowRejectsBadInput(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	artistFixture(t, api.DB, "artist-1", "Codeine", models.ManagedByAutotaggerr, false)

	if w := do(r, "POST", "/api/v1/artists/missing/follow", token, map[string]any{"monitored": false}); w.Code != http.StatusNotFound {
		t.Errorf("unknown artist = %d, want 404", w.Code)
	}
	// An empty body is valid JSON with no updates in it: nothing changes, and it is
	// not an error.
	if w := do(r, "POST", "/api/v1/artists/artist-1/follow", token, map[string]any{}); w.Code != http.StatusOK {
		t.Errorf("empty update = %d, want 200: %s", w.Code, w.Body.String())
	}
}

// TestSetArtistMonitoredUnfollow: /monitor is the older, narrower sibling of
// /follow. Only the false path avoids a sync.
func TestSetArtistMonitoredUnfollow(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	artist := artistFixture(t, api.DB, "artist-1", "Duster", models.ManagedByAutotaggerr, true)

	w := do(r, "POST", "/api/v1/artists/"+artist.MBID+"/monitor", token, map[string]any{"monitored": false})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var stored models.CollectionArtist
	api.DB.Where("mb_id = ?", artist.MBID).First(&stored)
	if stored.Monitored {
		t.Error("monitored was not cleared")
	}

	if w := do(r, "POST", "/api/v1/artists/nope/monitor", token, map[string]any{"monitored": false}); w.Code != http.StatusNotFound {
		t.Errorf("unknown artist = %d, want 404", w.Code)
	}
	if w := do(r, "POST", "/api/v1/artists/artist-1/monitor", token, "not-an-object"); w.Code != http.StatusBadRequest {
		t.Errorf("bad body = %d, want 400", w.Code)
	}
}

// TestAddArtistIsIdempotent: the UI offers "add" without first checking, so asking
// twice must be the same artist rather than an error or a duplicate.
func TestAddArtistIsIdempotent(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)

	first := decodeJSON[map[string]any](t, r, "POST", "/api/v1/artists", token,
		map[string]string{"mb_id": "artist-new", "name": "Grouper"})
	second := decodeJSON[map[string]any](t, r, "POST", "/api/v1/artists", token,
		map[string]string{"mb_id": "artist-new", "name": "Grouper"})
	if first["id"] != second["id"] {
		t.Errorf("adding twice created two artists: %v vs %v", first["id"], second["id"])
	}
	// A manually added artist owns nothing, and that has to be visible — otherwise
	// a rebuild cannot tell "not owned yet" from "stale row".
	if first["origin"] != models.CollectionOriginManual {
		t.Errorf("origin = %v, want %q", first["origin"], models.CollectionOriginManual)
	}

	if w := do(r, "POST", "/api/v1/artists", token, map[string]string{"name": "No ID"}); w.Code != http.StatusBadRequest {
		t.Errorf("missing mb_id = %d, want 400", w.Code)
	}
	if w := do(r, "POST", "/api/v1/artists", token, "garbage"); w.Code != http.StatusBadRequest {
		t.Errorf("bad body = %d, want 400", w.Code)
	}
}

func TestSearchArtistsRequiresQuery(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)
	// Guarded before the MusicBrainz call, so this is the one search path that can
	// be asserted without the network.
	if w := do(r, "GET", "/api/v1/search/artists?q=%20%20", token, nil); w.Code != http.StatusBadRequest {
		t.Errorf("blank query = %d, want 400", w.Code)
	}
}

func TestClearDesire(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	artist := artistFixture(t, api.DB, "artist-1", "Mount Eerie", models.ManagedByAutotaggerr, false)
	if err := api.DB.Create(&models.CollectionDesire{
		ArtistMBID: artist.MBID, ReleaseGroupMBID: "rg-1", ReleaseMBID: "",
	}).Error; err != nil {
		t.Fatalf("create desire: %v", err)
	}

	w := do(r, "DELETE", "/api/v1/artists/"+artist.MBID+"/desires?release_group_mb_id=rg-1&release_mb_id=", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var remaining int64
	api.DB.Model(&models.CollectionDesire{}).Count(&remaining)
	if remaining != 0 {
		t.Errorf("desires remaining = %d, want 0", remaining)
	}

	// Clearing something that was never wanted is not an error — the UI drops a
	// want it may already have dropped.
	if w := do(r, "DELETE", "/api/v1/artists/"+artist.MBID+"/desires?release_group_mb_id=rg-absent", token, nil); w.Code != http.StatusOK {
		t.Errorf("clearing an absent desire = %d, want 200", w.Code)
	}
}

// TestRebuildCollectionIsNetworkFree: rebuild re-derives the disk view from the
// index and cached releases only. An empty index rebuilds to an empty collection
// rather than failing.
func TestScanCollectionIsNetworkFree(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)

	out := decodeJSON[map[string]any](t, r, "POST", "/api/v1/scan", token, nil)
	if out["artists"] != float64(0) || out["owned_release_groups"] != float64(0) {
		t.Errorf("scan of an empty index = %v, want zeroes", out)
	}
}

// TestRebuildKeepsDesires: the property that matters most about rebuild — it owns
// the disk view and must never touch authored intent.
func TestScanKeepsDesires(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	artist := artistFixture(t, api.DB, "artist-1", "Low", models.ManagedByAutotaggerr, false)
	if err := api.DB.Create(&models.CollectionDesire{
		ArtistMBID: artist.MBID, ReleaseGroupMBID: "rg-1", RecordingMBIDs: []string{"rec-1"},
	}).Error; err != nil {
		t.Fatalf("create desire: %v", err)
	}

	if w := do(r, "POST", "/api/v1/scan", token, nil); w.Code != http.StatusOK {
		t.Fatalf("scan = %d: %s", w.Code, w.Body.String())
	}

	var desires []models.CollectionDesire
	api.DB.Find(&desires)
	if len(desires) != 1 || len(desires[0].RecordingMBIDs) != 1 {
		t.Errorf("desires after rebuild = %+v, want the one authored want intact", desires)
	}
}

func TestSyncLidarrNeedsAManager(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	// Refused up front rather than started and failed in the background: there is
	// nothing to sync from, and an event reporting that is just noise.
	if w := do(r, "POST", "/api/v1/collection/sync-lidarr", token, nil); w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400: %s", w.Code, w.Body.String())
	}

	// A disabled manager is not a manager for this purpose.
	if err := api.DB.Create(&models.Manager{
		Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: false,
	}).Error; err != nil {
		t.Fatalf("create manager: %v", err)
	}
	if w := do(r, "POST", "/api/v1/collection/sync-lidarr", token, nil); w.Code != http.StatusBadRequest {
		t.Errorf("disabled manager = %d, want 400", w.Code)
	}
}

func TestArtistInfoUnknownArtist(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)
	// Checked against the database before MusicBrainz is contacted, so an unknown
	// artist is a 404 rather than a wasted rate-limited call.
	if w := do(r, "GET", "/api/v1/artists/nope/info", token, nil); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestDiscographyUnknownArtist(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)
	if w := do(r, "GET", "/api/v1/artists/nope/discography", token, nil); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestCollectionEndpointsRequireAuth(t *testing.T) {
	r, _ := setupAPI(t)
	for _, path := range []string{
		"/api/v1/artists",
		"/api/v1/artists/artist-1",
		"/api/v1/artists/artist-1/info",
		"/api/v1/artists/artist-1/discography",
	} {
		if w := do(r, "GET", path, "", nil); w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without a token = %d, want 401", path, w.Code)
		}
	}
}

// TestAnnotateEditionsKeepsOwnedEditionsMusicBrainzOmits: an edition you own files
// of must never vanish from the page that edits wants for it, even when MusicBrainz
// is unreachable or the release was merged away upstream.
func TestAnnotateEditionsKeepsOwnedEditionsMusicBrainzOmits(t *testing.T) {
	listed := []models.MusicBrainzReleaseSearchResult{
		{ID: "release-listed", Title: "Spiderland", Date: "1991-03-27"},
	}
	owned := []models.CollectionRelease{
		{MBID: "release-listed", Title: "Spiderland", OwnedTracks: 6, TotalTracks: 6},
		{MBID: "release-orphan", Title: "Spiderland (remaster)", OwnedTracks: 3, TotalTracks: 6},
	}

	out := annotateEditions(listed, owned)
	if len(out) != 2 {
		t.Fatalf("editions = %d, want 2 (the listed one plus the owned orphan)", len(out))
	}

	byMBID := map[string]editionView{}
	for _, e := range out {
		byMBID[e.ID] = e
	}
	if e := byMBID["release-listed"]; !e.Owned || !e.Complete || e.OwnedTracks != 6 {
		t.Errorf("listed edition = %+v, want owned and complete", e)
	}
	orphan, ok := byMBID["release-orphan"]
	if !ok {
		t.Fatal("an owned edition MusicBrainz does not list was dropped")
	}
	if !orphan.Owned || orphan.Complete || orphan.OwnedTracks != 3 {
		t.Errorf("orphan edition = %+v, want owned and partial", orphan)
	}
}

func TestAnnotateEditionsMarksUnownedEditions(t *testing.T) {
	out := annotateEditions([]models.MusicBrainzReleaseSearchResult{
		{ID: "release-a", Title: "Original"},
		{ID: "release-b", Title: "Reissue"},
	}, nil)
	if len(out) != 2 {
		t.Fatalf("editions = %d, want 2", len(out))
	}
	for _, e := range out {
		if e.Owned || e.Complete || e.OwnedTracks != 0 {
			t.Errorf("edition %s = %+v, want unowned", e.ID, e)
		}
	}
}

// TestArtistPageShowsCollaborations is the reported bug at the API level: a release
// credited to two artists must appear, owned, on both artists' pages — and count
// towards both on the collection overview. It used to appear on whichever artist the
// last sync named, and to look un-owned on the other.
func TestArtistPageShowsCollaborations(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	first := artistFixture(t, api.DB, "art-1", "First Artist", models.ManagedByAutotaggerr, false)
	second := artistFixture(t, api.DB, "art-2", "Second Artist", models.ManagedByAutotaggerr, false)

	// One owned collaboration, whose primary credit is the first artist.
	releaseGroupFixture(t, api.DB, first.MBID, rgFixture{
		mbid: "rg-collab", title: "The Collaboration", primary: "Single", date: "2021-05-01",
		owned: true, ownedTracks: 1, totalTracks: 1,
	})
	// Both artists are credited on it.
	for position, artistMBID := range []string{"art-1", "art-2"} {
		if err := api.DB.Create(&models.CollectionReleaseGroupArtist{
			ReleaseGroupMBID: "rg-collab", ArtistMBID: artistMBID, Position: position,
		}).Error; err != nil {
			t.Fatalf("link %s: %v", artistMBID, err)
		}
	}

	for _, artist := range []models.CollectionArtist{first, second} {
		detail := decodeJSON[struct {
			ReleaseGroups []map[string]any `json:"release_groups"`
		}](t, r, "GET", "/api/v1/artists/"+artist.MBID, token, nil)

		if len(detail.ReleaseGroups) != 1 {
			t.Fatalf("%s sees %d release-groups, want the collaboration", artist.Name, len(detail.ReleaseGroups))
		}
		rg := detail.ReleaseGroups[0]
		if rg["mb_id"] != "rg-collab" {
			t.Errorf("%s sees %v, want rg-collab", artist.Name, rg["mb_id"])
		}
		if rg["owned"] != true {
			t.Errorf("%s sees the collaboration as not owned: %v", artist.Name, rg["owned"])
		}
		if rg["complete"] != true {
			t.Errorf("%s sees the collaboration as incomplete: %v", artist.Name, rg["complete"])
		}
	}

	// And it counts for both on the overview, rather than only for the primary credit.
	summaries := decodeJSON[[]map[string]any](t, r, "GET", "/api/v1/artists", token, nil)
	owned := map[string]float64{}
	for _, s := range summaries {
		mbid, _ := s["mb_id"].(string)
		n, _ := s["owned_count"].(float64)
		owned[mbid] = n
	}
	if owned["art-1"] != 1 || owned["art-2"] != 1 {
		t.Errorf("owned counts = art-1:%v art-2:%v, want 1 each", owned["art-1"], owned["art-2"])
	}
}

package routers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/modules"
)

// seedDiscography puts an artist's discography straight into the MusicBrainz entity
// cache, fresh and complete, so the handlers that read it through
// modules.GetArtistDiscography — a composite deliberately not behind the metadata
// port — answer without a network call.
func seedDiscography(t *testing.T, artistMBID string, groups []models.MusicBrainzArtistReleaseGroup) {
	t.Helper()
	modules.ResetMusicBrainzCachesForTest(t)
	modules.SeedMusicBrainzEntityForTest(t, models.MBEntityDiscography, artistMBID, map[string]any{
		"groups": groups, "complete": true,
	})
}

// refuseMusicBrainz points the real MusicBrainz client at a local server that
// answers every request with a non-transient 400, so the handlers' upstream-failure
// branches run without a retry, a sleep, or a packet leaving the machine.
func refuseMusicBrainz(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"refused"}`, http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	modules.SetMusicBrainzBaseURLForTest(t, srv.URL)
}

// TestListArtistsFailures: the artist list fails loudly when either table it needs
// cannot be read, and degrades — not fails — when only the credit links are missing.
func TestListArtistsFailures(t *testing.T) {
	t.Run("artists unreadable", func(t *testing.T) {
		r, api := setupAPI(t)
		token := loginToken(t, r)
		dropTable(t, api.DB, &models.CollectionArtist{})
		w := do(r, "GET", "/api/v1/artists", token, nil)
		expectStatus(t, "GET /artists", w.Code, w.Body.String(), http.StatusInternalServerError, "failed to list artists")
	})
	t.Run("release groups unreadable", func(t *testing.T) {
		r, api := setupAPI(t)
		token := loginToken(t, r)
		dropTable(t, api.DB, &models.CollectionReleaseGroup{})
		w := do(r, "GET", "/api/v1/artists", token, nil)
		expectStatus(t, "GET /artists", w.Code, w.Body.String(), http.StatusInternalServerError, "failed to list release groups")
	})
	t.Run("credit links unreadable", func(t *testing.T) {
		r, api := setupAPI(t)
		token := loginToken(t, r)
		artistFixture(t, api.DB, "art-1", "Band", models.ManagedByAutotaggerr, false)
		// An artist with no albums at all still gets a row, with zero counts.
		artistFixture(t, api.DB, "art-2", "Empty", models.ManagedByAutotaggerr, false)
		releaseGroupFixture(t, api.DB, "art-1", rgFixture{mbid: "rg-1", title: "A", primary: "Album", owned: true})
		dropTable(t, api.DB, &models.CollectionReleaseGroupArtist{})

		got := decodeJSON[[]struct {
			MBID       string `json:"mb_id"`
			OwnedCount int    `json:"owned_count"`
		}](t, r, "GET", "/api/v1/artists", token, nil)
		if len(got) != 2 {
			t.Fatalf("artists = %+v, want both", got)
		}
		owned := map[string]int{}
		for _, a := range got {
			owned[a.MBID] = a.OwnedCount
		}
		// Without links the primary credit is the fallback, so the album still counts.
		if owned["art-1"] != 1 || owned["art-2"] != 0 {
			t.Errorf("owned counts = %v, want art-1:1 art-2:0", owned)
		}
	})
}

// TestArtistPagesDegradeWhenSideTablesFail: the artist page, its discography and the
// release-group page each read several side tables. Losing one is logged and the page
// still renders from what is left — a 500 for a missing count would blank the page.
func TestArtistPagesDegradeWhenSideTablesFail(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	artistFixture(t, api.DB, "art-1", "Band", models.ManagedByAutotaggerr, false)
	seedDiscography(t, "art-1", []models.MusicBrainzArtistReleaseGroup{
		{ID: "rg-live", Title: "Live", PrimaryType: "Album", SecondaryTypes: []string{"Live"}, FirstReleaseDate: "2001"},
	})
	api.Meta = &fakeMeta{}

	dropTable(t, api.DB, &models.CollectionReleaseGroupArtist{})
	dropTable(t, api.DB, &models.CollectionDesire{})
	dropTable(t, api.DB, &models.CollectionRelease{})

	w := do(r, "GET", "/api/v1/artists/art-1", token, nil)
	expectStatus(t, "GET artist", w.Code, w.Body.String(), http.StatusOK, `"Band"`)

	w = do(r, "GET", "/api/v1/artists/art-1/discography", token, nil)
	expectStatus(t, "GET discography", w.Code, w.Body.String(), http.StatusOK, "rg-live")

	w = do(r, "GET", "/api/v1/artists/art-1/release-groups/rg-live", token, nil)
	expectStatus(t, "GET release group", w.Code, w.Body.String(), http.StatusOK, `"Live"`)
}

// TestDiscographyMergesLiveAndStored: the live list is annotated with what the
// collection holds, carries MusicBrainz metadata for albums the collection does not
// have, and keeps owned albums the live list omits (the compilation case).
func TestDiscographyMergesLiveAndStored(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	artistFixture(t, api.DB, "art-1", "Band", models.ManagedByAutotaggerr, false)
	releaseGroupFixture(t, api.DB, "art-1", rgFixture{mbid: "rg-owned", title: "Owned", primary: "Album", owned: true})
	releaseGroupFixture(t, api.DB, "art-1", rgFixture{mbid: "rg-comp", title: "Comp", primary: "Album", secondary: "Compilation", owned: true})
	seedDiscography(t, "art-1", []models.MusicBrainzArtistReleaseGroup{
		{ID: "rg-owned", Title: "Owned", PrimaryType: "Album"},
		{ID: "rg-new", Title: "New", PrimaryType: "EP", SecondaryTypes: []string{"Remix", "Live"}, FirstReleaseDate: "2024-01-01"},
	})

	got := decodeJSON[[]struct {
		MBID           string `json:"mb_id"`
		Title          string `json:"title"`
		SecondaryTypes string `json:"secondary_types"`
		Owned          bool   `json:"owned"`
	}](t, r, "GET", "/api/v1/artists/art-1/discography", token, nil)

	byID := map[string]int{}
	for i, g := range got {
		byID[g.MBID] = i
	}
	if len(got) != 3 {
		t.Fatalf("discography = %+v, want the two live rows plus the stored compilation", got)
	}
	if g := got[byID["rg-owned"]]; !g.Owned {
		t.Errorf("rg-owned = %+v, want it annotated as owned", g)
	}
	if g := got[byID["rg-new"]]; g.Owned || g.Title != "New" || g.SecondaryTypes != "Remix, Live" {
		t.Errorf("rg-new = %+v, want MusicBrainz metadata and nothing owned", g)
	}
	if g := got[byID["rg-comp"]]; !g.Owned {
		t.Errorf("rg-comp = %+v, want the stored-only album kept", g)
	}
}

// TestDiscographyUpstreamFailure: with nothing cached and MusicBrainz refusing, the
// discography is a 502 — the UI's "could not load", not an empty catalogue.
func TestDiscographyUpstreamFailure(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	artistFixture(t, api.DB, "11111111-1111-1111-1111-111111111111", "Band", models.ManagedByAutotaggerr, false)
	refuseMusicBrainz(t)

	w := do(r, "GET", "/api/v1/artists/11111111-1111-1111-1111-111111111111/discography", token, nil)
	expectStatus(t, "GET discography", w.Code, w.Body.String(), http.StatusBadGateway, "could not load the discography")
}

// TestReleaseGroupDetailFallsBackToDiscography: a release-group the collection has no
// row for still renders, with its title and types taken from the cached discography.
func TestReleaseGroupDetailFallsBackToDiscography(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	artistFixture(t, api.DB, "art-1", "Band", models.ManagedByAutotaggerr, false)
	seedDiscography(t, "art-1", []models.MusicBrainzArtistReleaseGroup{
		{ID: "rg-other", Title: "Other", PrimaryType: "Single"},
		{ID: "rg-x", Title: "Unowned", PrimaryType: "Album", SecondaryTypes: []string{"Soundtrack"}, FirstReleaseDate: "1999"},
	})
	api.Meta = &fakeMeta{}

	got := decodeJSON[struct {
		ReleaseGroup struct {
			Title            string `json:"title"`
			PrimaryType      string `json:"primary_type"`
			SecondaryTypes   string `json:"secondary_types"`
			FirstReleaseDate string `json:"first_release_date"`
		} `json:"release_group"`
	}](t, r, "GET", "/api/v1/artists/art-1/release-groups/rg-x", token, nil)
	rg := got.ReleaseGroup
	if rg.Title != "Unowned" || rg.PrimaryType != "Album" || rg.SecondaryTypes != "Soundtrack" || rg.FirstReleaseDate != "1999" {
		t.Errorf("release group = %+v, want it filled from the discography", rg)
	}

	w := do(r, "GET", "/api/v1/artists/nope/release-groups/rg-x", token, nil)
	expectStatus(t, "unknown artist", w.Code, w.Body.String(), http.StatusNotFound, "artist not found")
}

// TestArtistInfoFallsBackToTagsAndCaps: an artist with no curated genres shows its
// most-voted tags instead, capped at the header's limit and skipping blank names.
func TestArtistInfoFallsBackToTagsAndCaps(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	artistFixture(t, api.DB, "art-1", "Band", models.ManagedByAutotaggerr, false)
	api.Meta = &fakeMeta{getArtist: func(string) (models.MusicBrainzArtistLookup, error) {
		var info models.MusicBrainzArtistLookup
		info.Tags = []models.MusicBrainzNamedCount{
			{Name: "e", Count: 1}, {Name: "", Count: 9}, {Name: "a", Count: 8},
			{Name: "b", Count: 7}, {Name: "c", Count: 6}, {Name: "d", Count: 5},
		}
		info.Area.Name = "Sweden"
		return info, nil
	}}

	got := decodeJSON[artistInfoView](t, r, "GET", "/api/v1/artists/art-1/info", token, nil)
	want := []string{"a", "b", "c", "d"}
	if len(got.Genres) != len(want) {
		t.Fatalf("genres = %v, want %v", got.Genres, want)
	}
	for i := range want {
		if got.Genres[i] != want[i] {
			t.Errorf("genres = %v, want %v", got.Genres, want)
			break
		}
	}
	if got.Area != "Sweden" {
		t.Errorf("area = %q, want the country-level area when there is no begin area", got.Area)
	}
}

// TestFollowAndMonitorFailures covers the write and sync failures of the two follow
// endpoints: a refused update is a 500, a discography sync that fails is a 502.
func TestFollowAndMonitorFailures(t *testing.T) {
	failing := &fakeMeta{getArtistRGs: func(string) ([]models.MusicBrainzArtistReleaseGroup, bool, error) {
		return nil, false, errors.New("upstream down")
	}}

	t.Run("monitor sync fails", func(t *testing.T) {
		r, api := setupAPI(t)
		token := loginToken(t, r)
		artistFixture(t, api.DB, "art-1", "Band", models.ManagedByAutotaggerr, false)
		api.Meta = failing
		w := do(r, "POST", "/api/v1/artists/art-1/monitor", token, map[string]any{"monitored": true})
		expectStatus(t, "monitor", w.Code, w.Body.String(), http.StatusBadGateway, "upstream down")
	})
	t.Run("monitor invalid body and update fails", func(t *testing.T) {
		r, api := setupAPI(t)
		token := loginToken(t, r)
		artistFixture(t, api.DB, "art-1", "Band", models.ManagedByAutotaggerr, false)
		w := do(r, "POST", "/api/v1/artists/art-1/monitor", token, "not-an-object")
		expectStatus(t, "monitor malformed", w.Code, w.Body.String(), http.StatusBadRequest, "invalid body")

		failWrites(t, api.DB, &models.CollectionArtist{}, "UPDATE")
		w = do(r, "POST", "/api/v1/artists/art-1/monitor", token, map[string]any{"monitored": false})
		expectStatus(t, "monitor update", w.Code, w.Body.String(), http.StatusInternalServerError, "failed to update artist")
	})
	t.Run("follow sync fails", func(t *testing.T) {
		r, api := setupAPI(t)
		token := loginToken(t, r)
		artistFixture(t, api.DB, "art-1", "Band", models.ManagedByAutotaggerr, true)
		api.Meta = failing
		w := do(r, "POST", "/api/v1/artists/art-1/follow", token, map[string]any{"follow_secondary": true})
		expectStatus(t, "follow", w.Code, w.Body.String(), http.StatusBadGateway, "upstream down")
	})
	t.Run("follow invalid body and update fails", func(t *testing.T) {
		r, api := setupAPI(t)
		token := loginToken(t, r)
		artistFixture(t, api.DB, "art-1", "Band", models.ManagedByAutotaggerr, false)
		w := do(r, "POST", "/api/v1/artists/art-1/follow", token, "not-an-object")
		expectStatus(t, "follow malformed", w.Code, w.Body.String(), http.StatusBadRequest, "invalid body")

		failWrites(t, api.DB, &models.CollectionArtist{}, "UPDATE")
		w = do(r, "POST", "/api/v1/artists/art-1/follow", token, map[string]any{"follow_types": "Album"})
		expectStatus(t, "follow update", w.Code, w.Body.String(), http.StatusInternalServerError, "failed to update the artist")
	})
}

// TestScanFailures: both inline Scan verbs report a rebuild that cannot run as a 500
// rather than an all-zero success.
func TestScanFailures(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	artistFixture(t, api.DB, "art-1", "Band", models.ManagedByAutotaggerr, false)
	dropTable(t, api.DB, &models.LibraryItem{})

	w := do(r, "POST", "/api/v1/scan", token, nil)
	expectStatus(t, "scan collection", w.Code, w.Body.String(), http.StatusInternalServerError, "failed to scan the library")
	w = do(r, "POST", "/api/v1/artists/art-1/scan", token, nil)
	expectStatus(t, "scan artist", w.Code, w.Body.String(), http.StatusInternalServerError, "failed to scan this artist")
}

// TestDesireAndDetachFailures covers the remaining error mappings of the want and
// authority endpoints.
func TestDesireAndDetachFailures(t *testing.T) {
	t.Run("set desire rejected", func(t *testing.T) {
		r, api := setupAPI(t)
		token := loginToken(t, r)
		artistFixture(t, api.DB, "art-1", "Band", models.ManagedByAutotaggerr, false)
		// No release group: nothing to want.
		w := do(r, "POST", "/api/v1/artists/art-1/desires", token, map[string]any{})
		expectStatus(t, "set desire", w.Code, w.Body.String(), http.StatusBadRequest, "")
	})
	t.Run("set desire cannot resolve the manager", func(t *testing.T) {
		r, api := setupAPI(t)
		token := loginToken(t, r)
		dropTable(t, api.DB, &models.CollectionArtist{})
		w := do(r, "POST", "/api/v1/artists/art-1/desires", token, map[string]any{"release_group_mb_id": "rg-1"})
		expectStatus(t, "set desire", w.Code, w.Body.String(), http.StatusInternalServerError, "failed to resolve the artist's manager")
	})
	t.Run("clear desire fails", func(t *testing.T) {
		r, api := setupAPI(t)
		token := loginToken(t, r)
		dropTable(t, api.DB, &models.CollectionDesire{})
		w := do(r, "DELETE", "/api/v1/artists/art-1/desires?release_group_mb_id=rg-1", token, nil)
		expectStatus(t, "clear desire", w.Code, w.Body.String(), http.StatusInternalServerError, "failed to clear the desire")
	})
	t.Run("detach write fails", func(t *testing.T) {
		r, api := setupAPI(t)
		token := loginToken(t, r)
		seedManagedArtist(t, api)
		failWrites(t, api.DB, &models.CollectionDesire{}, "UPDATE")
		w := do(r, "POST", "/api/v1/artists/art-1/detach", token, nil)
		expectStatus(t, "detach", w.Code, w.Body.String(), http.StatusInternalServerError, "failed to detach the artist")
	})
	t.Run("reattach unknown", func(t *testing.T) {
		r, _ := setupAPI(t)
		token := loginToken(t, r)
		w := do(r, "DELETE", "/api/v1/artists/nope/detach", token, nil)
		expectStatus(t, "reattach", w.Code, w.Body.String(), http.StatusNotFound, "artist not found")
	})
	t.Run("reattach write fails", func(t *testing.T) {
		r, api := setupAPI(t)
		token := loginToken(t, r)
		if err := api.DB.Create(&models.CollectionArtist{
			MBID: "art-1", Name: "Band", ManagedBy: models.ManagedByAutotaggerr, ManagerDetached: true,
		}).Error; err != nil {
			t.Fatalf("artist: %v", err)
		}
		failWrites(t, api.DB, &models.CollectionArtist{}, "UPDATE")
		w := do(r, "DELETE", "/api/v1/artists/art-1/detach", token, nil)
		expectStatus(t, "reattach", w.Code, w.Body.String(), http.StatusInternalServerError, "failed to reattach the artist")
	})
}

// TestSyncLidarrRecordsAnEvent: the collection-wide sync answers 202 and records its
// pass as an Activity event. With no Lidarr artists in the collection the pass returns
// before any HTTP call and says why, so the test needs no Lidarr.
func TestSyncLidarrRecordsAnEvent(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	w := do(r, "POST", "/api/v1/artists/art-1/sync-lidarr", token, nil)
	expectStatus(t, "artist sync without manager", w.Code, w.Body.String(), http.StatusBadRequest, "no enabled Lidarr manager")

	if err := api.DB.Create(&models.Manager{
		Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true,
		LidarrBaseURL: "http://127.0.0.1:1", LidarrAPIKey: "k",
	}).Error; err != nil {
		t.Fatalf("create manager: %v", err)
	}

	w = do(r, "POST", "/api/v1/collection/sync-lidarr", token, map[string]any{"ignore_cache": true})
	expectStatus(t, "sync", w.Code, w.Body.String(), http.StatusAccepted, "lidarr sync started")

	deadline := time.Now().Add(5 * time.Second)
	var ev models.Event
	for time.Now().Before(deadline) {
		err := api.DB.Where("type = ? AND status <> ?", models.EventTypeLidarrSync, models.EventStatusRunning).First(&ev).Error
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ev.ID.String() == "00000000-0000-0000-0000-000000000000" {
		t.Fatal("the sync never finished its event")
	}
	if ev.Status != models.EventStatusOK {
		t.Errorf("event status = %q, want ok (an empty pass is not a failure)", ev.Status)
	}
}

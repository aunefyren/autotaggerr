package modules

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/metadata"
	"github.com/aunefyren/autotaggerr/models"
)

// MusicBrainz failure classification for the artist, discography-page, search and
// generic-JSON fetchers. The rule everywhere: 404/410 is Gone (and recorded), 5xx
// and 429 are Transient, any other status is a plain error, and a stale cached copy
// beats any failure. All of it against withMockMB — no musicbrainz.org.

const coverageArtistID = "a74b1b7f-71a5-4011-9441-d0b5e4122711"

func statusHandler(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// seedStaleArtist puts an expired artist lookup in the entity cache.
func seedStaleArtist(t *testing.T) {
	t.Helper()
	mbCachePut(models.MBEntityArtist, coverageArtistID, models.MusicBrainzArtistLookup{ID: coverageArtistID, Name: "Radiohead"})
	MusicbrainzExpireEntity(models.MBEntityArtist, coverageArtistID)
}

func TestGetMusicBrainzArtistOnceClassifiesFailures(t *testing.T) {
	cases := []struct {
		name          string
		handler       http.HandlerFunc
		wantGone      bool
		wantTransient bool
		wantText      string
	}{
		{"gone", statusHandler(http.StatusGone, "deleted"), true, false, ""},
		{"unavailable", statusHandler(http.StatusServiceUnavailable, "busy"), false, true, ""},
		{"bad request", statusHandler(http.StatusBadRequest, "bad"), false, false, "HTTP 400"},
		{"not json", statusHandler(http.StatusOK, "<html>"), false, false, "failed to parse artist"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withMigrationDB(t)
			withMockMB(t, tc.handler)

			_, err := getMusicBrainzArtistOnce(coverageArtistID)
			if err == nil {
				t.Fatal("a failed artist lookup returned no error")
			}
			if got := errors.Is(err, ErrEntityGone); got != tc.wantGone {
				t.Errorf("ErrEntityGone = %v, want %v (%v)", got, tc.wantGone, err)
			}
			if got := errors.Is(err, ErrTransient); got != tc.wantTransient {
				t.Errorf("ErrTransient = %v, want %v (%v)", got, tc.wantTransient, err)
			}
			if tc.wantText != "" && !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("err = %q, want %q", err, tc.wantText)
			}
		})
	}
}

// Stale beats blank on every failure path — and a deletion is still recorded even
// when the stale copy is what gets served.
func TestGetMusicBrainzArtistOnceServesStale(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"gone", statusHandler(http.StatusNotFound, "")},
		{"bad request", statusHandler(http.StatusBadRequest, "")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := withMigrationDB(t)
			withMockMB(t, tc.handler)
			seedStaleArtist(t)

			got, err := getMusicBrainzArtistOnce(coverageArtistID)
			if err != nil || got.Name != "Radiohead" {
				t.Fatalf("got (%+v, %v), want the stale artist", got, err)
			}
			var n int64
			db.Model(&models.MusicbrainzMigration{}).Where("old_mb_id = ?", coverageArtistID).Count(&n)
			if want := int64(map[bool]int{true: 1, false: 0}[tc.name == "gone"]); n != want {
				t.Errorf("migration rows = %d, want %d", n, want)
			}
		})
	}

	t.Run("transport", func(t *testing.T) {
		srv := withMockMB(t, statusHandler(http.StatusOK, "{}"))
		srv.Close()
		seedStaleArtist(t)
		got, err := getMusicBrainzArtistOnce(coverageArtistID)
		if err != nil || got.Name != "Radiohead" {
			t.Fatalf("got (%+v, %v), want the stale artist on a dead server", got, err)
		}
	})

	t.Run("transport without stale copy", func(t *testing.T) {
		srv := withMockMB(t, statusHandler(http.StatusOK, "{}"))
		srv.Close()
		if _, err := getMusicBrainzArtistOnce(coverageArtistID); !errors.Is(err, ErrTransient) {
			t.Fatalf("err = %v, want ErrTransient", err)
		}
	})
}

func TestGetMusicBrainzArtistOnceBadBaseURL(t *testing.T) {
	withMockMB(t, statusHandler(http.StatusOK, "{}"))
	musicbrainzBaseURL = "http://bad host"
	if _, err := getMusicBrainzArtistOnce(coverageArtistID); err == nil {
		t.Error("an unparseable base URL fetched an artist")
	}
	if _, err := fetchArtistReleaseGroupPage(coverageArtistID, 0); err == nil {
		t.Error("an unparseable base URL fetched a discography page")
	}
	if _, err := searchMusicBrainzReleasesOnce(metadata.ReleaseSearchQuery{Text: "x"}); err == nil {
		t.Error("an unparseable base URL searched")
	}
	if err := musicbrainzGetJSONOnce("http://bad host/x", &struct{}{}); err == nil {
		t.Error("an unparseable endpoint fetched")
	}
}

func TestFetchArtistReleaseGroupPageClassifiesFailures(t *testing.T) {
	cases := []struct {
		name                    string
		handler                 http.HandlerFunc
		wantGone, wantTransient bool
		wantText                string
	}{
		{"gone", statusHandler(http.StatusNotFound, ""), true, false, ""},
		{"rate limited", statusHandler(http.StatusTooManyRequests, ""), false, true, ""},
		{"bad request", statusHandler(http.StatusBadRequest, "nope"), false, false, "HTTP 400"},
		{"not json", statusHandler(http.StatusOK, "{"), false, false, "failed to parse artist release-groups"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withMigrationDB(t)
			withMockMB(t, tc.handler)
			_, err := fetchArtistReleaseGroupPage(coverageArtistID, 0)
			if err == nil {
				t.Fatal("no error")
			}
			if errors.Is(err, ErrEntityGone) != tc.wantGone || errors.Is(err, ErrTransient) != tc.wantTransient {
				t.Errorf("err = %v, want gone=%v transient=%v", err, tc.wantGone, tc.wantTransient)
			}
			if tc.wantText != "" && !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("err = %q, want %q", err, tc.wantText)
			}
		})
	}

	t.Run("transport", func(t *testing.T) {
		srv := withMockMB(t, statusHandler(http.StatusOK, "{}"))
		srv.Close()
		if _, err := fetchArtistReleaseGroupPage(coverageArtistID, 0); !errors.Is(err, ErrTransient) {
			t.Errorf("err = %v, want ErrTransient", err)
		}
	})
}

func TestSearchMusicBrainzReleasesOnceFailuresAndClamping(t *testing.T) {
	var gotQuery string
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"count":1,"releases":[{"id":"r1","title":"OK Computer"}]}`))
	})
	page, err := searchMusicBrainzReleasesOnce(metadata.ReleaseSearchQuery{Release: "OK Computer", Limit: 10000, Offset: -5})
	if err != nil || page.Count != 1 || len(page.Releases) != 1 || page.Offset != 0 {
		t.Fatalf("search = (%+v, %v)", page, err)
	}
	if !strings.Contains(gotQuery, "offset=0") || strings.Contains(gotQuery, "limit=10000") {
		t.Errorf("query = %q, want a clamped limit and non-negative offset", gotQuery)
	}

	if page, err := searchMusicBrainzReleasesOnce(metadata.ReleaseSearchQuery{}); err != nil || page.Count != 0 {
		t.Errorf("empty query = (%+v, %v), want an empty page without a request", page, err)
	}

	cases := []struct {
		name          string
		handler       http.HandlerFunc
		wantTransient bool
		wantText      string
	}{
		{"unavailable", statusHandler(http.StatusBadGateway, ""), true, ""},
		{"bad request", statusHandler(http.StatusBadRequest, "syntax"), false, "HTTP 400"},
		{"not json", statusHandler(http.StatusOK, "nope"), false, "failed to parse MusicBrainz search"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withMockMB(t, tc.handler)
			_, err := searchMusicBrainzReleasesOnce(metadata.ReleaseSearchQuery{Text: "x"})
			if err == nil || errors.Is(err, ErrTransient) != tc.wantTransient {
				t.Fatalf("err = %v, want transient=%v", err, tc.wantTransient)
			}
			if tc.wantText != "" && !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("err = %q, want %q", err, tc.wantText)
			}
		})
	}

	t.Run("transport", func(t *testing.T) {
		srv := withMockMB(t, statusHandler(http.StatusOK, "{}"))
		srv.Close()
		if _, err := searchMusicBrainzReleasesOnce(metadata.ReleaseSearchQuery{Text: "x"}); !errors.Is(err, ErrTransient) {
			t.Errorf("err = %v, want ErrTransient", err)
		}
	})
}

func TestMusicbrainzGetJSONOnceClassifiesFailures(t *testing.T) {
	srv := withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/busy":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/missing":
			http.Error(w, "not here", http.StatusNotFound)
		case "/garbled":
			_, _ = w.Write([]byte("{"))
		default:
			_, _ = w.Write([]byte(`{"id":"x"}`))
		}
	})
	var out struct{ ID string }

	if err := musicbrainzGetJSONOnce(srv.URL+"/busy", &out); !errors.Is(err, ErrTransient) {
		t.Errorf("busy err = %v, want ErrTransient", err)
	}
	// The generic helper cannot know what a 404 means, so it carries the status.
	if err := musicbrainzGetJSONOnce(srv.URL+"/missing", &out); HTTPStatus(err) != http.StatusNotFound ||
		!strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("missing err = %v, want a StatusError carrying 404", err)
	}
	if err := musicbrainzGetJSONOnce(srv.URL+"/garbled", &out); err == nil || !strings.Contains(err.Error(), "failed to parse") {
		t.Errorf("garbled err = %v", err)
	}
	if err := musicbrainzGetJSONOnce(srv.URL+"/ok", &out); err != nil || out.ID != "x" {
		t.Errorf("ok = (%+v, %v)", out, err)
	}

	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	if err := musicbrainzGetJSONOnce(dead.URL, &out); !errors.Is(err, ErrTransient) {
		t.Errorf("dead err = %v, want ErrTransient", err)
	}
}

// A change closed as resolved-elsewhere goes back in the queue when it is seen
// again; one a person dismissed does not.
func TestMigrationReopensOnlyExternallyClosedRows(t *testing.T) {
	db := withMigrationDB(t)
	now := time.Now()
	rows := []models.MusicbrainzMigration{
		{EntityType: models.MigrationEntityArtist, OldMBID: "ext-del", Kind: models.MigrationKindDeleted,
			Status: models.MigrationStatusResolved, Resolution: models.MigrationResolutionExternal, ResolvedAt: &now, DetectedAt: now},
		{EntityType: models.MigrationEntityArtist, OldMBID: "ext-red", NewMBID: "new", Kind: models.MigrationKindRedirect,
			Status: models.MigrationStatusResolved, Resolution: models.MigrationResolutionExternal, ResolvedAt: &now, DetectedAt: now},
		{EntityType: models.MigrationEntityArtist, OldMBID: "dismissed", Kind: models.MigrationKindDeleted,
			Status: models.MigrationStatusDismissed, Resolution: models.MigrationResolutionDismissed, DetectedAt: now},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	RecordDeletion(models.MigrationEntityArtist, "ext-del")
	RecordRedirect(models.MigrationEntityArtist, "ext-red", "new", "Name")
	RecordDeletion(models.MigrationEntityArtist, "dismissed")

	status := func(old string) (string, string) {
		var m models.MusicbrainzMigration
		if err := db.First(&m, "old_mb_id = ?", old).Error; err != nil {
			t.Fatalf("read %s: %v", old, err)
		}
		return m.Status, m.Resolution
	}
	for _, old := range []string{"ext-del", "ext-red"} {
		if s, r := status(old); s != models.MigrationStatusPending || r != "" {
			t.Errorf("%s = (%s, %q), want re-opened as pending", old, s, r)
		}
	}
	if s, _ := status("dismissed"); s != models.MigrationStatusDismissed {
		t.Errorf("a dismissed change re-opened: %s", s)
	}
}

// With the database gone, recording only logs: a fetch must never fail because its
// bookkeeping could not be written.
func TestMigrationRecordingSurvivesADeadDatabase(t *testing.T) {
	closedCacheDB(t)
	resetMap()
	t.Cleanup(resetMap)
	RecordDeletion(models.MigrationEntityArtist, "x")
	RecordRedirect(models.MigrationEntityArtist, "x", "y", "n")

	// The in-memory half of a drop still happens when the database half fails.
	musicbrainzReleaseCacheMu.Lock()
	musicbrainzReleaseCache["x"] = models.CachedMusicBrainzRelease{}
	musicbrainzReleaseCacheMu.Unlock()
	DropCachedRelease("x")
	musicbrainzReleaseCacheMu.Lock()
	_, still := musicbrainzReleaseCache["x"]
	musicbrainzReleaseCacheMu.Unlock()
	if still {
		t.Error("DropCachedRelease kept the in-memory entry when the database failed")
	}
}

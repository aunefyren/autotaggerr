package modules

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/aunefyren/autotaggerr/metadata"
	"github.com/aunefyren/autotaggerr/models"
)

// failThenServe returns a handler that answers `failures` requests with `status` and
// every request after that with `body` as JSON.
func failThenServe(hits *int32, failures int32, status int, body any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(hits, 1) <= failures {
			w.WriteHeader(status)
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}
}

// TestReleaseFetchSurvivesOneTransientFailure is the case the retry exists for: a
// single 503 in the middle of a run. Without it the file's release is unresolved, and
// everything downstream — the correlation, the album's place in the disk view — is
// decided by one unlucky request.
func TestReleaseFetchSurvivesOneTransientFailure(t *testing.T) {
	want := models.MusicBrainzReleaseResponse{ID: "rel-flaky", Title: "Kid A"}
	var hits int32
	withMockMB(t, failThenServe(&hits, 1, http.StatusServiceUnavailable, want))

	got, err := GetMusicBrainzRelease("rel-flaky")
	if err != nil {
		t.Fatalf("GetMusicBrainzRelease: %v, want the retry to succeed", err)
	}
	if got.Title != want.Title {
		t.Errorf("title = %q, want %q", got.Title, want.Title)
	}
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Errorf("server hit %d times, want 2 (the failure and the retry)", n)
	}
}

// TestReleaseFetchGivesUpAfterTheRetry pins the other end: the retry is one attempt,
// not a loop. A service that is genuinely down must produce an answer — ErrTransient,
// so the caller keeps the file's correlation — rather than an unbounded retry storm
// against a service already in trouble.
func TestReleaseFetchGivesUpAfterTheRetry(t *testing.T) {
	var hits int32
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	_, err := GetMusicBrainzRelease("rel-down")
	if !errors.Is(err, ErrTransient) {
		t.Fatalf("error = %v, want ErrTransient", err)
	}
	if n := atomic.LoadInt32(&hits); n != int32(mbTransientRetries+1) {
		t.Errorf("server hit %d times, want %d", n, mbTransientRetries+1)
	}
}

// TestOnlyTransientFailuresAreRetried guards the classification. A 404 is an *answer*
// — the release is gone, and a migration is recorded from it — while a 400 is a
// request this client will keep getting wrong. Repeating either would at best waste
// the rate limit and at worst hide a bug behind a retry.
func TestOnlyTransientFailuresAreRetried(t *testing.T) {
	cases := []struct {
		name   string
		status int
		mbID   string
	}{
		{"gone", http.StatusNotFound, "rel-gone"},
		{"bad request", http.StatusBadRequest, "rel-bad"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits int32
			withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&hits, 1)
				w.WriteHeader(tc.status)
			})

			if _, err := GetMusicBrainzRelease(tc.mbID); err == nil {
				t.Fatalf("HTTP %d: expected an error", tc.status)
			}
			if n := atomic.LoadInt32(&hits); n != 1 {
				t.Errorf("HTTP %d: server hit %d times, want 1 — this is not a transient failure", tc.status, n)
			}
		})
	}
}

// TestUnparseableResponseIsNotRetried covers the third kind: MusicBrainz answered 200
// with something that is not the shape we expect. Asking again gets the same bytes.
func TestUnparseableResponseIsNotRetried(t *testing.T) {
	var hits int32
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte("<html>not json</html>"))
	})

	if _, err := GetMusicBrainzRelease("rel-garbage"); err == nil {
		t.Fatal("expected a parse error")
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("server hit %d times, want 1 — a parse failure is not retried", n)
	}
}

// TestReleaseSearchSurvivesOneTransientFailure covers the user-facing path. A search
// has no cache to fall back on, so a blip here is a person told to try again.
func TestReleaseSearchSurvivesOneTransientFailure(t *testing.T) {
	want := models.MusicBrainzReleaseSearchResponse{
		Count:    1,
		Releases: []models.MusicBrainzReleaseSearchResult{{ID: "rel-1", Title: "Amnesiac"}},
	}
	var hits int32
	withMockMB(t, failThenServe(&hits, 1, http.StatusServiceUnavailable, want))

	page, err := SearchMusicBrainzReleases(metadata.ReleaseSearchQuery{Text: "amnesiac"})
	if err != nil {
		t.Fatalf("SearchMusicBrainzReleases: %v, want the retry to succeed", err)
	}
	if len(page.Releases) != 1 || page.Releases[0].ID != "rel-1" {
		t.Errorf("page = %+v, want the single seeded hit", page)
	}
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Errorf("server hit %d times, want 2 (the failure and the retry)", n)
	}
}

// TestDiscographyRetriesThePageNotTheWholeWalk pins the placement inside the paging
// loop. A transient failure on a later page must cost one repeat of *that* page —
// restarting the discography would re-spend a rate-limited request per page already
// read, which is the opposite of what a retry is for.
func TestDiscographyRetriesThePageNotTheWholeWalk(t *testing.T) {
	// Two full pages of results, so paging continues past the first.
	full := make([]models.MusicBrainzArtistReleaseGroup, artistReleaseGroupPageSize)
	for i := range full {
		full[i] = models.MusicBrainzArtistReleaseGroup{ID: "rg", Title: "Album", PrimaryType: "Album"}
	}

	var hits, failed int32
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		offset := r.URL.Query().Get("offset")
		// Fail the second page exactly once.
		if offset != "0" && atomic.AddInt32(&failed, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		groups := full
		count := artistReleaseGroupPageSize * 2
		if offset != "0" {
			groups = full[:1]
		}
		_ = json.NewEncoder(w).Encode(models.MusicBrainzArtistReleaseGroups{
			Count: count, ReleaseGroups: groups,
		})
	})

	groups, complete, err := GetMusicBrainzArtistReleaseGroups("artist-flaky")
	if err != nil {
		t.Fatalf("GetMusicBrainzArtistReleaseGroups: %v, want the retry to succeed", err)
	}
	if !complete {
		t.Error("discography reported incomplete after a recovered page")
	}
	if len(groups) != artistReleaseGroupPageSize+1 {
		t.Errorf("collected %d release-groups, want %d — a restarted walk would duplicate page one",
			len(groups), artistReleaseGroupPageSize+1)
	}
	// Page 1, page 2 (503), page 2 again. A whole-walk retry would be four.
	if n := atomic.LoadInt32(&hits); n != 3 {
		t.Errorf("server hit %d times, want 3 (page 1, the failed page 2, its retry)", n)
	}
}

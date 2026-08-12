package modules

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// A 404 from the editions browse is only a deletion once the release-group lookup
// agrees. These tests pin both halves of that, because the whole value of the
// confirmation is in the case where the two answers differ.

// TestEditionsBrowse404ConfirmedIsGone: browse 404 + lookup 404 = a GoneError naming
// the release-group, which is what lets the refresh report it as gone rather than as
// one more failed row to retry tomorrow.
func TestEditionsBrowse404ConfirmedIsGone(t *testing.T) {
	const rgID = "85ab4626-9004-4e58-9c67-e9ef7da5f19b"

	var browsed, lookedUp bool
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/release-group/"):
			lookedUp = true
		case r.URL.Path == "/release":
			browsed = true
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Not Found"}`))
	})

	_, err := GetMusicBrainzReleaseGroupReleases(rgID)
	if err == nil {
		t.Fatal("want an error for a release-group that resolves nowhere")
	}
	if !browsed || !lookedUp {
		t.Fatalf("want both requests made; browsed=%v lookedUp=%v", browsed, lookedUp)
	}

	entity, mbid, gone := GoneEntity(err)
	if !gone {
		t.Fatalf("want a GoneError, got %v", err)
	}
	if entity != models.MigrationEntityReleaseGroup {
		t.Errorf("entity = %q, want %q", entity, models.MigrationEntityReleaseGroup)
	}
	if mbid != rgID {
		t.Errorf("mbid = %q, want %q", mbid, rgID)
	}
	if !errors.Is(err, ErrEntityGone) {
		t.Error("want the error to satisfy errors.Is(err, ErrEntityGone)")
	}
}

// TestEditionsBrowse404ButGroupResolves: the browse 404s and the lookup does not.
// The group exists, so nothing may be retired on the strength of the browse — this is
// the case the second request is paid for.
func TestEditionsBrowse404ButGroupResolves(t *testing.T) {
	const rgID = "b3073a10-2d7c-484c-b387-e49ae629da3d"

	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/release-group/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"` + rgID + `","title":"DeBÍ TiRAR MáS FOToS"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Not Found"}`))
	})

	_, err := GetMusicBrainzReleaseGroupReleases(rgID)
	if err == nil {
		t.Fatal("want the browse failure to still be an error")
	}
	if _, _, gone := GoneEntity(err); gone {
		t.Fatalf("must not report gone when the release-group resolves: %v", err)
	}
}

// TestEditionsBrowse404LookupUnavailable: a confirmation that fails transiently proves
// nothing. Recording a deletion off an outage is the mistake the confirmation exists to
// prevent, so the browse error is returned unchanged.
func TestEditionsBrowse404LookupUnavailable(t *testing.T) {
	const rgID = "1162ace0-0000-0000-0000-0000006e0ef2"

	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/release-group/") {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Not Found"}`))
	})

	_, err := GetMusicBrainzReleaseGroupReleases(rgID)
	if err == nil {
		t.Fatal("want an error")
	}
	if _, _, gone := GoneEntity(err); gone {
		t.Fatalf("must not report gone when the confirmation itself failed: %v", err)
	}
}

// TestEditionsTransientKeepsStale: a 503 is not evidence about the group, so the stale
// edition list is still served. Guards the ordering of the gone check against the
// stale fallback — the gone branch runs first and must not swallow this case.
func TestEditionsTransientKeepsStale(t *testing.T) {
	const rgID = "2d0b8870-0000-0000-0000-000000b94013"

	// Seeded after withMockMB, which resets the entity cache.
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	mbCachePut(models.MBEntityEditions, rgID, []models.MusicBrainzReleaseSearchResult{{ID: "cached"}})
	MusicbrainzExpireEntity(models.MBEntityEditions, rgID)

	got, err := GetMusicBrainzReleaseGroupReleases(rgID)
	if err != nil {
		t.Fatalf("want the stale list served on an outage, got %v", err)
	}
	if len(got) != 1 || got[0].ID != "cached" {
		t.Fatalf("want the cached edition list, got %+v", got)
	}
}

// TestHTTPStatusReadsThroughRetry: the status has to survive the retry wrapper, or the
// 404 branch never fires in production even though the direct call reports it.
func TestHTTPStatusReadsThroughRetry(t *testing.T) {
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Not Found"}`))
	})

	var out map[string]any
	err := musicbrainzGetJSON(musicbrainzBaseURL+"/release-group/x", &out)
	if got := HTTPStatus(err); got != http.StatusNotFound {
		t.Fatalf("HTTPStatus = %d, want 404 (err %v)", got, err)
	}
}

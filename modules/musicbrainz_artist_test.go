package modules

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

func TestGetMusicBrainzArtistReleaseGroups(t *testing.T) {
	withMockMB(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(models.MusicBrainzArtistReleaseGroups{
			Count: 2,
			ReleaseGroups: []models.MusicBrainzArtistReleaseGroup{
				{ID: "rg-1", Title: "Debut", PrimaryType: "Album"},
				{ID: "rg-2", Title: "Live at X", PrimaryType: "Album", SecondaryTypes: []string{"Live"}},
			},
		})
	})

	rgs, complete, err := GetMusicBrainzArtistReleaseGroups("art-1")
	if err != nil {
		t.Fatalf("GetMusicBrainzArtistReleaseGroups: %v", err)
	}
	if len(rgs) != 2 || rgs[0].ID != "rg-1" || rgs[1].SecondaryTypes[0] != "Live" {
		t.Errorf("parsed release-groups = %+v", rgs)
	}
	if !complete {
		t.Error("a single short page is a complete discography")
	}
}

// A discography longer than the page cap must report itself as incomplete, because
// the prune path treats "absent from this list" as "MusicBrainz no longer has it".
func TestArtistReleaseGroupsReportTruncation(t *testing.T) {
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		// Count far exceeds what the page cap can fetch, so paging stops early.
		page := make([]models.MusicBrainzArtistReleaseGroup, artistReleaseGroupPageSize)
		for i := range page {
			page[i] = models.MusicBrainzArtistReleaseGroup{ID: "rg", Title: "T"}
		}
		_ = json.NewEncoder(w).Encode(models.MusicBrainzArtistReleaseGroups{
			Count:         10000,
			ReleaseGroups: page,
		})
	})

	rgs, complete, err := GetMusicBrainzArtistReleaseGroups("art-big")
	if err != nil {
		t.Fatalf("GetMusicBrainzArtistReleaseGroups: %v", err)
	}
	if complete {
		t.Error("a discography cut off at the page cap must not claim to be complete")
	}
	if len(rgs) != artistReleaseGroupPageSize*maxArtistReleaseGroupPages {
		t.Errorf("fetched %d groups, want the full page budget", len(rgs))
	}
}

func TestCachedRelease(t *testing.T) {
	withMockMB(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(models.MusicBrainzReleaseResponse{ID: "rel-x", Title: "Cached"})
	})

	if _, ok := CachedRelease("rel-x"); ok {
		t.Error("release should not be cached before fetch")
	}
	if _, err := GetMusicBrainzRelease("rel-x"); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	rel, ok := CachedRelease("rel-x")
	if !ok || rel.Title != "Cached" {
		t.Errorf("CachedRelease after fetch = %+v ok=%v", rel, ok)
	}
}

package modules

import (
	"net/http"
	"testing"

	"github.com/aunefyren/autotaggerr/metadata"
)

// TestMetadataSourceAdapterDelegates exercises the real adapter's six methods against
// the MB stub, confirming each delegates to its free function without error. An empty
// JSON object unmarshals to a zero value for every response shape, so this covers the
// delegation wiring — the network/parse behaviour itself is tested per free function.
func TestMetadataSourceAdapterDelegates(t *testing.T) {
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})

	src := NewMetadataSource()
	if _, err := src.GetRelease("rel-1"); err != nil {
		t.Errorf("GetRelease: %v", err)
	}
	if _, err := src.GetArtist("art-1"); err != nil {
		t.Errorf("GetArtist: %v", err)
	}
	if _, _, err := src.GetArtistReleaseGroups("art-1"); err != nil {
		t.Errorf("GetArtistReleaseGroups: %v", err)
	}
	if _, err := src.GetReleaseGroupReleases("rg-1"); err != nil {
		t.Errorf("GetReleaseGroupReleases: %v", err)
	}
	if _, err := src.SearchReleases(metadata.ReleaseSearchQuery{Artist: "x"}); err != nil {
		t.Errorf("SearchReleases: %v", err)
	}
	if _, err := src.SearchArtists("x"); err != nil {
		t.Errorf("SearchArtists: %v", err)
	}
}

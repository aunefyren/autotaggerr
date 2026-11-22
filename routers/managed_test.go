package routers

import (
	"net/http"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// TestManagedArtistsRoutes: the list carries managed and its reason and narrows by
// scope; Manage and Stop managing move an artist between the two, and Stop managing
// is refused while they are followed; a manager listing an artist reaches the cached
// list without any collection row changing.
func TestManagedArtistsRoutes(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	artistFixture(t, api.DB, "album", "Album Artist", models.ManagedByAutotaggerr, false)
	artistFixture(t, api.DB, "featured", "Featured", models.ManagedByAutotaggerr, false)
	artistFixture(t, api.DB, "followed", "Followed", models.ManagedByAutotaggerr, true)
	releaseGroupFixture(t, api.DB, "album", rgFixture{mbid: "rg-1", title: "Record", primary: "Album", owned: true, ownedTracks: 5, totalTracks: 5})
	api.DB.Create(&models.CollectionReleaseGroupArtist{ReleaseGroupMBID: "rg-1", ArtistMBID: "featured", Position: 1, FromDisk: true})

	names := func(list []artistSummary) map[string]string {
		out := map[string]string{}
		for _, ar := range list {
			out[ar.MBID] = ar.ManagedReason
		}
		return out
	}
	all := decodeJSON[[]artistSummary](t, r, http.MethodGet, "/api/v1/artists", token, nil)
	if got := names(all); len(got) != 3 || got["album"] != "album_artist" || got["featured"] != "" || got["followed"] != "followed" {
		t.Fatalf("every artist = %v", got)
	}
	managed := decodeJSON[[]artistSummary](t, r, http.MethodGet, "/api/v1/artists?scope=managed", token, nil)
	if got := names(managed); len(got) != 2 || got["album"] == "" || got["followed"] == "" {
		t.Errorf("scope=managed = %v", got)
	}
	present := decodeJSON[[]artistSummary](t, r, http.MethodGet, "/api/v1/artists?scope=present", token, nil)
	if len(present) != 1 || present[0].MBID != "featured" || present[0].Managed {
		t.Errorf("scope=present = %+v", present)
	}
	w := do(r, http.MethodGet, "/api/v1/artists?scope=mine", token, nil)
	expectStatus(t, "bad scope", w.Code, w.Body.String(), http.StatusBadRequest, "scope")

	// Manage the featured artist: managed, and so on the artist page too.
	type artistBody struct {
		Artist artistView `json:"artist"`
	}
	got := decodeJSON[artistBody](t, r, http.MethodPost, "/api/v1/artists/featured/manage", token, nil)
	if !got.Artist.Managed || got.Artist.ManagedReason != "added" {
		t.Errorf("manage = %+v", got.Artist)
	}
	detail := decodeJSON[artistBody](t, r, http.MethodGet, "/api/v1/artists/featured", token, nil)
	if !detail.Artist.Managed {
		t.Error("the artist page does not say a managed artist is managed")
	}
	got = decodeJSON[artistBody](t, r, http.MethodDelete, "/api/v1/artists/featured/manage", token, nil)
	if got.Artist.Managed {
		t.Errorf("stop managing = %+v", got.Artist)
	}
	w = do(r, http.MethodDelete, "/api/v1/artists/followed/manage", token, nil)
	expectStatus(t, "stop managing a followed artist", w.Code, w.Body.String(), http.StatusConflict, "unfollow")
	w = do(r, http.MethodPost, "/api/v1/artists/nobody/manage", token, nil)
	expectStatus(t, "manage an unknown artist", w.Code, w.Body.String(), http.StatusNotFound, "")

	// A Lidarr listing makes an artist managed, though no collection row moves. The
	// list is cached between writes, so the fingerprint has to see the link.
	api.DB.Model(&models.CollectionArtist{}).Where("mb_id = ?", "featured").Update("manage_override", models.ManageAuto)
	decodeJSON[[]artistSummary](t, r, http.MethodGet, "/api/v1/artists", token, nil)
	lidarr := models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true}
	api.DB.Create(&lidarr)
	api.DB.Create(&models.ManagerLink{ManagerID: lidarr.ID, EntityType: models.ManagerLinkArtist, MBID: "featured"})
	after := decodeJSON[[]artistSummary](t, r, http.MethodGet, "/api/v1/artists", token, nil)
	if names(after)["featured"] != "lidarr" {
		t.Errorf("after Lidarr listed them = %v, want featured managed through Lidarr", names(after))
	}

	// The artist page's albums say whether each is one the user has or manages.
	detailRGs := decodeJSON[struct {
		ReleaseGroups []releaseGroupView `json:"release_groups"`
	}](t, r, http.MethodGet, "/api/v1/artists/album", token, nil)
	if len(detailRGs.ReleaseGroups) != 1 || !detailRGs.ReleaseGroups[0].Tracked {
		t.Errorf("an owned album is not tracked: %+v", detailRGs.ReleaseGroups)
	}

	// The graph answers both ways, and its artist search answers a list, empty for no query.
	for _, path := range []string{"/api/v1/insights/graph", "/api/v1/insights/graph?everyone=1",
		"/api/v1/insights/graph/artists?q=feat", "/api/v1/insights/graph/artists"} {
		if w := do(r, http.MethodGet, path, token, nil); w.Code != http.StatusOK {
			t.Errorf("%s = %d: %s", path, w.Code, w.Body.String())
		}
	}
}

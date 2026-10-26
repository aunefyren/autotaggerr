package routers

import (
	"net/http"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// TestFollowIntoSeveralLibraries: one set of follow settings, wanted into several
// native libraries. Following in a library adds it; unfollowing removes only it; the
// artist view says where following wants into, and each library's view agrees.
func TestFollowIntoSeveralLibraries(t *testing.T) {
	r, api := setupAPI(t)
	api.Meta = &fakeMeta{}
	token := loginToken(t, r)

	a := models.Library{Name: "Music A", Path: "/a", Enabled: true}
	b := models.Library{Name: "Music B", Path: "/b", Enabled: true}
	for _, row := range []any{&a, &b, &models.CollectionArtist{MBID: "art-1", Name: "Artist", ManagedBy: models.ManagedByAutotaggerr}} {
		if err := api.DB.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	follow := func(on bool, library string) {
		t.Helper()
		body := map[string]any{"monitored": on, "library_id": library}
		if w := do(r, http.MethodPost, "/api/v1/artists/art-1/follow", token, body); w.Code != http.StatusOK {
			t.Fatalf("follow %v in %s = %d: %s", on, library, w.Code, w.Body.String())
		}
	}
	type view struct {
		Artist struct {
			Following   bool     `json:"following"`
			FollowingIn []string `json:"following_in"`
		} `json:"artist"`
	}
	read := func(query string) view {
		t.Helper()
		return decodeJSON[view](t, r, "GET", "/api/v1/artists/art-1"+query, token, nil)
	}

	follow(true, a.ID.String())
	follow(true, b.ID.String())
	if got := read("").Artist.FollowingIn; len(got) != 2 {
		t.Errorf("following in = %v, want both libraries", got)
	}
	if !read("?library="+a.ID.String()).Artist.Following || !read("?library="+b.ID.String()).Artist.Following {
		t.Error("a library the follow names does not read as followed")
	}

	follow(false, a.ID.String())
	if got := read("").Artist.FollowingIn; len(got) != 1 || got[0] != b.ID.String() {
		t.Errorf("after unfollowing A, following in = %v, want B", got)
	}
	if read("?library=" + a.ID.String()).Artist.Following {
		t.Error("A still reads as followed")
	}
	got := callMCP[mcpArtistDetail](t, mcpSession(t, api, mcpReader), "get_artist", map[string]any{"mbid": "art-1"})
	if len(got.FollowingIn) != 1 || got.FollowingIn[0] != "Music B" {
		t.Errorf("get_artist following_in = %v, want Music B", got.FollowingIn)
	}

	follow(false, b.ID.String())
	var saved models.CollectionArtist
	api.DB.Where("mb_id = ?", "art-1").First(&saved)
	if saved.Monitored {
		t.Error("removing the last library left following on")
	}
}

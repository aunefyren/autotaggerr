package routers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// TestAlbumDetailNamesEveryCreditedArtist: a collaboration reached through one artist
// still names the others, in credit order, so the page can say whose album it also is
// — and an artist the collection does not hold is left out rather than shown as an ID.
func TestAlbumDetailNamesEveryCreditedArtist(t *testing.T) {
	r, api := setupAPI(t)
	api.Meta = &fakeMeta{}
	token := loginToken(t, r)

	for _, a := range []models.CollectionArtist{
		{MBID: "art-x", Name: "X", ManagedBy: models.ManagedByLidarr},
		{MBID: "art-y", Name: "Y", ManagedBy: models.ManagedByLidarr},
	} {
		if err := api.DB.Create(&a).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := api.DB.Create(&models.CollectionReleaseGroup{MBID: "rg-shared", ArtistMBID: "art-x", Title: "Together"}).Error; err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"art-x", "art-y", "art-unknown"} {
		if err := api.DB.Create(&models.CollectionReleaseGroupArtist{ReleaseGroupMBID: "rg-shared", ArtistMBID: id, Position: i}).Error; err != nil {
			t.Fatal(err)
		}
	}

	w := do(r, http.MethodGet, "/api/v1/artists/art-y/release-groups/rg-shared", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Credits []creditView `json:"credits"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Credits) != 2 || got.Credits[0].Name != "X" || got.Credits[1].Name != "Y" {
		t.Errorf("credits = %+v, want X then Y", got.Credits)
	}
}

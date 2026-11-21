package routers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/quality"
	"github.com/google/uuid"
)

func lossyAndLossless() quality.Tiers {
	return quality.Tiers{
		{Qualities: []quality.ID{quality.MP3320}, Allowed: true},
		{Qualities: []quality.ID{quality.FLAC, quality.FLAC24}, Allowed: true},
	}
}

// TestQualityProfileCRUD: a profile is validated by the engine, lists who uses it, and
// deleting it clears it from libraries and artists rather than being refused.
func TestQualityProfileCRUD(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	info := decodeJSON[struct {
		Qualities []quality.Definition `json:"qualities"`
	}](t, r, http.MethodGet, "/api/v1/quality", token, nil)
	if len(info.Qualities) != len(quality.Ladder()) {
		t.Fatalf("ladder = %d qualities", len(info.Qualities))
	}

	if w := do(r, http.MethodPost, "/api/v1/quality-profiles", token, map[string]any{"name": "Nothing"}); w.Code != http.StatusBadRequest {
		t.Errorf("a profile allowing nothing = %d, want 400", w.Code)
	}
	if w := do(r, http.MethodPost, "/api/v1/quality-profiles", token, map[string]any{
		"name": "Bad cutoff", "tiers": lossyAndLossless(), "cutoff_tier": 5,
	}); w.Code != http.StatusBadRequest {
		t.Errorf("a cutoff past the tiers = %d, want 400", w.Code)
	}
	w := do(r, http.MethodPost, "/api/v1/quality-profiles", token, map[string]any{
		"name": "Lossless", "tiers": lossyAndLossless(), "cutoff_tier": 1, "upgrade_allowed": true,
	})
	var created models.QualityProfile
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &created) != nil {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	if created.ID == uuid.Nil || len(created.Tiers) != 2 || created.CutoffTier != 1 {
		t.Fatalf("created = %+v", created)
	}

	music := models.Library{Name: "Music", Path: "/music", Enabled: true}
	artist := models.CollectionArtist{MBID: "art-1", Name: "Artist", ManagedBy: models.ManagedByAutotaggerr}
	for _, row := range []any{&music, &artist} {
		if err := api.DB.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if w := do(r, http.MethodPut, "/api/v1/libraries/"+music.ID.String(), token, map[string]any{"quality_profile_id": created.ID}); w.Code != http.StatusOK {
		t.Fatalf("set library profile = %d %s", w.Code, w.Body.String())
	}
	if w := do(r, http.MethodPut, "/api/v1/libraries/"+music.ID.String(), token, map[string]any{"quality_profile_id": uuid.New()}); w.Code != http.StatusBadRequest {
		t.Errorf("an unknown profile = %d, want 400", w.Code)
	}
	if w := do(r, http.MethodPut, "/api/v1/artists/art-1/quality-profile", token, map[string]any{"quality_profile_id": created.ID}); w.Code != http.StatusOK {
		t.Fatalf("set artist override = %d %s", w.Code, w.Body.String())
	}
	if w := do(r, http.MethodPut, "/api/v1/artists/art-1/quality-profile", token, map[string]any{}); w.Code != http.StatusBadRequest {
		t.Errorf("an override with no field = %d, want 400", w.Code)
	}

	list := decodeJSON[[]qualityProfileView](t, r, http.MethodGet, "/api/v1/quality-profiles", token, nil)
	if len(list) != 1 || len(list[0].Libraries) != 1 || list[0].Libraries[0] != "Music" || list[0].Artists != 1 {
		t.Fatalf("list = %+v", list)
	}

	if w := do(r, http.MethodPut, "/api/v1/quality-profiles/"+created.ID.String(), token, map[string]any{"cutoff_tier": 0}); w.Code != http.StatusOK {
		t.Fatalf("update = %d %s", w.Code, w.Body.String())
	}
	if w := do(r, http.MethodPut, "/api/v1/quality-profiles/"+created.ID.String(), token, map[string]any{"name": " "}); w.Code != http.StatusBadRequest {
		t.Errorf("blank name = %d, want 400", w.Code)
	}

	if w := do(r, http.MethodDelete, "/api/v1/quality-profiles/"+created.ID.String(), token, nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", w.Code)
	}
	var lib models.Library
	api.DB.First(&lib, "id = ?", music.ID)
	var ar models.CollectionArtist
	api.DB.First(&ar, "mb_id = ?", "art-1")
	if lib.QualityProfileID != nil || ar.QualityProfileID != nil {
		t.Errorf("deleting left references: library %v, artist %v", lib.QualityProfileID, ar.QualityProfileID)
	}
	if w := do(r, http.MethodDelete, "/api/v1/quality-profiles/"+created.ID.String(), token, nil); w.Code != http.StatusNotFound {
		t.Errorf("second delete = %d, want 404", w.Code)
	}
}

// TestLidarrLibraryRefusesQualityProfile: Lidarr decides quality in its own library.
func TestLidarrLibraryRefusesQualityProfile(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	profile := models.QualityProfile{Name: "Lossless", Tiers: lossyAndLossless(), CutoffTier: 1}
	lidarr := models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true}
	for _, row := range []any{&profile, &lidarr} {
		if err := api.DB.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	lib := models.Library{Name: "Music", Path: "/music", Enabled: true, ManagerID: &lidarr.ID}
	if err := api.DB.Create(&lib).Error; err != nil {
		t.Fatal(err)
	}
	if w := do(r, http.MethodPut, "/api/v1/libraries/"+lib.ID.String(), token, map[string]any{"quality_profile_id": profile.ID}); w.Code != http.StatusBadRequest {
		t.Errorf("profile on a Lidarr library = %d, want 400", w.Code)
	}
	// Switching the manager off in the same edit is allowed.
	if w := do(r, http.MethodPut, "/api/v1/libraries/"+lib.ID.String(), token, map[string]any{
		"quality_profile_id": profile.ID, "manager_id": nil,
	}); w.Code != http.StatusOK {
		t.Errorf("profile with the manager cleared = %d %s", w.Code, w.Body.String())
	}
}

// TestUpgradeWanted: a wanted album held below its library's cutoff wants an upgrade
// and counts on the list; one at the cutoff, one not wanted, and one Lidarr governs
// do not; and switching the artist's override changes it with no rebuild.
func TestUpgradeWanted(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	lossless := models.QualityProfile{Name: "Lossless", Tiers: lossyAndLossless(), CutoffTier: 1, UpgradeAllowed: true}
	anything := models.QualityProfile{Name: "Any", Tiers: lossyAndLossless(), CutoffTier: 0, UpgradeAllowed: true}
	lidarr := models.Manager{Name: "Lidarr", Type: models.ManagerTypeLidarr, Enabled: true}
	for _, row := range []any{&lossless, &anything, &lidarr} {
		if err := api.DB.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	music := models.Library{Name: "Music", Path: "/music", Enabled: true, QualityProfileID: &lossless.ID}
	lidarrLib := models.Library{Name: "Lidarr", Path: "/lidarr", Enabled: true, ManagerID: &lidarr.ID, QualityProfileID: &lossless.ID}
	for _, row := range []any{&music, &lidarrLib} {
		if err := api.DB.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}

	owned := func(mbid, title string) *models.CollectionReleaseGroup {
		return &models.CollectionReleaseGroup{MBID: mbid, ArtistMBID: "art-1", Title: title, PrimaryType: "Album",
			Owned: true, OwnedTracks: 2, TotalTracks: 2}
	}
	rows := []any{
		&models.CollectionArtist{MBID: "art-1", Name: "Native", ManagedBy: models.ManagedByAutotaggerr},
		owned("rg-mp3", "MP3 album"), owned("rg-flac", "FLAC album"), owned("rg-unwanted", "Unwanted MP3"),
		owned("rg-both", "Two editions"),
		&models.CollectionArtist{MBID: "art-2", Name: "Lidarr artist", ManagedBy: models.ManagedByLidarr},
		&models.CollectionReleaseGroup{MBID: "rg-lidarr", ArtistMBID: "art-2", Title: "Lidarr MP3", PrimaryType: "Album",
			Owned: true, OwnedTracks: 1, TotalTracks: 1, InCatalog: true, CatalogMonitored: true},
	}
	for _, rg := range []string{"rg-mp3", "rg-flac", "rg-both"} {
		rows = append(rows, &models.CollectionDesire{ArtistMBID: "art-1", ReleaseGroupMBID: rg, Source: models.DesireSourceManual})
	}
	for _, row := range rows {
		if err := api.DB.Create(row).Error; err != nil {
			t.Fatalf("seed %T: %v", row, err)
		}
	}

	file := 0
	hold := func(artist, rg, release string, library uuid.UUID, qualities ...quality.ID) {
		t.Helper()
		if err := api.DB.Create(&models.CollectionReleaseCopy{ReleaseMBID: release, LibraryID: library,
			ReleaseGroupMBID: rg, ArtistMBID: artist, OwnedTracks: len(qualities), Files: len(qualities)}).Error; err != nil {
			t.Fatal(err)
		}
		for _, q := range qualities {
			file++
			if err := api.DB.Create(&models.LibraryItem{LibraryID: library, Path: "/f/" + string(rune('a'+file)),
				MBReleaseID: release, Quality: string(q)}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	hold("art-1", "rg-mp3", "rel-mp3", music.ID, quality.MP3320, quality.FLAC)
	hold("art-1", "rg-flac", "rel-flac", music.ID, quality.FLAC, quality.FLAC24)
	hold("art-1", "rg-unwanted", "rel-unwanted", music.ID, quality.MP3320)
	// One edition FLAC, another MP3: the album is good enough.
	hold("art-1", "rg-both", "rel-both-cd", music.ID, quality.FLAC, quality.FLAC)
	hold("art-1", "rg-both", "rel-both-web", music.ID, quality.MP3320)
	hold("art-2", "rg-lidarr", "rel-lidarr", lidarrLib.ID, quality.MP3320)

	type rgRow struct {
		MBID          string         `json:"mb_id"`
		UpgradeWanted bool           `json:"upgrade_wanted"`
		Raw           map[string]any `json:"quality"`
	}
	detail := func(mbid, query string) map[string]rgRow {
		t.Helper()
		body := decodeJSON[struct {
			ReleaseGroups []rgRow `json:"release_groups"`
		}](t, r, http.MethodGet, "/api/v1/artists/"+mbid+query, token, nil)
		out := map[string]rgRow{}
		for _, g := range body.ReleaseGroups {
			out[g.MBID] = g
		}
		return out
	}

	for _, query := range []string{"", "?library=" + music.ID.String()} {
		got := detail("art-1", query)
		if !got["rg-mp3"].UpgradeWanted || got["rg-mp3"].Raw["lowest"] != string(quality.MP3320) || got["rg-mp3"].Raw["cutoff_name"] != "FLAC / FLAC 24bit" {
			t.Errorf("%q: the MP3 album = %+v", query, got["rg-mp3"])
		}
		if got["rg-flac"].UpgradeWanted || got["rg-flac"].Raw["cutoff_met"] != true {
			t.Errorf("%q: the FLAC album = %+v", query, got["rg-flac"])
		}
		if got["rg-unwanted"].UpgradeWanted || got["rg-unwanted"].Raw == nil {
			t.Errorf("%q: an unwanted album is judged but never wants an upgrade: %+v", query, got["rg-unwanted"])
		}
		if got["rg-both"].UpgradeWanted || got["rg-both"].Raw["lowest"] != string(quality.FLAC) {
			t.Errorf("%q: the best edition decides: %+v", query, got["rg-both"])
		}
	}
	if got := detail("art-2", ""); got["rg-lidarr"].UpgradeWanted || got["rg-lidarr"].Raw != nil {
		t.Errorf("Lidarr decides quality in its library: %+v", got["rg-lidarr"])
	}

	type listRow struct {
		MBID    string `json:"mb_id"`
		Upgrade int    `json:"upgrade_count"`
	}
	count := func(query string) map[string]int {
		t.Helper()
		out := map[string]int{}
		for _, row := range decodeJSON[[]listRow](t, r, http.MethodGet, "/api/v1/artists"+query, token, nil) {
			out[row.MBID] = row.Upgrade
		}
		return out
	}
	if got := count(""); got["art-1"] != 1 || got["art-2"] != 0 {
		t.Errorf("list = %v, want one upgrade for the native artist", got)
	}
	if got := count("?library=" + music.ID.String()); got["art-1"] != 1 {
		t.Errorf("list in Music = %v", got)
	}

	// The override wins over the library, read straight away: no rebuild.
	if w := do(r, http.MethodPut, "/api/v1/artists/art-1/quality-profile", token, map[string]any{"quality_profile_id": anything.ID}); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if got := detail("art-1", ""); got["rg-mp3"].UpgradeWanted || got["rg-mp3"].Raw["profile"] != "Any" {
		t.Errorf("under the override = %+v", got["rg-mp3"])
	}
	if got := count(""); got["art-1"] != 0 {
		t.Errorf("the cached list did not notice the override: %v", got)
	}
}

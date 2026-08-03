package routers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/aunefyren/autotaggerr/metadata"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
)

// errBoom is a stand-in upstream failure for the MB-bound handler tests.
var errBoom = errors.New("boom")

// fakeMeta is an injectable metadata.MetadataSource whose every method delegates to
// an optional func field, so a test wires only the calls it exercises and leaves the
// rest returning zero values. This is the seam that makes the MB-bound handlers
// coverable without a network round-trip — set api.Meta = &fakeMeta{...}.
type fakeMeta struct {
	getRelease    func(mbID string) (models.MusicBrainzReleaseResponse, error)
	getArtist     func(artistID string) (models.MusicBrainzArtistLookup, error)
	getArtistRGs  func(artistID string) ([]models.MusicBrainzArtistReleaseGroup, bool, error)
	getRGReleases func(rgID string) ([]models.MusicBrainzReleaseSearchResult, error)
	searchRel     func(q metadata.ReleaseSearchQuery) (metadata.ReleaseSearchPage, error)
	searchArtists func(q string) ([]models.MusicBrainzArtistSearchResult, error)
}

func (f *fakeMeta) GetRelease(mbID string) (models.MusicBrainzReleaseResponse, error) {
	if f.getRelease != nil {
		return f.getRelease(mbID)
	}
	return models.MusicBrainzReleaseResponse{}, nil
}

func (f *fakeMeta) GetArtist(artistID string) (models.MusicBrainzArtistLookup, error) {
	if f.getArtist != nil {
		return f.getArtist(artistID)
	}
	return models.MusicBrainzArtistLookup{}, nil
}

func (f *fakeMeta) GetArtistReleaseGroups(artistID string) ([]models.MusicBrainzArtistReleaseGroup, bool, error) {
	if f.getArtistRGs != nil {
		return f.getArtistRGs(artistID)
	}
	return nil, false, nil
}

func (f *fakeMeta) GetReleaseGroupReleases(rgID string) ([]models.MusicBrainzReleaseSearchResult, error) {
	if f.getRGReleases != nil {
		return f.getRGReleases(rgID)
	}
	return nil, nil
}

func (f *fakeMeta) SearchReleases(q metadata.ReleaseSearchQuery) (metadata.ReleaseSearchPage, error) {
	if f.searchRel != nil {
		return f.searchRel(q)
	}
	return metadata.ReleaseSearchPage{}, nil
}

func (f *fakeMeta) SearchArtists(q string) ([]models.MusicBrainzArtistSearchResult, error) {
	if f.searchArtists != nil {
		return f.searchArtists(q)
	}
	return nil, nil
}

const testMBID = "1b022e01-4da6-387b-8658-8678046e4cef"

func searchURL(params map[string]string) string {
	v := url.Values{}
	for key, value := range params {
		v.Set(key, value)
	}
	return "/api/v1/search/releases?" + v.Encode()
}

// TestSearchReleasesFieldedQuery: a fielded search reaches the source with the form
// fields mapped onto the query, and its page is returned verbatim. Before the port
// this handler could only be tested on the empty-query path, because the real call
// hit musicbrainz.org.
func TestSearchReleasesFieldedQuery(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	var got metadata.ReleaseSearchQuery
	api.Meta = &fakeMeta{
		searchRel: func(q metadata.ReleaseSearchQuery) (metadata.ReleaseSearchPage, error) {
			got = q
			return metadata.ReleaseSearchPage{
				Count:    1,
				Releases: []models.MusicBrainzReleaseSearchResult{{ID: "rel-1", Title: "Saturday Night Fever"}},
			}, nil
		},
	}

	w := do(r, "GET", searchURL(map[string]string{"artist": "Bee Gees", "release": "Saturday Night Fever", "tracks": "17"}), token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if got.Artist != "Bee Gees" || got.Release != "Saturday Night Fever" || got.Tracks != 17 {
		t.Errorf("query not mapped from the form: %+v", got)
	}

	var page metadata.ReleaseSearchPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.Count != 1 || len(page.Releases) != 1 || page.Releases[0].ID != "rel-1" {
		t.Errorf("page = %+v", page)
	}
}

// TestSearchReleasesEmptyQuery: a request with no usable fields must 400 before the
// source is touched — the handler must not burn a request on a guaranteed-empty search.
func TestSearchReleasesEmptyQuery(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	called := false
	api.Meta = &fakeMeta{searchRel: func(metadata.ReleaseSearchQuery) (metadata.ReleaseSearchPage, error) {
		called = true
		return metadata.ReleaseSearchPage{}, nil
	}}

	w := do(r, "GET", "/api/v1/search/releases", token, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if called {
		t.Error("empty query reached the metadata source")
	}
}

// TestSearchReleasesUpstreamError: when the source fails, the handler answers 502
// rather than leaking the raw error.
func TestSearchReleasesUpstreamError(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	api.Meta = &fakeMeta{searchRel: func(metadata.ReleaseSearchQuery) (metadata.ReleaseSearchPage, error) {
		return metadata.ReleaseSearchPage{}, errBoom
	}}

	w := do(r, "GET", searchURL(map[string]string{"artist": "Bee Gees"}), token, nil)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", w.Code, w.Body.String())
	}
}

// TestSearchReleasesPastedReleaseURL: pasting a release URL resolves that one release
// directly instead of searching, projecting it onto the same page shape the list uses.
func TestSearchReleasesPastedReleaseURL(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	api.Meta = &fakeMeta{getRelease: func(mbID string) (models.MusicBrainzReleaseResponse, error) {
		if mbID != testMBID {
			t.Errorf("GetRelease called with %q, want %q", mbID, testMBID)
		}
		return models.MusicBrainzReleaseResponse{ID: testMBID, Title: "Spiderland"}, nil
	}}

	w := do(r, "GET", searchURL(map[string]string{"q": "https://musicbrainz.org/release/" + testMBID}), token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var page metadata.ReleaseSearchPage
	_ = json.Unmarshal(w.Body.Bytes(), &page)
	if page.Count != 1 || len(page.Releases) != 1 || page.Releases[0].ID != testMBID {
		t.Errorf("page = %+v", page)
	}
}

// TestSearchReleasesPastedReleaseGroupURL: a release-group URL expands to that group's
// editions.
func TestSearchReleasesPastedReleaseGroupURL(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	api.Meta = &fakeMeta{getRGReleases: func(rgID string) ([]models.MusicBrainzReleaseSearchResult, error) {
		if rgID != testMBID {
			t.Errorf("GetReleaseGroupReleases called with %q, want %q", rgID, testMBID)
		}
		return []models.MusicBrainzReleaseSearchResult{{ID: "ed-1"}, {ID: "ed-2"}}, nil
	}}

	w := do(r, "GET", searchURL(map[string]string{"q": "https://musicbrainz.org/release-group/" + testMBID}), token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var page metadata.ReleaseSearchPage
	_ = json.Unmarshal(w.Body.Bytes(), &page)
	if page.Count != 2 || len(page.Releases) != 2 {
		t.Errorf("page = %+v", page)
	}
}

// TestSearchReleasesPastedArtistURL: an artist URL narrows the search to that artist
// (an artist is not something a file can attach to), so it re-runs the search with the
// arid set and the free text cleared.
func TestSearchReleasesPastedArtistURL(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	var got metadata.ReleaseSearchQuery
	api.Meta = &fakeMeta{searchRel: func(q metadata.ReleaseSearchQuery) (metadata.ReleaseSearchPage, error) {
		got = q
		return metadata.ReleaseSearchPage{Count: 0}, nil
	}}

	w := do(r, "GET", searchURL(map[string]string{"q": "https://musicbrainz.org/artist/" + testMBID}), token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if got.ArtistID != testMBID || got.Text != "" {
		t.Errorf("artist URL did not narrow the query: %+v", got)
	}
}

// TestSearchReleasesBareMBIDFallsBackToReleaseGroup: a bare ID that is not a release is
// retried as a release group, so pasting an ID without its URL still lands somewhere.
func TestSearchReleasesBareMBIDFallsBackToReleaseGroup(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	api.Meta = &fakeMeta{
		getRelease: func(string) (models.MusicBrainzReleaseResponse, error) {
			return models.MusicBrainzReleaseResponse{}, errBoom
		},
		getRGReleases: func(string) ([]models.MusicBrainzReleaseSearchResult, error) {
			return []models.MusicBrainzReleaseSearchResult{{ID: "ed-1"}}, nil
		},
	}

	w := do(r, "GET", searchURL(map[string]string{"q": testMBID}), token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var page metadata.ReleaseSearchPage
	_ = json.Unmarshal(w.Body.Bytes(), &page)
	if page.Count != 1 || len(page.Releases) != 1 {
		t.Errorf("page = %+v", page)
	}
}

// TestSearchReleasesPastedMBIDNotFound: a bare ID that is neither a release nor a
// release group is a 404, not a 502 — the ID is well-formed, it just does not exist.
func TestSearchReleasesPastedMBIDNotFound(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	api.Meta = &fakeMeta{
		getRelease: func(string) (models.MusicBrainzReleaseResponse, error) {
			return models.MusicBrainzReleaseResponse{}, errBoom
		},
		getRGReleases: func(string) ([]models.MusicBrainzReleaseSearchResult, error) { return nil, nil },
	}

	w := do(r, "GET", searchURL(map[string]string{"q": testMBID}), token, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body.String())
	}
}

// TestSearchArtists: the artist-search handler reaches the source and returns its
// hits. Previously coverable only on the blank-query 400 path.
func TestSearchArtists(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	var gotQuery string
	api.Meta = &fakeMeta{searchArtists: func(q string) ([]models.MusicBrainzArtistSearchResult, error) {
		gotQuery = q
		return []models.MusicBrainzArtistSearchResult{{ID: "art-1", Name: "Bee Gees"}}, nil
	}}

	w := do(r, "GET", "/api/v1/search/artists?q="+url.QueryEscape("Bee Gees"), token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if gotQuery != "Bee Gees" {
		t.Errorf("query = %q, want %q", gotQuery, "Bee Gees")
	}
	var results []models.MusicBrainzArtistSearchResult
	_ = json.Unmarshal(w.Body.Bytes(), &results)
	if len(results) != 1 || results[0].ID != "art-1" {
		t.Errorf("results = %+v", results)
	}
}

// TestSearchArtistsBlankAndError: a blank query never reaches the source (400), and an
// upstream failure is a 502.
func TestSearchArtistsBlankAndError(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	called := false
	api.Meta = &fakeMeta{searchArtists: func(string) ([]models.MusicBrainzArtistSearchResult, error) {
		called = true
		return nil, errBoom
	}}

	if w := do(r, "GET", "/api/v1/search/artists?q=%20%20", token, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("blank query status = %d, want 400", w.Code)
	}
	if called {
		t.Error("blank query reached the source")
	}

	if w := do(r, "GET", "/api/v1/search/artists?q=Bee", token, nil); w.Code != http.StatusBadGateway {
		t.Fatalf("upstream error status = %d, want 502", w.Code)
	}
}

// TestArtistInfo: the artist-info handler resolves the collection artist, fetches the
// MB entity through the source, and ranks genres above tags. This whole body was
// network-blocked before the port.
func TestArtistInfo(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	if err := api.DB.Create(&models.CollectionArtist{MBID: testMBID, Name: "Bee Gees"}).Error; err != nil {
		t.Fatalf("seed artist: %v", err)
	}

	api.Meta = &fakeMeta{getArtist: func(mbID string) (models.MusicBrainzArtistLookup, error) {
		if mbID != testMBID {
			t.Errorf("GetArtist called with %q, want %q", mbID, testMBID)
		}
		lookup := models.MusicBrainzArtistLookup{ID: testMBID, Name: "Bee Gees", Type: "Group", Country: "GB"}
		lookup.Genres = []models.MusicBrainzNamedCount{{Name: "disco", Count: 10}, {Name: "pop", Count: 3}}
		return lookup, nil
	}}

	w := do(r, "GET", "/api/v1/artists/"+testMBID+"/info", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var info struct {
		Type    string   `json:"type"`
		Country string   `json:"country"`
		Genres  []string `json:"genres"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Higher-voted genre ranks first.
	if len(info.Genres) != 2 || info.Genres[0] != "disco" {
		t.Errorf("genres = %+v", info.Genres)
	}
	if info.Type != "Group" || info.Country != "GB" {
		t.Errorf("info = %+v", info)
	}
}

// TestArtistInfoUnknownAndError: an artist not in the collection is a 404 before any MB
// call; a source failure for a known artist is a 502.
func TestArtistInfoUnknownAndError(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	called := false
	api.Meta = &fakeMeta{getArtist: func(string) (models.MusicBrainzArtistLookup, error) {
		called = true
		return models.MusicBrainzArtistLookup{}, errBoom
	}}

	if w := do(r, "GET", "/api/v1/artists/"+testMBID+"/info", token, nil); w.Code != http.StatusNotFound {
		t.Fatalf("unknown artist status = %d, want 404", w.Code)
	}
	if called {
		t.Error("unknown artist reached the source")
	}

	if err := api.DB.Create(&models.CollectionArtist{MBID: testMBID, Name: "Bee Gees"}).Error; err != nil {
		t.Fatalf("seed artist: %v", err)
	}
	if w := do(r, "GET", "/api/v1/artists/"+testMBID+"/info", token, nil); w.Code != http.StatusBadGateway {
		t.Fatalf("source error status = %d, want 502", w.Code)
	}
}

// TestReleaseTracksUpstreamError: the picker's tracklist endpoint answers 502 when the
// release cannot be fetched.
func TestReleaseTracksUpstreamError(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	api.Meta = &fakeMeta{getRelease: func(string) (models.MusicBrainzReleaseResponse, error) {
		return models.MusicBrainzReleaseResponse{}, errBoom
	}}

	if w := do(r, "GET", "/api/v1/releases/rel-x/tracks", token, nil); w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", w.Code, w.Body.String())
	}
}

// TestAttachReleaseFetchError: attach validates the chosen release against MusicBrainz,
// so a fetch failure is a 502 and the correlation is not written.
func TestAttachReleaseFetchError(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	item := seedAttachFixtures(t, api.DB)
	api.Meta = &fakeMeta{getRelease: func(string) (models.MusicBrainzReleaseResponse, error) {
		return models.MusicBrainzReleaseResponse{}, errBoom
	}}

	w := do(r, "POST", "/api/v1/library-items/"+item.ID.String()+"/attach", token, map[string]string{
		"mb_release_id": "rel-1", "mb_release_track_id": "t1",
	})
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", w.Code, w.Body.String())
	}
	var stored models.LibraryItem
	_ = api.DB.First(&stored, "id = ?", item.ID).Error
	if stored.MBReleaseID != "" || stored.Pinned {
		t.Errorf("failed attach still modified the item: %+v", stored)
	}
}

// TestBulkPreviewReleaseFetchError: the bulk-preview endpoint answers 502 when the
// release it maps against cannot be fetched.
func TestBulkPreviewReleaseFetchError(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)
	item := seedAttachFixtures(t, api.DB)
	api.Meta = &fakeMeta{getRelease: func(string) (models.MusicBrainzReleaseResponse, error) {
		return models.MusicBrainzReleaseResponse{}, errBoom
	}}

	w := do(r, "POST", "/api/v1/attach/preview", token, map[string]any{
		"mb_release_id": "rel-1", "item_ids": []string{item.ID.String()},
	})
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", w.Code, w.Body.String())
	}
}

// TestDetachUnknownItem: detaching a non-existent item is a 404.
func TestDetachUnknownItem(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)

	if w := do(r, "DELETE", "/api/v1/library-items/"+uuid.New().String()+"/attach", token, nil); w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body.String())
	}
}

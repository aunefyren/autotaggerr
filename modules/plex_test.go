package modules

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// newPlexServer starts a mock Plex server with a custom handler and returns a
// client pointed at it.
func newPlexServer(t *testing.T, h http.Handler) *PlexClient {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewPlexClient(srv.URL, "tok")
}

func resetPlexCache() {
	plexAlbumKeyCacheMu.Lock()
	plexAlbumKeyCache = map[string]models.PlexAlbumKeyCache{}
	plexAlbumKeyCacheMu.Unlock()
}

func TestNormalizeAlbumKey(t *testing.T) {
	tests := map[string]string{
		"/library/metadata/196905":          "/library/metadata/196905",
		"/library/metadata/196905/children": "/library/metadata/196905",
		"  /library/metadata/1/children  ":  "/library/metadata/1",
	}
	for in, want := range tests {
		if got := normalizeAlbumKey(in); got != want {
			t.Errorf("normalizeAlbumKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeArtistChildrenPath(t *testing.T) {
	ok := map[string]string{
		"/library/metadata/900":          "/library/metadata/900/children",
		"/library/metadata/900/children": "/library/metadata/900/children",
		"900":                            "/library/metadata/900/children",
	}
	for in, want := range ok {
		got, err := normalizeArtistChildrenPath(in)
		if err != nil || got != want {
			t.Errorf("normalizeArtistChildrenPath(%q) = (%q, %v), want (%q, nil)", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "   ", "not-a-key"} {
		if _, err := normalizeArtistChildrenPath(bad); err == nil {
			t.Errorf("normalizeArtistChildrenPath(%q) expected error", bad)
		}
	}
}

func TestResolveAlbumKeyInSectionAlbumMatch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/library/sections/5/all", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("type") == "9" {
			_, _ = w.Write([]byte(`<MediaContainer><Directory key="/library/metadata/777/children" title="The Blueprint" parentTitle="Jay-Z" type="album"/></MediaContainer>`))
			return
		}
		_, _ = w.Write([]byte(`<MediaContainer></MediaContainer>`))
	})
	client := newPlexServer(t, mux)

	key, err := client.ResolveAlbumKeyInSection("5", "Jay-Z", "The Blueprint", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "/library/metadata/777" {
		t.Errorf("album key = %q, want /library/metadata/777", key)
	}
}

// The album search returns nothing; resolution should fall back to the track
// search (type=10) and derive the album key from the track's ParentKey.
func TestResolveAlbumKeyInSectionTrackFallback(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/library/sections/5/all", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("type") == "10" {
			_, _ = w.Write([]byte(`<MediaContainer><Track title="99 Problems" parentTitle="The Black Album" grandparentTitle="Jay-Z" parentKey="/library/metadata/888"/></MediaContainer>`))
			return
		}
		_, _ = w.Write([]byte(`<MediaContainer></MediaContainer>`)) // type=9 empty
	})
	client := newPlexServer(t, mux)

	key, err := client.ResolveAlbumKeyInSection("5", "Jay-Z", "The Black Album", "99 Problems")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "/library/metadata/888" {
		t.Errorf("album key = %q, want /library/metadata/888", key)
	}
}

func TestResolveAlbumKeyInSectionNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/library/sections/5/all", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<MediaContainer></MediaContainer>`))
	})
	client := newPlexServer(t, mux)

	if _, err := client.ResolveAlbumKeyInSection("5", "Jay-Z", "Nope", "Nope"); err == nil {
		t.Error("expected error when album/track not found")
	}
}

func TestRefreshAlbum(t *testing.T) {
	var hit bool
	mux := http.NewServeMux()
	mux.HandleFunc("/library/metadata/196905/refresh", func(w http.ResponseWriter, r *http.Request) {
		hit = true
		if r.URL.Query().Get("force") != "1" {
			t.Errorf("refresh missing force=1")
		}
		w.WriteHeader(http.StatusOK)
	})
	client := newPlexServer(t, mux)

	if err := client.RefreshAlbum("/library/metadata/196905/children"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hit {
		t.Error("refresh endpoint was not called")
	}
}

// plexRefreshMux serves the endpoints PlexRefreshForFile needs on a cache miss.
func plexRefreshMux(t *testing.T, artistFound bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/library/sections", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<MediaContainer><Directory key="5" title="Music" type="artist"/></MediaContainer>`))
	})
	mux.HandleFunc("/library/sections/5/all", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("type") {
		case "8": // artist search
			if artistFound {
				_, _ = w.Write([]byte(`<MediaContainer><Directory key="/library/metadata/900" title="Jay-Z" type="artist"/></MediaContainer>`))
			} else {
				_, _ = w.Write([]byte(`<MediaContainer></MediaContainer>`))
			}
		case "9": // album search
			_, _ = w.Write([]byte(`<MediaContainer><Directory key="/library/metadata/777" title="The Blueprint" parentTitle="Jay-Z" type="album"/></MediaContainer>`))
		default:
			_, _ = w.Write([]byte(`<MediaContainer></MediaContainer>`))
		}
	})
	return mux
}

func TestPlexRefreshForFileMissSuccess(t *testing.T) {
	resetPlexCache()
	client := newPlexServer(t, plexRefreshMux(t, true))
	set := NewAlbumRefreshSet(nil)

	err := PlexRefreshForFile(false, 1, set, *client, "The Blueprint", "Jay-Z", "Izzo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := set.Snapshot()["The Blueprint"]; got != "/library/metadata/777" {
		t.Errorf("refresh set album key = %q, want /library/metadata/777", got)
	}
	// the resolved key should now be cached
	plexAlbumKeyCacheMu.RLock()
	_, cached := plexAlbumKeyCache["The Blueprint"]
	plexAlbumKeyCacheMu.RUnlock()
	if !cached {
		t.Error("album key was not cached after resolution")
	}
}

func TestPlexRefreshForFileCacheHit(t *testing.T) {
	resetPlexCache()
	plexAlbumKeyCacheMu.Lock()
	plexAlbumKeyCache["The Blueprint"] = models.PlexAlbumKeyCache{AlbumKey: "/library/metadata/777", Timestamp: time.Now()}
	plexAlbumKeyCacheMu.Unlock()

	// Any HTTP call would be a bug on the cache-hit path.
	client := newPlexServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected HTTP call on cache hit: %s", r.URL)
	}))
	set := NewAlbumRefreshSet(nil)

	if err := PlexRefreshForFile(false, 1, set, *client, "The Blueprint", "Jay-Z", "Izzo"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := set.Snapshot()["The Blueprint"]; got != "/library/metadata/777" {
		t.Errorf("refresh set album key = %q, want cached value", got)
	}
}

// A missing Plex artist must return a wrapped, non-fatal error and must NOT add
// the album to the refresh set.
func TestPlexRefreshForFileMissingArtist(t *testing.T) {
	resetPlexCache()
	client := newPlexServer(t, plexRefreshMux(t, false))
	set := NewAlbumRefreshSet(nil)

	err := PlexRefreshForFile(false, 1, set, *client, "The Blueprint", "Jay-Z", "Izzo")
	if err == nil {
		t.Fatal("expected error for missing artist")
	}
	if _, ok := set.Snapshot()["The Blueprint"]; ok {
		t.Error("album should not be queued for refresh when artist lookup fails")
	}
}

// newPlexMock serves canned XML per path and asserts the Plex token is attached.
func newPlexMock(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for path, xmlBody := range routes {
		body := xmlBody
		p := path
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("X-Plex-Token") == "" {
				t.Errorf("request to %s missing X-Plex-Token", p)
			}
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(body))
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestPlexFindMusicSectionID(t *testing.T) {
	srv := newPlexMock(t, map[string]string{
		"/library/sections": `<MediaContainer>
			<Directory key="3" title="Movies" type="movie"/>
			<Directory key="5" title="Music" type="artist"/>
		</MediaContainer>`,
	})
	client := NewPlexClient(srv.URL, "tok")

	id, err := client.FindMusicSectionID()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "5" {
		t.Errorf("section id = %q, want 5", id)
	}
}

func TestPlexFindMusicSectionIDNone(t *testing.T) {
	srv := newPlexMock(t, map[string]string{
		"/library/sections": `<MediaContainer><Directory key="3" title="Movies" type="movie"/></MediaContainer>`,
	})
	client := NewPlexClient(srv.URL, "tok")

	if _, err := client.FindMusicSectionID(); err == nil {
		t.Error("expected error when no artist-type section exists")
	}
}

func TestPlexFindArtistKey(t *testing.T) {
	srv := newPlexMock(t, map[string]string{
		"/library/sections/5/all": `<MediaContainer>
			<Directory key="/library/metadata/900" title="Pink Floyd" type="artist"/>
		</MediaContainer>`,
	})
	client := NewPlexClient(srv.URL, "tok")

	// Canon-based match should tolerate case differences.
	key, err := client.FindArtistKey("5", "pink floyd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "/library/metadata/900" {
		t.Errorf("artist key = %q, want /library/metadata/900", key)
	}
}

// TestPlexFindArtistKeyLooseDash covers the real-world case where Plex stores the
// artist with a non-ASCII dash (here a non-breaking hyphen, U+2011) while our tag
// uses a plain "-". Strict matching missed this and spammed "artist not found".
func TestPlexFindArtistKeyLooseDash(t *testing.T) {
	srv := newPlexMock(t, map[string]string{
		// "Jay‑Z" — non-breaking hyphen, not an ASCII '-'
		"/library/sections/5/all": "<MediaContainer>" +
			`<Directory key="/library/metadata/42" title="Jay‑Z" type="artist"/>` +
			"</MediaContainer>",
	})
	client := NewPlexClient(srv.URL, "tok")

	key, err := client.FindArtistKey("5", "Jay-Z") // ASCII hyphen
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "/library/metadata/42" {
		t.Errorf("artist key = %q, want /library/metadata/42", key)
	}
}

func TestPlexFindArtistKeyNotFound(t *testing.T) {
	srv := newPlexMock(t, map[string]string{
		"/library/sections/5/all": `<MediaContainer>
			<Directory key="/library/metadata/900" title="Radiohead" type="artist"/>
		</MediaContainer>`,
	})
	client := NewPlexClient(srv.URL, "tok")

	if _, err := client.FindArtistKey("5", "Pink Floyd"); err == nil {
		t.Error("expected error for artist not found")
	}
}

func TestPlexHealthCheck(t *testing.T) {
	srv := newPlexMock(t, map[string]string{
		"/identity": `<MediaContainer machineIdentifier="abc" version="1.40"/>`,
	})
	client := NewPlexClient(srv.URL, "tok")

	ok, err := client.HealthCheck()
	if err != nil || !ok {
		t.Errorf("HealthCheck = (%v, %v), want (true, nil)", ok, err)
	}
}

// TestRefreshAlbumFallsBackToPUT: Plex installs disagree about whether the refresh
// endpoint answers a GET, so a 404/405 there is not a failure — it is the signal to
// try the other verb. Getting this wrong means refreshes silently never happen on
// some servers.
func TestRefreshAlbumFallsBackToPUT(t *testing.T) {
	var methods []string
	mux := http.NewServeMux()
	mux.HandleFunc("/library/metadata/196905/refresh", func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	client := newPlexServer(t, mux)

	if err := client.RefreshAlbum("/library/metadata/196905"); err != nil {
		t.Fatalf("RefreshAlbum: %v", err)
	}
	if len(methods) != 2 || methods[0] != http.MethodGet || methods[1] != http.MethodPut {
		t.Errorf("methods = %v, want a GET then a PUT", methods)
	}
}

func TestRefreshAlbumReportsFailures(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		wantIn  string
	}{
		{
			name: "GET rejected outright",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				// Not a 404/405, so there is nothing to retry with — a bad token
				// looks like this, and it must be reported rather than swallowed.
				http.Error(w, "unauthorized", http.StatusUnauthorized)
			},
			wantIn: "GET",
		},
		{
			name: "both verbs rejected",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				http.Error(w, "still no", http.StatusInternalServerError)
			},
			wantIn: "PUT",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/library/metadata/196905/refresh", tc.handler)
			client := newPlexServer(t, mux)

			err := client.RefreshAlbum("/library/metadata/196905")
			if err == nil {
				t.Fatal("err = nil, want a failure")
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("err = %q, want it to name the %s attempt", err.Error(), tc.wantIn)
			}
		})
	}
}

// TestRefreshAlbumUnreachableServer: Plex being down is an error the scan reports,
// never a silent success that leaves the library stale.
func TestRefreshAlbumUnreachableServer(t *testing.T) {
	client := NewPlexClient("http://127.0.0.1:1", "tok")
	if err := client.RefreshAlbum("/library/metadata/1"); err == nil {
		t.Error("an unreachable Plex was reported as a successful refresh")
	}
}

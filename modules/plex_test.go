package modules

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

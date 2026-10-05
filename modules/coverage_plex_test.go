package modules

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Plex failure modes: every lookup failure has to surface as an error naming the
// step, never as a silently empty key, because PlexRefreshForFile turns an empty
// key into a refresh of nothing.

func deadPlexClient(t *testing.T) *PlexClient {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	c := NewPlexClient(srv.URL, "tok")
	srv.Close()
	return c
}

func TestPlexGetReportsTransportAndStatusFailures(t *testing.T) {
	dead := deadPlexClient(t)
	if _, err := dead.FindMusicSectionID(); err == nil {
		t.Error("FindMusicSectionID against a dead server succeeded")
	}
	if _, err := dead.FindArtistKey("5", "Jay-Z"); err == nil {
		t.Error("FindArtistKey against a dead server succeeded")
	}

	client := newPlexServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized token", http.StatusUnauthorized)
	}))
	_, err := client.FindMusicSectionID()
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "unauthorized token") {
		t.Errorf("err = %v, want the status and Plex's body", err)
	}
}

func TestResolveAlbumKeyInSectionQueryFailures(t *testing.T) {
	// Album search fails outright.
	albumDown := newPlexServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	if _, err := albumDown.ResolveAlbumKeyInSection("5", "Jay-Z", "The Blueprint", ""); err == nil {
		t.Error("a failed album search resolved a key")
	}

	// Album search answers nothing; the track fallback then fails.
	trackDown := newPlexServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("type") == "9" {
			_, _ = w.Write([]byte(`<MediaContainer></MediaContainer>`))
			return
		}
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	if _, err := trackDown.ResolveAlbumKeyInSection("5", "Jay-Z", "The Blueprint", "Izzo"); err == nil {
		t.Error("a failed track search resolved a key")
	}
}

// A track hit with only a numeric parentRatingKey still yields a refreshable path.
func TestResolveAlbumKeyInSectionParentRatingKeyFallback(t *testing.T) {
	client := newPlexServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("type") == "10" {
			_, _ = w.Write([]byte(`<MediaContainer><Track title="Izzo" parentTitle="The Blueprint" grandparentTitle="Jay-Z" parentRatingKey="4242"/></MediaContainer>`))
			return
		}
		_, _ = w.Write([]byte(`<MediaContainer></MediaContainer>`))
	}))
	got, err := client.ResolveAlbumKeyInSection("5", "Jay-Z", "The Blueprint", "Izzo")
	if err != nil || got != "/library/metadata/4242" {
		t.Errorf("ResolveAlbumKeyInSection = (%q, %v), want /library/metadata/4242", got, err)
	}
}

func TestRefreshAlbumTransportFailures(t *testing.T) {
	if err := deadPlexClient(t).RefreshAlbum("/library/metadata/1"); err == nil {
		t.Error("RefreshAlbum against a dead server succeeded")
	}

	// GET says 405, and the PUT fallback then loses its connection.
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("no hijacker")
		}
		conn, _, _ := hj.Hijack()
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)
	if err := NewPlexClient(srv.URL, "tok").RefreshAlbum("/library/metadata/1"); err == nil {
		t.Error("a dropped PUT fallback reported success")
	}
}

func TestPlexHealthCheckFailureModes(t *testing.T) {
	if ok, err := deadPlexClient(t).HealthCheck(); ok || err == nil {
		t.Errorf("dead server HealthCheck = (%v, %v), want (false, error)", ok, err)
	}

	unauthorized := newPlexServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	if ok, err := unauthorized.HealthCheck(); ok || err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("401 HealthCheck = (%v, %v), want (false, error naming the status)", ok, err)
	}

	// 200 with an unparseable body is reachable-and-authorised, so still healthy.
	garbled := newPlexServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not xml <"))
	}))
	if ok, err := garbled.HealthCheck(); !ok || err != nil {
		t.Errorf("garbled 200 HealthCheck = (%v, %v), want (true, nil)", ok, err)
	}

	bad := NewPlexClient("http://bad host", "tok")
	if ok, err := bad.HealthCheck(); ok || err == nil {
		t.Errorf("unparseable URL HealthCheck = (%v, %v), want (false, error)", ok, err)
	}
}

func TestPlexRefreshForFileStepFailures(t *testing.T) {
	resetPlexCache()
	t.Cleanup(resetPlexCache)

	// No music section.
	noSection := newPlexServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<MediaContainer></MediaContainer>`))
	}))
	err := PlexRefreshForFile(false, 1, NewAlbumRefreshSet(nil), *noSection, "The Blueprint", "Jay-Z", "Izzo")
	if err == nil || !strings.Contains(err.Error(), "find Plex music section") {
		t.Errorf("err = %v, want the section step named", err)
	}

	// Artist found but the album is in neither search.
	mux := http.NewServeMux()
	mux.HandleFunc("/library/sections", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<MediaContainer><Directory key="5" type="artist"/></MediaContainer>`))
	})
	mux.HandleFunc("/library/sections/5/all", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("type") == "8" {
			_, _ = w.Write([]byte(`<MediaContainer><Directory key="/library/metadata/900" title="Jay-Z" type="artist"/></MediaContainer>`))
			return
		}
		_, _ = w.Write([]byte(`<MediaContainer></MediaContainer>`))
	})
	noAlbum := newPlexServer(t, mux)
	set := NewAlbumRefreshSet(nil)
	err = PlexRefreshForFile(false, 1, set, *noAlbum, "The Blueprint", "Jay-Z", "Izzo")
	if err == nil || !strings.Contains(err.Error(), "resolve Plex album key") {
		t.Errorf("err = %v, want the album step named", err)
	}
	if len(set.Snapshot()) != 0 {
		t.Error("an unresolved album was queued for refresh")
	}
}

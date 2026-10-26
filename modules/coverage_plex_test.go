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
	if _, err := dead.MusicSections(); err == nil {
		t.Error("MusicSections against a dead server succeeded")
	}

	client := newPlexServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized token", http.StatusUnauthorized)
	}))
	_, err := client.MusicSections()
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "unauthorized token") {
		t.Errorf("err = %v, want the status and Plex's body", err)
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

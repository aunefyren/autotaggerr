package modules

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

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

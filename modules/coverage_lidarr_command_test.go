package modules

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The one Lidarr write. Every claim the comments in lidarr_command.go make — a 201
// is an accepted command, a login portal answering 200 is not, a timeout is
// "unknown" rather than failure — is checked here against a stub Lidarr.

// fastLidarrCommandPolling shrinks the poll interval (and optionally the timeout) so
// WaitForCommand tests run in milliseconds instead of the production seconds.
func fastLidarrCommandPolling(t *testing.T, timeout time.Duration) {
	t.Helper()
	origPoll, origTimeout := lidarrCommandPollInterval, lidarrCommandTimeout
	lidarrCommandPollInterval = time.Millisecond
	if timeout > 0 {
		lidarrCommandTimeout = timeout
	}
	t.Cleanup(func() {
		lidarrCommandPollInterval, lidarrCommandTimeout = origPoll, origTimeout
	})
}

func newTestLidarrClient(t *testing.T, h http.Handler, cookie string) *LidarrClient {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	var c *string
	if cookie != "" {
		c = &cookie
	}
	return NewLidarrClient(srv.URL+"/", "test-key", c)
}

func TestRefreshArtistSendsScopedCommand(t *testing.T) {
	var (
		gotBody   map[string]any
		gotHeader http.Header
		gotMethod string
	)
	cli := newTestLidarrClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotHeader = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":42,"name":"RefreshArtist","status":"queued"}`))
	}), "session=abc")

	id, err := cli.RefreshArtist(7)
	if err != nil {
		t.Fatalf("RefreshArtist: %v", err)
	}
	if id != 42 {
		t.Errorf("command id = %d, want 42", id)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	// Scoped to one artist: the bodyless form refreshes the whole library.
	if gotBody["name"] != "RefreshArtist" || gotBody["artistId"] != float64(7) {
		t.Errorf("body = %v, want a RefreshArtist for artist 7", gotBody)
	}
	if gotHeader.Get("X-Api-Key") != "test-key" || gotHeader.Get("Cookie") != "session=abc" {
		t.Errorf("headers = %v, want the api key and configured cookie", gotHeader)
	}
	if gotHeader.Get("Content-Type") != "application/json" {
		t.Errorf("content-type = %q", gotHeader.Get("Content-Type"))
	}
}

func TestRefreshArtistWithoutCommandIDIsAnError(t *testing.T) {
	cli := newTestLidarrClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"queued"}`))
	}), "")

	if _, err := cli.RefreshArtist(7); err == nil || !strings.Contains(err.Error(), "no command id") {
		t.Fatalf("err = %v, want a missing-command-id error", err)
	}
}

func TestRefreshArtistReportsRejectedCredentials(t *testing.T) {
	cases := []struct {
		name, cookie, wantHint string
	}{
		{"with cookie", "session=stale", "cookie may have expired"},
		{"without cookie", "", "no cookie is configured"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cli := newTestLidarrClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte("nope"))
			}), tc.cookie)

			_, err := cli.RefreshArtist(1)
			if err == nil {
				t.Fatal("a 401 read as success")
			}
			for _, want := range []string{"401", "nope", tc.wantHint} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %q, want it to mention %q", err, want)
				}
			}
		})
	}
}

// A login portal answering 200 to a write must not read as the write succeeding.
func TestRefreshArtistLoginPageIsNotSuccess(t *testing.T) {
	cli := newTestLidarrClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>Sign in</body></html>"))
	}), "")

	_, err := cli.RefreshArtist(1)
	if err == nil {
		t.Fatal("an HTML 200 read as an accepted command")
	}
	if !strings.Contains(err.Error(), "not JSON") || !strings.Contains(err.Error(), "text/html") {
		t.Errorf("err = %q, want it to name the non-JSON body and its content type", err)
	}
}

func TestPostJSONRedirectIsFlagged(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/command", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusFound)
	})
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	cli := newTestLidarrClient(t, mux, "session=abc")

	_, err := cli.RefreshArtist(1)
	if err == nil {
		t.Fatal("a redirected write read as success")
	}
	if !strings.Contains(err.Error(), "redirected to") || !strings.Contains(err.Error(), "authentication proxy") {
		t.Errorf("err = %q, want the redirect named and the proxy hint", err)
	}
}

func TestPostJSONNilDestinationAndRateLimit(t *testing.T) {
	cli := newTestLidarrClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A body that would fail to decode: with dst nil it must never be read.
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("not json"))
	}), "")
	limited := 0
	cli.RateLimit = func(f func() error) error {
		limited++
		return f()
	}

	if err := cli.postJSON("/api/v1/command", map[string]any{"name": "Ping"}, nil); err != nil {
		t.Fatalf("postJSON with nil dst: %v", err)
	}
	if limited != 1 {
		t.Errorf("rate limiter ran %d times, want 1", limited)
	}
}

func TestPostJSONLocalFailures(t *testing.T) {
	cli := NewLidarrClient("http://127.0.0.1:1", "k", nil)

	// An unencodable body is caught before any request is built.
	if err := cli.postJSON("/x", map[string]any{"c": make(chan int)}, nil); err == nil ||
		!strings.Contains(err.Error(), "could not encode body") {
		t.Errorf("unencodable body err = %v", err)
	}

	// A URL that cannot be parsed fails at request construction.
	bad := NewLidarrClient("http://bad host", "k", nil)
	if err := bad.postJSON("/x", map[string]any{}, nil); err == nil ||
		!strings.Contains(err.Error(), "could not build request") {
		t.Errorf("unparseable URL err = %v", err)
	}

	// Nothing listening: a transport failure, named as such.
	srv := httptest.NewServer(http.NotFoundHandler())
	dead := NewLidarrClient(srv.URL, "k", nil)
	srv.Close()
	if err := dead.postJSON("/x", map[string]any{}, nil); err == nil ||
		!strings.Contains(err.Error(), "request failed") {
		t.Errorf("dead server err = %v", err)
	}
}

// commandSequence serves GET /api/v1/command/{id} from a scripted list of states,
// repeating the last one once the script runs out.
func commandSequence(t *testing.T, states ...string) (*LidarrClient, func() int) {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	cli := newTestLidarrClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/command/9" {
			t.Errorf("polled %s, want /api/v1/command/9", r.URL.Path)
		}
		mu.Lock()
		state := states[min(calls, len(states)-1)]
		calls++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 9, "name": "RefreshArtist", "status": state, "message": "metadata service said " + state,
		})
	}), "")
	return cli, func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
}

func TestWaitForCommandPollsUntilCompleted(t *testing.T) {
	fastLidarrCommandPolling(t, 0)
	cli, calls := commandSequence(t, "queued", "started", "completed")

	finished, err := cli.WaitForCommand(9)
	if err != nil || !finished {
		t.Fatalf("WaitForCommand = (%v, %v), want (true, nil)", finished, err)
	}
	if calls() != 3 {
		t.Errorf("status reads = %d, want 3 (queued, started, completed)", calls())
	}
}

func TestWaitForCommandReportsTerminalFailure(t *testing.T) {
	fastLidarrCommandPolling(t, 0)
	for _, state := range []string{lidarrCommandFailed, lidarrCommandAborted} {
		t.Run(state, func(t *testing.T) {
			cli, _ := commandSequence(t, "started", state)
			finished, err := cli.WaitForCommand(9)
			if !finished {
				t.Error("a terminal state did not report finished")
			}
			if err == nil || !strings.Contains(err.Error(), state) || !strings.Contains(err.Error(), "metadata service said") {
				t.Errorf("err = %v, want the state and Lidarr's own message", err)
			}
		})
	}
}

func TestWaitForCommandTimeoutIsUnknownNotFailure(t *testing.T) {
	fastLidarrCommandPolling(t, 5*time.Millisecond)
	cli, calls := commandSequence(t, "started")

	finished, err := cli.WaitForCommand(9)
	if finished || err != nil {
		t.Fatalf("WaitForCommand = (%v, %v), want (false, nil) on timeout", finished, err)
	}
	if calls() < 2 {
		t.Errorf("status reads = %d, want it to have polled more than once", calls())
	}
}

func TestWaitForCommandStatusReadFailure(t *testing.T) {
	fastLidarrCommandPolling(t, 0)
	cli := newTestLidarrClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}), "")

	finished, err := cli.WaitForCommand(9)
	if finished || err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("WaitForCommand = (%v, %v), want (false, a 404 error)", finished, err)
	}
}

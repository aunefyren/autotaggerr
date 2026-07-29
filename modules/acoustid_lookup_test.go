package modules

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The AcoustID client. Fingerprinting itself needs fpcalc on PATH and is skipped
// where it is absent, but the lookup — the part that talks to AcoustID and decides
// what a response *means* — is testable against a stub, and it is where the
// interesting rules live: one candidate per (recording, release) pair, because that
// pair is what a file can actually be attached to.

func TestLookupAcoustIDFlattensRecordingsAndReleases(t *testing.T) {
	var gotForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok","results":[{"id":"fp-1","score":0.98,"recordings":[
			{"id":"rec-1","title":"Nude","duration":255,"artists":[{"name":"Radiohead"}],"releases":[
				{"id":"rel-1","title":"In Rainbows","track_count":10,"date":{"year":2007}},
				{"id":"rel-2","title":"In Rainbows (deluxe)","track_count":18,"date":{"year":2008}}
			]},
			{"id":"rec-2","title":"Nude (live)","duration":260,"artists":[{"name":"Radiohead"}]}
		]}]}`)
	}))
	defer server.Close()

	candidates, err := LookupAcoustID("client-key", server.URL, AcoustIDFingerprint{Fingerprint: "AQAB", Duration: 255})
	if err != nil {
		t.Fatalf("LookupAcoustID: %v", err)
	}

	// Two releases of the first recording, plus the second recording with no release
	// of its own — a recording that identifies the song is still useful, and the user
	// picks the release.
	if len(candidates) != 3 {
		t.Fatalf("candidates = %d, want 3: %+v", len(candidates), candidates)
	}
	if candidates[0].RecordingMBID != "rec-1" || candidates[0].ReleaseMBID != "rel-1" {
		t.Errorf("first candidate = %+v", candidates[0])
	}
	if candidates[0].Score != 0.98 {
		t.Errorf("score = %v, want the result's own score", candidates[0].Score)
	}
	if candidates[0].Artist != "Radiohead" || candidates[0].TrackCount != 10 {
		t.Errorf("first candidate metadata = %+v", candidates[0])
	}
	if candidates[1].ReleaseYear != 2008 || candidates[1].TrackCount != 18 {
		t.Errorf("second candidate = %+v, want the deluxe edition", candidates[1])
	}
	// The releaseless recording keeps its identity and simply has no release.
	if candidates[2].RecordingMBID != "rec-2" || candidates[2].ReleaseMBID != "" {
		t.Errorf("third candidate = %+v", candidates[2])
	}

	// POSTed as a form: a fingerprint is kilobytes and overflows a URL.
	if got := gotForm.Get("client"); got != "client-key" {
		t.Errorf("client = %q, want client-key", got)
	}
	if got := gotForm.Get("fingerprint"); got != "AQAB" {
		t.Errorf("fingerprint = %q", got)
	}
	if got := gotForm.Get("duration"); got != "255" {
		t.Errorf("duration = %q, want 255", got)
	}
	// Without meta=recordings+releases the response carries ids and nothing a person
	// could choose between.
	if got := gotForm.Get("meta"); !strings.Contains(got, "recordings") || !strings.Contains(got, "releases") {
		t.Errorf("meta = %q, want recordings+releases", got)
	}
}

func TestLookupAcoustIDRequiresAKey(t *testing.T) {
	// The whole feature is off without a key, so this must read as "not configured"
	// rather than reaching the service and being rejected there.
	if _, err := LookupAcoustID("  ", "http://127.0.0.1:1", AcoustIDFingerprint{Fingerprint: "AQAB", Duration: 1}); err == nil {
		t.Error("a blank API key was accepted")
	}
}

func TestLookupAcoustIDSurfacesServiceErrors(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		wantIn  string
	}{
		{
			name: "error status in a 200 body",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				// AcoustID reports application errors inside a 200, so a status check
				// alone would read this as success.
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"status":"error","error":{"message":"invalid API key"}}`)
			},
			wantIn: "invalid API key",
		},
		{
			name: "error status with no message",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, `{"status":"error"}`)
			},
			wantIn: "error",
		},
		{
			name: "http failure",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "too many requests", http.StatusTooManyRequests)
			},
			wantIn: "429",
		},
		{
			name: "unparseable body",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, `not json at all`)
			},
			wantIn: "parse",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(tc.handler)
			defer server.Close()

			_, err := LookupAcoustID("key", server.URL, AcoustIDFingerprint{Fingerprint: "AQAB", Duration: 1})
			if err == nil {
				t.Fatal("err = nil, want a failure")
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("err = %q, want it to mention %q", err.Error(), tc.wantIn)
			}
		})
	}
}

func TestLookupAcoustIDEmptyResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok","results":[]}`)
	}))
	defer server.Close()

	// AcoustID knowing nothing about a fingerprint is the common case for obscure
	// or self-released audio: no candidates, no error.
	candidates, err := LookupAcoustID("key", server.URL, AcoustIDFingerprint{Fingerprint: "AQAB", Duration: 1})
	if err != nil {
		t.Fatalf("LookupAcoustID: %v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("candidates = %+v, want none", candidates)
	}
}

func TestLookupAcoustIDSkipsRecordingsWithoutAnID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok","results":[{"id":"fp-1","score":0.5,"recordings":[
			{"id":"","title":"Untitled"},
			{"id":"rec-1","title":"Real"}
		]}]}`)
	}))
	defer server.Close()

	candidates, err := LookupAcoustID("key", server.URL, AcoustIDFingerprint{Fingerprint: "AQAB", Duration: 1})
	if err != nil {
		t.Fatalf("LookupAcoustID: %v", err)
	}
	// A candidate with no recording MBID cannot be attached to anything, so offering
	// it would be offering a dead end.
	if len(candidates) != 1 || candidates[0].RecordingMBID != "rec-1" {
		t.Errorf("candidates = %+v, want only the identified recording", candidates)
	}
}

func TestAcoustIDThrottleSpacesCalls(t *testing.T) {
	// The limiter is AcoustID's documented ceiling, and deliberately not shared with
	// the MusicBrainz one — two services, two budgets.
	acoustidMu.Lock()
	acoustidLastCall = time.Time{}
	acoustidMu.Unlock()

	start := time.Now()
	acoustidThrottle()
	acoustidThrottle()
	if elapsed := time.Since(start); elapsed < acoustidRateLimit {
		t.Errorf("two calls took %s, want at least %s apart", elapsed, acoustidRateLimit)
	}
}

func TestFpcalcAvailabilityMatchesThePath(t *testing.T) {
	// Whether fpcalc exists is a fact about the deployment, so the answer must match
	// the environment rather than being assumed either way.
	_, err := exec.LookPath("fpcalc")
	if got, want := FpcalcAvailable(), err == nil; got != want {
		t.Errorf("FpcalcAvailable() = %v, want %v (fpcalc on PATH: %v)", got, want, err == nil)
	}
	// Looked up once and cached, so a second call must agree with the first.
	if FpcalcAvailable() != (err == nil) {
		t.Error("FpcalcAvailable changed answer on the second call")
	}
}

func TestFingerprintWithoutFpcalc(t *testing.T) {
	if FpcalcAvailable() {
		t.Skip("fpcalc is installed, so the unavailable path cannot be exercised here")
	}
	// A missing optional binary is reported as such, per file, rather than crashing
	// the scan that asked.
	if _, err := Fingerprint("/does/not/exist.flac"); err == nil {
		t.Error("Fingerprint succeeded without fpcalc")
	}
}

func TestSnippetTruncatesLongBodies(t *testing.T) {
	// Error bodies go into log lines and API errors; an HTML error page pasted whole
	// makes both unreadable.
	long := strings.Repeat("x", 500)
	got := snippet([]byte(long))
	if len(got) > 250 {
		t.Errorf("snippet length = %d, want it truncated", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("snippet = %q, want an ellipsis marking the truncation", got[len(got)-10:])
	}
	if got := snippet([]byte("  short  ")); got != "short" {
		t.Errorf("snippet = %q, want it trimmed", got)
	}
}

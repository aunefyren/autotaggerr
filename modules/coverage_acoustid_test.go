package modules

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// fpcalc is optional and absent from most dev machines and CI, so the parts of the
// AcoustID path that run it are exercised against a stand-in: a shell script that
// behaves the way fpcalc does (JSON on stdout, a message on stderr when it fails).

// withFakeFpcalc installs script as the fpcalc binary for one test.
func withFakeFpcalc(t *testing.T, script string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fpcalc")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatalf("write fake fpcalc: %v", err)
	}

	// Consume the once first, so a later FpcalcAvailable cannot overwrite the stand-in.
	FpcalcAvailable()
	origPath, origAvailable := fpcalcPath, fpcalcAvailable
	fpcalcPath, fpcalcAvailable = path, true
	t.Cleanup(func() { fpcalcPath, fpcalcAvailable = origPath, origAvailable })
}

func TestFingerprintParsesFpcalcOutput(t *testing.T) {
	withFakeFpcalc(t, `echo '{"duration": 254, "fingerprint": "AQADtEmUaE"}'`)

	fp, err := Fingerprint("/music/a.flac")
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if fp.Duration != 254 || fp.Fingerprint != "AQADtEmUaE" {
		t.Errorf("fingerprint = %+v", fp)
	}
}

func TestFingerprintFailures(t *testing.T) {
	cases := []struct {
		name, script, want string
	}{
		// fpcalc's own message is the useful part of a failure, so it is quoted.
		{"stderr", `echo "ERROR: Could not open the input file" >&2; exit 2`, "Could not open the input file"},
		{"silent exit", `exit 3`, "fpcalc failed"},
		{"not json", `echo "DURATION=254"`, "could not read fpcalc output"},
		{"empty fingerprint", `echo '{"duration": 254, "fingerprint": ""}'`, "no usable fingerprint"},
		{"zero duration", `echo '{"duration": 0, "fingerprint": "AQAD"}'`, "no usable fingerprint"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withFakeFpcalc(t, tc.script)
			_, err := Fingerprint("/music/a.flac")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// acoustidStub answers /lookup with one recording and counts the calls.
func acoustidStub(t *testing.T) (*httptest.Server, *int64) {
	t.Helper()
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		fmt.Fprint(w, `{"status":"ok","results":[{"id":"fp","score":0.97,"recordings":[
			{"id":"rec-1","title":"Track","artists":[{"name":"Artist"}],"releases":[
				{"id":"rel-1","title":"Album","track_count":1,"date":{"year":2020}}]}]}]}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// A cold IdentifyFile fingerprints, looks up, and stores both — so the second call
// is the cheap one the UI button relies on.
func TestIdentifyFileFingerprintsLooksUpAndCaches(t *testing.T) {
	db := withMigrationDB(t)
	withFakeFpcalc(t, `echo '{"duration": 200, "fingerprint": "AQADfresh"}'`)
	srv, calls := acoustidStub(t)

	path := "/music/Artist/Album (2020)/01 Track.flac"
	mod := time.Now().Truncate(time.Second)

	got, err := IdentifyFile(path, "key", srv.URL, 1000, mod)
	if err != nil {
		t.Fatalf("IdentifyFile: %v", err)
	}
	if len(got) == 0 || got[0].ReleaseMBID != "rel-1" {
		t.Fatalf("ranked = %+v, want the stubbed release", got)
	}

	var row models.AcoustIDLookup
	if err := db.First(&row, "path = ?", path).Error; err != nil {
		t.Fatalf("lookup was not cached: %v", err)
	}
	if row.Fingerprint != "AQADfresh" || row.Duration != 200 || row.LookedUpAt == nil || row.Size != 1000 {
		t.Errorf("cached row = %+v", row)
	}
	var cached []AcoustIDCandidate
	if err := json.Unmarshal([]byte(row.Candidates), &cached); err != nil || len(cached) != 1 {
		t.Errorf("cached candidates = %q (%v)", row.Candidates, err)
	}

	// Second call: same size and mtime, so neither fpcalc nor AcoustID is touched.
	// Breaking the fake proves the subprocess is not run.
	withFakeFpcalc(t, `exit 1`)
	if _, err := IdentifyFile(path, "key", srv.URL, 1000, mod); err != nil {
		t.Fatalf("cached IdentifyFile: %v", err)
	}
	if n := atomic.LoadInt64(calls); n != 1 {
		t.Errorf("AcoustID calls = %d, want 1 — the cache did not hold", n)
	}
}

// A changed file invalidates the cache, and a corrupt stored candidate list is
// re-asked rather than trusted.
func TestIdentifyFileRefreshesOnChangeOrCorruptCache(t *testing.T) {
	db := withMigrationDB(t)
	srv, calls := acoustidStub(t)
	path := "/music/Artist/Album (2020)/02 Track.flac"
	mod := time.Now().Truncate(time.Second)
	lookedUp := mod

	if err := db.Create(&models.AcoustIDLookup{
		Path: path, Size: 500, ModTime: &mod,
		Fingerprint: "AQADold", Duration: 100,
		Candidates: "{not json", LookedUpAt: &lookedUp, FetchedAt: mod,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Fresh fingerprint, corrupt candidates: no fpcalc needed, one lookup.
	if _, err := IdentifyFile(path, "key", srv.URL, 500, mod); err != nil {
		t.Fatalf("IdentifyFile (corrupt cache): %v", err)
	}
	if n := atomic.LoadInt64(calls); n != 1 {
		t.Errorf("AcoustID calls = %d, want 1 after a corrupt cache", n)
	}

	// Size changed: the stored fingerprint is for a different file now.
	withFakeFpcalc(t, `echo '{"duration": 101, "fingerprint": "AQADnew"}'`)
	if _, err := IdentifyFile(path, "key", srv.URL, 501, mod); err != nil {
		t.Fatalf("IdentifyFile (changed): %v", err)
	}
	var row models.AcoustIDLookup
	if err := db.First(&row, "path = ?", path).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if row.Fingerprint != "AQADnew" || row.Size != 501 {
		t.Errorf("row = %+v, want the re-fingerprinted file", row)
	}
}

func TestIdentifyFilePropagatesFailures(t *testing.T) {
	withMigrationDB(t)

	withFakeFpcalc(t, `exit 1`)
	if _, err := IdentifyFile("/music/A/B (2020)/x.flac", "key", "http://127.0.0.1:1", 1, time.Now()); err == nil {
		t.Error("a failed fingerprint did not fail the identify")
	}

	withFakeFpcalc(t, `echo '{"duration": 1, "fingerprint": "AQAD"}'`)
	if _, err := IdentifyFile("/music/A/B (2020)/x.flac", "", "http://127.0.0.1:1", 1, time.Now()); err == nil {
		t.Error("a failed lookup did not fail the identify")
	}
}

// LookupAcoustID's own failure modes that the existing service-error test does
// not reach: the transport failing, and a non-ok status with no message.
func TestLookupAcoustIDTransportAndBareStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"error"}`)
	}))
	defer srv.Close()

	_, err := LookupAcoustID("k", srv.URL, AcoustIDFingerprint{Fingerprint: "x", Duration: 1})
	if err == nil || !strings.Contains(err.Error(), "rejected the lookup: error") {
		t.Errorf("bare status err = %v, want the status used as the message", err)
	}

	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	if _, err := LookupAcoustID("k", dead.URL, AcoustIDFingerprint{Fingerprint: "x", Duration: 1}); err == nil ||
		!strings.Contains(err.Error(), "request failed") {
		t.Errorf("transport err = %v", err)
	}
}

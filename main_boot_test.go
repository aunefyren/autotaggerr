//go:build !windows

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/gin-gonic/gin"
)

// freePort asks the kernel for a port nothing is listening on.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// TestMainBootsServesAndShutsDown runs the real entry point end to end in a temp
// working directory: config load, database connect + seed, the single-file path, the
// router, and the signal-driven shutdown. It is the only test that proves the wiring
// order in main holds together — every helper it calls is tested on its own, but
// nothing else checks that they compose into a process that starts and stops.
//
// The configuration is chosen to walk the branches a real deployment hits on a bad
// day: a timezone that does not exist (removed and saved back, not fatal), a Plex
// connection (served by httptest), and a --file that matches no library (logged, not
// fatal). SIGINT is what a container stop delivers.
func TestMainBootsServesAndShutsDown(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	configDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(files.SetConfigPaths(configDir))

	plex := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"MediaContainer":{}}`))
	}))
	t.Cleanup(plex.Close)

	port := freePort(t)
	cfg := map[string]any{
		"autotaggerr_port":        port,
		"autotaggerr_environment": "prod",
		"autotaggerr_log_level":   "panic",
		"timezone":                "Not/A_Zone",
		"plex_base_url":           plex.URL,
		"plex_token":              "token",
	}
	raw, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	// Globals main replaces; put them back so the rest of the package's tests see
	// what they expect.
	oldArgs, oldFlags, oldLog, oldConfig := os.Args, flag.CommandLine, logger.Log, files.ConfigFile
	t.Cleanup(func() {
		os.Args, flag.CommandLine, logger.Log, files.ConfigFile = oldArgs, oldFlags, oldLog, oldConfig
		gin.SetMode(gin.TestMode)
	})
	flag.CommandLine = flag.NewFlagSet("autotaggerr", flag.ContinueOnError)
	missing := filepath.Join(dir, "music", "Artist", "Album (2020)", "01 Track.flac")
	os.Args = []string{"autotaggerr", "-file", missing, "-fileRoot", filepath.Join(dir, "music")}

	// Registering our own channel first turns off SIGINT's default action, so a
	// signal that lands before main's own Notify does not kill the test binary.
	guard := make(chan os.Signal, 16)
	signal.Notify(guard, os.Interrupt)
	t.Cleanup(func() { signal.Stop(guard) })

	done := make(chan struct{})
	go func() {
		defer close(done)
		main()
	}()

	// Wait for the server to answer, which is the last thing main does before it
	// blocks in shutdown.
	pingURL := fmt.Sprintf("http://127.0.0.1:%d/api/ping", port)
	deadline := time.Now().Add(20 * time.Second)
	for {
		resp, err := http.Get(pingURL)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET /api/ping = %d, want 200", resp.StatusCode)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never came up: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Keep signalling until main returns: the server answering does not prove
	// shutdown has registered for the signal yet.
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	timeout := time.After(45 * time.Second)
	for stopped := false; !stopped; {
		select {
		case <-done:
			stopped = true
		case <-tick.C:
			_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
		case <-timeout:
			t.Fatal("main did not return after SIGINT")
		}
	}

	if _, err := http.Get(pingURL); err == nil {
		t.Error("server still answering after shutdown")
	}
	if files.ConfigFile.Timezone != "" {
		t.Errorf("invalid timezone kept as %q, want it removed", files.ConfigFile.Timezone)
	}
	if plexClient == nil || plexClient.BaseURL != plex.URL {
		t.Errorf("plex client not built from config: %+v", plexClient)
	}
	if gin.Mode() != gin.ReleaseMode {
		t.Errorf("gin mode = %q outside the test environment, want release", gin.Mode())
	}

	// The removed timezone was saved back, so the next boot does not trip on it.
	saved, err := os.ReadFile(filepath.Join(configDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(saved, &back); err != nil {
		t.Fatal(err)
	}
	if back["timezone"] != "" {
		t.Errorf("saved timezone = %v, want empty", back["timezone"])
	}
	if _, err := os.Stat(filepath.Join(dir, "config", "autotaggerr.db")); err != nil {
		t.Errorf("database not created under the working directory: %v", err)
	}
}

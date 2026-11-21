// Command demo builds and runs an offline demo instance of Autotaggerr: a synthetic
// library of tiny but valid audio files, a pre-filled database, and a stand-in
// MusicBrainz, so the app can be tried at any size with no real music and no
// network. See docs/demo.md.
//
//	go run ./tools/demo generate -dir ~/autotaggerr-demo -size medium
//	go run ./tools/demo run      -dir ~/autotaggerr-demo   (generates first if needed)
//	go run ./tools/demo serve    -dir ~/autotaggerr-demo   (the stand-in only)
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func defaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "autotaggerr-demo"
	}
	return filepath.Join(home, "autotaggerr-demo")
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: go run ./tools/demo <command> [flags]

commands:
  generate   build a demo instance (files, config, database)
  run        start the stand-in MusicBrainz and the app (generating the demo first if needed)
  serve      start only the stand-in MusicBrainz

run "go run ./tools/demo <command> -h" for a command's flags`)
	os.Exit(2)
}

func main() {
	log.SetFlags(log.Ltime)
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "demo directory (outside the repository)")
	switch cmd {
	case "generate":
		size := fs.String("size", "small", "library size: "+presetNames())
		seed := fs.Int64("seed", 1, "seed; the same seed and size always give the same library")
		raw := fs.Bool("raw", false, "leave the library unindexed so the first Process does the work")
		_ = fs.Parse(args)
		preset, ok := presetByName(*size)
		if !ok {
			log.Fatalf("unknown size %q (want %s)", *size, presetNames())
		}
		u, err := Generate(GenerateOptions{Dir: *dir, Seed: *seed, Preset: preset, Raw: *raw, Log: log.Printf})
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("demo ready in %s: %d track files, %d unmatched. Start it with: go run ./tools/demo run -dir %s", *dir, u.TrackFiles(), u.Unmatched, *dir)
	case "serve":
		_ = fs.Parse(args)
		stub, marker := loadStub(*dir)
		log.Printf("stand-in MusicBrainz on http://%s (seed %d, %s)", marker.StubAddr, marker.Seed, marker.Size)
		log.Fatal(http.ListenAndServe(marker.StubAddr, stub))
	case "run":
		binary := fs.String("binary", "", "app binary to run (default: build the current checkout into the demo directory)")
		size := fs.String("size", "small", "library size, when the demo has to be generated first: "+presetNames())
		seed := fs.Int64("seed", 1, "seed, when the demo has to be generated first")
		_ = fs.Parse(args)
		if _, err := readMarker(*dir); err != nil {
			preset, ok := presetByName(*size)
			if !ok {
				log.Fatalf("unknown size %q (want %s)", *size, presetNames())
			}
			log.Printf("no demo in %s yet; generating a %s one", *dir, preset.Name)
			if _, err := Generate(GenerateOptions{Dir: *dir, Seed: *seed, Preset: preset, Log: log.Printf}); err != nil {
				log.Fatal(err)
			}
		}
		runDemo(*dir, *binary)
	default:
		usage()
	}
}

func presetNames() string {
	var names []string
	for _, p := range Presets {
		names = append(names, p.Name)
	}
	return strings.Join(names, ", ")
}

func loadStub(dir string) (*Stub, Marker) {
	marker, err := readMarker(dir)
	if err != nil {
		log.Fatal(err)
	}
	preset, ok := presetByName(marker.Size)
	if !ok {
		log.Fatalf("demo directory names unknown size %q", marker.Size)
	}
	return NewStub(GenerateUniverse(marker.Seed, preset)), marker
}

// runDemo starts the stand-in and the app (cwd = the demo directory, so the app's
// ./config is the demo's), and stops both on Ctrl+C.
func runDemo(dir, binary string) {
	dir, _ = filepath.Abs(dir)
	stub, marker := loadStub(dir)
	stub.Quiet = os.Getenv("DEMO_STUB_LOG") == ""
	server := &http.Server{Addr: marker.StubAddr, Handler: stub}
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("stand-in MusicBrainz: %s", err)
		}
	}()

	if binary == "" {
		binary = filepath.Join(dir, "autotaggerr")
		log.Printf("building the app into %s", binary)
		build := exec.Command("go", "build", "-o", binary, ".")
		build.Stdout, build.Stderr = os.Stdout, os.Stderr
		if err := build.Run(); err != nil {
			log.Fatalf("go build (run this from the repository root): %s", err)
		}
	}

	app := exec.Command(binary)
	app.Dir = dir
	app.Stdout, app.Stderr = os.Stdout, os.Stderr
	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
	log.Printf("demo on http://localhost:%d (admin / admin), stand-in MusicBrainz on http://%s", appPort, marker.StubAddr)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- app.Wait() }()
	select {
	case <-stop:
		_ = app.Process.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(40 * time.Second):
			_ = app.Process.Kill()
		}
	case err := <-done:
		if err != nil {
			log.Printf("app exited: %s", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
	log.Printf("stand-in served %d requests", stub.Requests.Load())
}

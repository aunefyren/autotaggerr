package main

import (
	"flag"
	"io"
	"os"
	"testing"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/sirupsen/logrus"
)

func init() {
	// main logs through logger.Log, which is nil until InitLogger runs.
	if logger.Log == nil {
		logger.Log = logrus.New()
		logger.Log.SetOutput(io.Discard)
	}
}

// runParseFlags invokes parseFlags with a fresh flag set + os.Args so it can be
// called repeatedly without "flag redefined" panics.
func runParseFlags(t *testing.T, args []string, cfg models.ConfigStruct) (models.ConfigStruct, *string, *string) {
	t.Helper()
	oldArgs, oldFlags := os.Args, flag.CommandLine
	t.Cleanup(func() { os.Args, flag.CommandLine = oldArgs, oldFlags })

	flag.CommandLine = flag.NewFlagSet("autotaggerr", flag.ContinueOnError)
	os.Args = append([]string{"autotaggerr"}, args...)

	got, fp, frp, err := parseFlags(cfg)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	return got, fp, frp
}

func TestParseFlagsDefaults(t *testing.T) {
	cfg := models.ConfigStruct{AutotaggerrPort: 8080, AutotaggerrProcessConcurrency: 4}
	got, fp, frp := runParseFlags(t, nil, cfg)

	if got.AutotaggerrProcessConcurrency != 4 {
		t.Errorf("concurrency = %d, want 4 (unchanged)", got.AutotaggerrProcessConcurrency)
	}
	if fp != nil || frp != nil {
		t.Errorf("expected nil file paths with no flags, got %v / %v", fp, frp)
	}
}

func TestParseFlagsOverrides(t *testing.T) {
	cfg := models.ConfigStruct{AutotaggerrPort: 8080, AutotaggerrProcessConcurrency: 4}
	got, _, _ := runParseFlags(t, []string{"-port", "1234", "-concurrency", "8"}, cfg)

	if got.AutotaggerrPort != 1234 {
		t.Errorf("port = %d, want 1234", got.AutotaggerrPort)
	}
	if got.AutotaggerrProcessConcurrency != 8 {
		t.Errorf("concurrency = %d, want 8", got.AutotaggerrProcessConcurrency)
	}
}

func TestParseFlagsSingleFile(t *testing.T) {
	cfg := models.ConfigStruct{AutotaggerrPort: 8080}

	// both file + fileRoot -> single-file mode
	_, fp, frp := runParseFlags(t, []string{"-file", "/music/a.flac", "-fileRoot", "/music"}, cfg)
	if fp == nil || frp == nil || *fp != "/music/a.flac" || *frp != "/music" {
		t.Fatalf("expected single-file paths, got %v / %v", fp, frp)
	}

	// file without fileRoot -> both nil (service mode)
	_, fp2, frp2 := runParseFlags(t, []string{"-file", "/music/a.flac"}, cfg)
	if fp2 != nil || frp2 != nil {
		t.Errorf("expected nil paths when fileRoot missing, got %v / %v", fp2, frp2)
	}
}

func TestParseFlagsPortFailsafe(t *testing.T) {
	// port 0 in config with no flag -> defaults to 8080
	got, _, _ := runParseFlags(t, nil, models.ConfigStruct{AutotaggerrPort: 0})
	if got.AutotaggerrPort != 8080 {
		t.Errorf("port failsafe = %d, want 8080", got.AutotaggerrPort)
	}
}

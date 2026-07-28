package scan

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/database"
	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/sirupsen/logrus"
)

func init() {
	logger.Log = logrus.New()
	logger.Log.SetOutput(io.Discard)
}

func TestRunnerRunAll(t *testing.T) {
	root := t.TempDir()
	albumDir := filepath.Join(root, "Artist", "Album (2020)")
	if err := os.MkdirAll(albumDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// An invalid FLAC: correlation fails, so the file is counted as an error and the
	// run completes without touching the network.
	if err := os.WriteFile(filepath.Join(albumDir, "01 track.flac"), []byte("not a real flac"), 0o644); err != nil {
		t.Fatal(err)
	}

	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := db.Create(&models.Library{Name: "L", Path: root, Enabled: true}).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}

	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrProcessConcurrency: 2, AutotaggerrVersion: "test"})
	r.RunAll()

	s := r.Status()
	if s.Running {
		t.Error("runner should be idle after RunAll returns")
	}
	if s.FinishedAt == nil || s.StartedAt == nil {
		t.Error("start/finish timestamps not set")
	}
	if s.Errors != 1 {
		t.Errorf("errors = %d, want 1 (the invalid flac)", s.Errors)
	}

	// The scan should have been recorded as an Activity event.
	var ev models.Event
	if err := db.Where("type = ?", models.EventTypeScan).First(&ev).Error; err != nil {
		t.Fatalf("scan event not recorded: %v", err)
	}
	if ev.Status != models.EventStatusError || ev.FinishedAt == nil {
		t.Errorf("scan event should be finished with error status: %+v", ev)
	}
	if errs, ok := ev.Details["errors"].(float64); !ok || errs != 1 {
		t.Errorf("event details errors = %#v, want 1", ev.Details["errors"])
	}
}

func TestRunnerSyncDriftEmitsEvent(t *testing.T) {
	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})
	r.SyncDrift() // no cached releases due -> a clean no-op sync

	var ev models.Event
	if err := db.Where("type = ?", models.EventTypeDriftSync).First(&ev).Error; err != nil {
		t.Fatalf("drift event not recorded: %v", err)
	}
	if ev.Status != models.EventStatusOK || ev.FinishedAt == nil {
		t.Errorf("drift event should finish ok: %+v", ev)
	}
	if checked, ok := ev.Details["releases_checked"].(float64); !ok || checked != 0 {
		t.Errorf("releases_checked = %#v, want 0", ev.Details["releases_checked"])
	}
}

func TestRunnerNoLibraries(t *testing.T) {
	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	r := NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})
	r.RunAll() // no libraries -> no-op, no panic
	if r.Running() {
		t.Error("runner should be idle")
	}
}

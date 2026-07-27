package logger

import (
	"os"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
)

// TestInitLogger verifies the logger initializes and honors the configured level.
// InitLogger writes to config/autotaggerr.log relative to the working dir, so we
// create (and clean up) that directory first to avoid its Fatalf-on-error path.
func TestInitLogger(t *testing.T) {
	if err := os.MkdirAll("config", 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll("config") })

	InitLogger(models.ConfigStruct{AutotaggerrLogLevel: "debug"})
	if Log == nil {
		t.Fatal("Log should be initialized")
	}
	if Log.GetLevel().String() != "debug" {
		t.Errorf("log level = %s, want debug", Log.GetLevel())
	}

	// An invalid level should fall back to info rather than crash.
	InitLogger(models.ConfigStruct{AutotaggerrLogLevel: "not-a-level"})
	if Log.GetLevel().String() != "info" {
		t.Errorf("invalid level fallback = %s, want info", Log.GetLevel())
	}
}

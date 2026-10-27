package logger

import (
	"io"
	"os"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/sirupsen/logrus"
)

// Log is usable before InitLogger has run. It starts as a plain stderr logger rather
// than nil because the alternative is a nil-pointer panic in any code that logs
// before startup reaches InitLogger — which is every package's test binary, and
// would make "does this function log?" a hidden precondition of calling it.
// InitLogger replaces it with the configured one.
var Log = logrus.New()

func InitLogger(configFile models.ConfigStruct) {
	Log = logrus.New()

	// Define log file, capped and rotated (rotate.go)
	logFile, err := openRotating(LogPath, int64(positive(configFile.AutotaggerrLogMaxSizeMB, models.DefaultLogMaxSizeMB))<<20,
		positive(configFile.AutotaggerrLogKeep, models.DefaultLogKeep))
	if err != nil {
		logrus.Fatalf("Failed to load log file: %v", err)
	}
	active = logFile

	// Set a plain text format with old-style timestamp
	Log.SetFormatter(&logrus.JSONFormatter{})

	// Output to both stdout and log file
	mw := io.MultiWriter(os.Stdout, logFile)
	Log.SetOutput(mw)

	// Set log level
	level, err := logrus.ParseLevel(configFile.AutotaggerrLogLevel)
	if err != nil {
		logrus.Errorf("Failed to load log file: %v", err)
		level = logrus.InfoLevel
	}

	Log.SetLevel(level)

	Log.Info("Log level set to: " + level.String())
}

func positive(v, fallback int) int {
	if v < 1 {
		return fallback
	}
	return v
}

package files

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sirupsen/logrus"
)

// keepConfig restores the process-global ConfigFile after a test that loads or
// replaces it.
func keepConfig(t *testing.T) {
	t.Helper()
	original := ConfigFile
	t.Cleanup(func() { ConfigFile = original })
}

// skipIfRoot skips a test that relies on file permissions being enforced; root
// ignores them.
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission bits are not enforced for root")
	}
}

// TestSetConfigPathsRedirectsAndRestores: the seam other packages use must actually
// move where config is written, and its restore func must put the old paths back.
func TestSetConfigPathsRedirectsAndRestores(t *testing.T) {
	keepConfig(t)
	beforeDir, beforeFile := configDirectoryPath, configFilePath
	dir := t.TempDir()

	restore := SetConfigPaths(dir)
	if configDirectoryPath != dir || configFilePath != filepath.Join(dir, "config.json") {
		t.Fatalf("paths not redirected: %q, %q", configDirectoryPath, configFilePath)
	}
	if err := LoadConfig(); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		t.Errorf("config.json was not written into the redirected directory: %v", err)
	}

	restore()
	if configDirectoryPath != beforeDir || configFilePath != beforeFile {
		t.Errorf("restore left %q, %q; want %q, %q", configDirectoryPath, configFilePath, beforeDir, beforeFile)
	}
}

// TestLoadConfigFillsEveryEmptyField: an empty object is the most a hand-written
// config can omit, and every default has to come back — including the port, which
// the partial-config test keeps.
func TestLoadConfigFillsEveryEmptyField(t *testing.T) {
	redirectConfig(t)
	keepConfig(t)
	if err := os.WriteFile(configFilePath, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := LoadConfig(); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if ConfigFile.AutotaggerrPort != 8080 {
		t.Errorf("port = %d, want the 8080 default", ConfigFile.AutotaggerrPort)
	}
	if ConfigFile.AutotaggerrVersion != autotaggerrVersionParameter {
		t.Errorf("version = %q, want the release tag", ConfigFile.AutotaggerrVersion)
	}
}

// TestLoadConfigResetsAnUnknownLogLevel: a level logrus cannot parse is replaced by
// info and written back, rather than failing startup.
func TestLoadConfigResetsAnUnknownLogLevel(t *testing.T) {
	redirectConfig(t)
	keepConfig(t)
	previousLevel := logrus.GetLevel()
	t.Cleanup(func() { logrus.SetLevel(previousLevel) })

	if err := os.WriteFile(configFilePath, []byte(`{"autotaggerr_log_level": "shouty"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := LoadConfig(); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if ConfigFile.AutotaggerrLogLevel != "info" {
		t.Errorf("log level = %q, want info", ConfigFile.AutotaggerrLogLevel)
	}

	// And a valid level is applied to logrus.
	if err := os.WriteFile(configFilePath, []byte(`{"autotaggerr_log_level": "warning"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := LoadConfig(); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if logrus.GetLevel() != logrus.WarnLevel {
		t.Errorf("logrus level = %s, want warning", logrus.GetLevel())
	}
}

func TestLoadConfigRejectsMalformedJSON(t *testing.T) {
	redirectConfig(t)
	keepConfig(t)
	if err := os.WriteFile(configFilePath, []byte(`{"autotaggerr_port": `), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := LoadConfig(); err == nil {
		t.Error("a truncated config.json should fail to load")
	}
}

func TestLoadConfigReportsAnUnreadableFile(t *testing.T) {
	skipIfRoot(t)
	redirectConfig(t)
	keepConfig(t)
	if err := os.WriteFile(configFilePath, []byte(`{}`), 0o000); err != nil {
		t.Fatal(err)
	}
	if err := LoadConfig(); err == nil {
		t.Error("an unreadable config.json should fail to load")
	}
}

// TestLoadConfigReportsAFailedBackfillSave: defaults that cannot be written back are
// an error, not a silent in-memory-only config.
func TestLoadConfigReportsAFailedBackfillSave(t *testing.T) {
	skipIfRoot(t)
	redirectConfig(t)
	keepConfig(t)
	if err := os.WriteFile(configFilePath, []byte(`{}`), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := LoadConfig(); err == nil {
		t.Error("a read-only config.json that needs back-filling should fail to load")
	}
}

// TestCreateConfigFileFailsWhenTheDirectoryCannotBeMade: a config directory under a
// regular file cannot be created, and both the first-run path and SaveConfig say so.
func TestCreateConfigFileFailsWhenTheDirectoryCannotBeMade(t *testing.T) {
	keepConfig(t)
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(SetConfigPaths(filepath.Join(blocker, "config")))

	if err := SaveConfig(); err == nil {
		t.Error("SaveConfig should fail when the directory cannot be created")
	}
	if err := CreateConfigFile(); err == nil {
		t.Error("CreateConfigFile should fail when the config cannot be saved")
	}
	if err := LoadConfig(); err == nil {
		t.Error("LoadConfig should surface the first-run creation failure")
	}
}

// TestSaveConfigFailsWhenThePathIsADirectory: the open, not the mkdir, is what fails.
func TestSaveConfigFailsWhenThePathIsADirectory(t *testing.T) {
	redirectConfig(t)
	keepConfig(t)
	if err := os.Mkdir(configFilePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SaveConfig(); err == nil {
		t.Error("SaveConfig should fail when config.json is a directory")
	}
}

// TestLoadConfigReportsAFailedFirstRun: no config.json and a directory it cannot be
// written into fails the load instead of running on an unsaved default config.
func TestLoadConfigReportsAFailedFirstRun(t *testing.T) {
	skipIfRoot(t)
	keepConfig(t)
	dir := filepath.Join(t.TempDir(), "config")
	if err := os.Mkdir(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(SetConfigPaths(dir))

	if err := LoadConfig(); err == nil {
		t.Error("LoadConfig should fail when the first-run config cannot be written")
	}
}

package files

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

// redirectConfig points the package config paths at a temp dir and restores them
// afterwards, so tests never touch the real ./config.
func redirectConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	origDir, origFile := configDirectoryPath, configFilePath
	configDirectoryPath = dir
	configFilePath = filepath.Join(dir, "config.json")
	t.Cleanup(func() {
		configDirectoryPath, configFilePath = origDir, origFile
	})
}

func TestLoadConfigCreatesDefaults(t *testing.T) {
	redirectConfig(t)

	if err := LoadConfig(); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if ConfigFile.AutotaggerrPort != 8080 {
		t.Errorf("default port = %d, want 8080", ConfigFile.AutotaggerrPort)
	}
	if ConfigFile.AutotaggerrProcessConcurrency != 4 {
		t.Errorf("default concurrency = %d, want 4", ConfigFile.AutotaggerrProcessConcurrency)
	}
	if ConfigFile.AutotaggerrName != "Autotaggerr" {
		t.Errorf("default name = %q, want Autotaggerr", ConfigFile.AutotaggerrName)
	}
	if ConfigFile.AutotaggerrProcessCronSchedule == "" {
		t.Error("default cron schedule should be set")
	}
	if ConfigFile.PrivateKey == "" {
		t.Error("a private key should be generated on first load")
	}
}

// TestLoadConfigBackfillsDefaults writes a partial config (only a port) and
// confirms LoadConfig fills every missing field via its back-fill branches
// without discarding the value that was present.
func TestLoadConfigBackfillsDefaults(t *testing.T) {
	redirectConfig(t)
	if err := os.WriteFile(configFilePath, []byte(`{"autotaggerr_port": 7000}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := LoadConfig(); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if ConfigFile.AutotaggerrPort != 7000 {
		t.Errorf("port = %d, want preserved 7000", ConfigFile.AutotaggerrPort)
	}
	if ConfigFile.AutotaggerrName != "Autotaggerr" {
		t.Errorf("name not back-filled: %q", ConfigFile.AutotaggerrName)
	}
	if ConfigFile.AutotaggerrProcessConcurrency != 4 {
		t.Errorf("concurrency not back-filled: %d", ConfigFile.AutotaggerrProcessConcurrency)
	}
	if ConfigFile.AutotaggerrProcessCronSchedule == "" {
		t.Error("cron schedule not back-filled")
	}
	if ConfigFile.AutotaggerrCustomArtistDelimiter == "" {
		t.Error("artist delimiter not back-filled")
	}
	if ConfigFile.PrivateKey == "" {
		t.Error("private key not generated")
	}
}

func TestSaveAndReloadConfig(t *testing.T) {
	redirectConfig(t)
	if err := LoadConfig(); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	ConfigFile.AutotaggerrPort = 9999
	ConfigFile.AutotaggerrProcessConcurrency = 12
	if err := SaveConfig(); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	// Wipe in-memory config and reload from disk.
	ConfigFile.AutotaggerrPort = 0
	if err := LoadConfig(); err != nil {
		t.Fatalf("reload LoadConfig: %v", err)
	}
	if ConfigFile.AutotaggerrPort != 9999 {
		t.Errorf("port after reload = %d, want 9999", ConfigFile.AutotaggerrPort)
	}
	if ConfigFile.AutotaggerrProcessConcurrency != 12 {
		t.Errorf("concurrency after reload = %d, want 12", ConfigFile.AutotaggerrProcessConcurrency)
	}
}

func TestGenerateSecureKey(t *testing.T) {
	k1, err := GenerateSecureKey(64)
	if err != nil {
		t.Fatalf("GenerateSecureKey: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(k1)
	if err != nil {
		t.Fatalf("key is not valid base64: %v", err)
	}
	if len(raw) != 64 {
		t.Errorf("decoded key length = %d, want 64", len(raw))
	}

	k2, _ := GenerateSecureKey(64)
	if k1 == k2 {
		t.Error("two generated keys should differ")
	}
}

func TestGetPrivateKey(t *testing.T) {
	want := []byte("sixteen-byte-key")
	ConfigFile.PrivateKey = base64.StdEncoding.EncodeToString(want)
	got := GetPrivateKey(0)
	if string(got) != string(want) {
		t.Errorf("GetPrivateKey = %q, want %q", got, want)
	}
}

// TestGetPrivateKeyRecoversFromACorruptKey: the signing key is what every session
// token is signed with, so a config file holding an unparseable one must self-heal
// rather than take the service down. The cost is that existing sessions stop
// verifying, which is the correct trade — they were signed with a key nobody has.
func TestGetPrivateKeyRecoversFromACorruptKey(t *testing.T) {
	// ResetSecureKey persists the replacement, so the config paths must point
	// somewhere writable that is not the real ./config.
	redirectConfig(t)

	original := ConfigFile
	t.Cleanup(func() { ConfigFile = original })

	ConfigFile.PrivateKey = "this is not base64!!!"
	got := GetPrivateKey(0)

	if len(got) == 0 {
		t.Fatal("GetPrivateKey returned no key after a corrupt one")
	}
	// The replacement is a real 64-byte key, and it was written back so the next
	// start does not have to regenerate it again.
	if len(got) != 64 {
		t.Errorf("recovered key length = %d, want 64", len(got))
	}
	if ConfigFile.PrivateKey == "this is not base64!!!" {
		t.Error("the corrupt key was left in the config")
	}
	if _, err := base64.StdEncoding.DecodeString(ConfigFile.PrivateKey); err != nil {
		t.Errorf("the replacement key is not valid base64: %v", err)
	}
}

// TestResetSecureKeyReplacesTheKey: called when a key is unusable, and directly by
// GetPrivateKey's recovery path.
func TestResetSecureKeyReplacesTheKey(t *testing.T) {
	redirectConfig(t)

	original := ConfigFile
	t.Cleanup(func() { ConfigFile = original })

	ConfigFile.PrivateKey = base64.StdEncoding.EncodeToString([]byte("the-old-key"))
	before := ConfigFile.PrivateKey

	ResetSecureKey()

	if ConfigFile.PrivateKey == before {
		t.Error("ResetSecureKey left the old key in place")
	}
	if _, err := base64.StdEncoding.DecodeString(ConfigFile.PrivateKey); err != nil {
		t.Errorf("new key is not valid base64: %v", err)
	}
}

package modules

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/database"
	"github.com/aunefyren/autotaggerr/models"
)

// dbForCache opens a temp DB, wires it into the cache layer, and resets global
// cache state on cleanup so other tests see the legacy (JSON) behavior.
func dbForCache(t *testing.T) {
	t.Helper()
	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "cache.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	SetDB(db)
	resetMap()
	t.Cleanup(func() {
		SetDB(nil)
		resetMap()
	})
}

func resetMap() {
	musicbrainzReleaseCacheMu.Lock()
	musicbrainzReleaseCache = map[string]models.CachedMusicBrainzRelease{}
	musicbrainzReleaseCacheMu.Unlock()
}

func TestMusicbrainzCacheDBRoundTrip(t *testing.T) {
	dbForCache(t)

	entry := models.CachedMusicBrainzRelease{
		Release:   models.MusicBrainzReleaseResponse{ID: "rel-x", Title: "Round Trip"},
		Timestamp: time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := musicbrainzStoreDB("rel-x", entry); err != nil {
		t.Fatalf("store: %v", err)
	}

	// Drop the in-memory copy and reload purely from the DB.
	resetMap()
	if err := MusicbrainzLoadCache(); err != nil {
		t.Fatalf("load: %v", err)
	}

	got, ok := musicbrainzReleaseCache["rel-x"]
	if !ok {
		t.Fatal("release not loaded from DB into map")
	}
	if got.Release.Title != "Round Trip" {
		t.Errorf("title = %q, want %q", got.Release.Title, "Round Trip")
	}
	if got.ExpiresAt.Unix() != entry.ExpiresAt.Unix() {
		t.Errorf("expiry not preserved: got %v want %v", got.ExpiresAt, entry.ExpiresAt)
	}
}

func TestMusicbrainzCacheJSONMigration(t *testing.T) {
	dbForCache(t)

	// Point the legacy JSON path at a temp file holding one entry.
	origPath := musicbrainzReleaseCachePath
	t.Cleanup(func() { musicbrainzReleaseCachePath = origPath })
	jsonPath := filepath.Join(t.TempDir(), "mb_releases.json")
	musicbrainzReleaseCachePath = jsonPath

	legacy := map[string]models.CachedMusicBrainzRelease{
		"rel-legacy": {
			Release:   models.MusicBrainzReleaseResponse{ID: "rel-legacy", Title: "From JSON"},
			Timestamp: time.Now(),
			ExpiresAt: time.Now().Add(time.Hour),
		},
	}
	data, _ := json.MarshalIndent(legacy, "", "  ")
	if err := os.WriteFile(jsonPath, data, 0644); err != nil {
		t.Fatalf("write legacy json: %v", err)
	}

	// LoadCache should migrate the JSON entry into the DB and warm the map.
	resetMap()
	if err := MusicbrainzLoadCache(); err != nil {
		t.Fatalf("load: %v", err)
	}

	if got, ok := musicbrainzReleaseCache["rel-legacy"]; !ok || got.Release.Title != "From JSON" {
		t.Errorf("legacy entry not migrated into map: ok=%v %+v", ok, got)
	}
	var count int64
	if err := cacheDB.Model(&models.MusicbrainzReleaseCache{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("DB row count = %d, want 1 after migration", count)
	}
}

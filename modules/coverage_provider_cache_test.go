package modules

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
)

// The provider cache's failure handling: a value that cannot be encoded, a row that
// no longer decodes, a database that has gone away, and a legacy file that cannot be
// read or removed are all logged and swallowed — the caller already has its answer.

const coverageSource = models.ProviderCacheLidarrArtists

func providerRowCount(t *testing.T, source string) int64 {
	t.Helper()
	var n int64
	if err := cacheDB.Model(&models.ProviderCache{}).Where("source = ?", source).Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func TestProviderCachePutManySkipsUnencodableValues(t *testing.T) {
	withMigrationDB(t)

	providerCachePutMany(coverageSource, time.Hour,
		providerCacheItem{key: "bad", value: make(chan int)},
		providerCacheItem{key: "good", value: map[string]string{"a": "b"}},
	)
	if n := providerRowCount(t, coverageSource); n != 1 {
		t.Errorf("rows = %d, want only the encodable entry", n)
	}

	// Nothing encodable: no write at all.
	providerCachePutMany(models.ProviderCacheLidarrAlbums, time.Hour, providerCacheItem{key: "bad", value: func() {}})
	if n := providerRowCount(t, models.ProviderCacheLidarrAlbums); n != 0 {
		t.Errorf("rows = %d, want none", n)
	}
}

func TestProviderCacheRestoreSkipsUndecodableRows(t *testing.T) {
	db := withMigrationDB(t)
	now := time.Now()
	rows := []models.ProviderCache{
		{Source: coverageSource, Key: "ok", Payload: `{"Artist":{"id":1}}`, FetchedAt: now, ExpiresAt: now.Add(time.Hour)},
		{Source: coverageSource, Key: "broken", Payload: `{`, FetchedAt: now, ExpiresAt: now.Add(time.Hour)},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}

	into := map[string]models.CachedLidarrArtistRelease{}
	if err := providerCacheRestore(coverageSource, into); err != nil {
		t.Fatalf("providerCacheRestore: %v", err)
	}
	if _, ok := into["ok"]; !ok || len(into) != 1 {
		t.Errorf("restored = %v, want only the decodable row", into)
	}
}

func TestProviderCacheSurvivesADeadDatabase(t *testing.T) {
	closedCacheDB(t)

	providerCachePut(coverageSource, "k", "v", time.Hour)
	providerCacheDropSource(coverageSource)
	providerCacheDrop(coverageSource, "k")

	if err := providerCacheRestore(coverageSource, map[string]string{}); err == nil {
		t.Error("restoring from a closed database reported success")
	}

	// The import cannot tell whether it already ran, so it must leave the file alone.
	path := filepath.Join(t.TempDir(), "legacy.json")
	if err := os.WriteFile(path, []byte(`{"1":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	providerCacheImportJSON(coverageSource, path, time.Hour, providerCacheDecodeMap[models.CachedLidarrArtistRelease])
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the legacy file was removed although the import could not run: %v", err)
	}
}

func TestProviderCacheDropIgnoresIncompleteKeys(t *testing.T) {
	withMigrationDB(t)
	providerCachePut(coverageSource, "k", "v", time.Hour)

	providerCacheDrop(coverageSource, "")
	providerCacheDrop("", "k")
	if n := providerRowCount(t, coverageSource); n != 1 {
		t.Errorf("rows = %d, want an incomplete key to drop nothing", n)
	}
	providerCacheDrop(coverageSource, "k")
	if n := providerRowCount(t, coverageSource); n != 0 {
		t.Errorf("rows = %d, want the keyed row dropped", n)
	}
}

func TestProviderCacheImportUnreadableAndUnremovablePaths(t *testing.T) {
	withMigrationDB(t)
	decode := providerCacheDecodeMap[models.CachedLidarrArtistRelease]

	// A directory where the file should be: unreadable, and not mistaken for absent.
	dir := filepath.Join(t.TempDir(), "legacy.json")
	if err := os.MkdirAll(filepath.Join(dir, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	providerCacheImportJSON(coverageSource, dir, time.Hour, decode)
	if n := providerRowCount(t, coverageSource); n != 0 {
		t.Errorf("rows = %d, want nothing imported from an unreadable path", n)
	}

	// Once the source has rows, the import only tidies up — and a path it cannot
	// remove (a non-empty directory) is left, not fatal.
	providerCachePut(coverageSource, "1", models.CachedLidarrArtistRelease{}, time.Hour)
	providerCacheImportJSON(coverageSource, dir, time.Hour, decode)
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the unremovable path vanished: %v", err)
	}
}

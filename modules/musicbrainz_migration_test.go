package modules

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/database"
	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
)

// withMigrationDB gives the detection path a database to record into. cacheDB is
// process-global, so it is restored afterwards to keep tests independent.
func withMigrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	orig := cacheDB
	cacheDB = db
	t.Cleanup(func() { cacheDB = orig })
	return db
}

// A merged release is the silent case: MusicBrainz answers 200 and the data is
// perfectly good, so nothing fails. The only evidence is that the payload's id is
// not the id we asked for, and missing it leaves the app keyed on a dead MBID
// indefinitely.
func TestReleaseRedirectIsDetected(t *testing.T) {
	db := withMigrationDB(t)
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(models.MusicBrainzReleaseResponse{
			ID:    "rel-new",
			Title: "In Rainbows",
		})
	})

	got, err := QueryMusicBrainzReleaseData("rel-old", "9.9.9")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "rel-new" {
		t.Fatalf("release id = %q, want rel-new", got.ID)
	}

	var m models.MusicbrainzMigration
	if err := db.Where("old_mb_id = ?", "rel-old").First(&m).Error; err != nil {
		t.Fatalf("no migration recorded: %v", err)
	}
	if m.NewMBID != "rel-new" {
		t.Errorf("new MBID = %q, want rel-new", m.NewMBID)
	}
	if m.Kind != models.MigrationKindRedirect || m.Status != models.MigrationStatusPending {
		t.Errorf("kind/status = %q/%q, want redirect/pending", m.Kind, m.Status)
	}
	if m.EntityType != models.MigrationEntityRelease {
		t.Errorf("entity type = %q, want release", m.EntityType)
	}
	if m.Name != "In Rainbows" {
		t.Errorf("name = %q, want the release title so the review UI is not two bare UUIDs", m.Name)
	}
}

// The caller asked for the old ID and is mid-scan holding a file that claims it.
// Caching under both keys is what keeps that file taggable while the migration is
// still pending — refusing to answer would break tagging to make a point about
// identity.
func TestReleaseRedirectCachesBothKeys(t *testing.T) {
	withMigrationDB(t)
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(models.MusicBrainzReleaseResponse{ID: "rel-new", Title: "Amnesiac"})
	})

	if _, err := QueryMusicBrainzReleaseData("rel-old", "9.9.9"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, id := range []string{"rel-old", "rel-new"} {
		if _, ok := cachedFreshRelease(id); !ok {
			t.Errorf("release not cached under %q", id)
		}
	}
}

// Re-detection is the normal case: every fetch of the old ID sees the same redirect
// until the migration is applied. It must not queue the same move twice.
func TestRedirectIsRecordedOnce(t *testing.T) {
	db := withMigrationDB(t)
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(models.MusicBrainzReleaseResponse{ID: "rel-new", Title: "Hail to the Thief"})
	})

	for i := 0; i < 3; i++ {
		if _, err := QueryMusicBrainzReleaseData("rel-old", "9.9.9"); err != nil {
			t.Fatalf("fetch %d: %v", i, err)
		}
	}

	var n int64
	if err := db.Model(&models.MusicbrainzMigration{}).Where("old_mb_id = ?", "rel-old").Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("recorded %d migrations for one redirect, want 1", n)
	}
}

// A 404 must be distinguishable from an outage. Before this, both arrived as an
// opaque error string and a dead release looked exactly like MusicBrainz being down.
func TestDeletedReleaseIsGoneNotAFailure(t *testing.T) {
	db := withMigrationDB(t)
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Not Found"}`))
	})

	_, err := QueryMusicBrainzReleaseData("rel-dead", "9.9.9")
	if err == nil {
		t.Fatal("expected an error for a 404")
	}
	if !errors.Is(err, ErrEntityGone) {
		t.Errorf("error %v does not unwrap to ErrEntityGone", err)
	}
	entity, mbID, gone := GoneEntity(err)
	if !gone || entity != models.MigrationEntityRelease || mbID != "rel-dead" {
		t.Errorf("GoneEntity = %q/%q/%v, want release/rel-dead/true", entity, mbID, gone)
	}

	var m models.MusicbrainzMigration
	if err := db.Where("old_mb_id = ?", "rel-dead").First(&m).Error; err != nil {
		t.Fatalf("no migration recorded: %v", err)
	}
	if m.Kind != models.MigrationKindDeleted || m.NewMBID != "" {
		t.Errorf("kind/new = %q/%q, want deleted and no target", m.Kind, m.NewMBID)
	}
}

// A 503 is the case that must NOT be mistaken for a deletion: treating an outage as
// "this release is gone" would un-identify a library because MusicBrainz had a bad
// afternoon.
func TestTransientFailureIsNotAMigration(t *testing.T) {
	db := withMigrationDB(t)
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	_, err := QueryMusicBrainzReleaseData("rel-1", "9.9.9")
	if err == nil {
		t.Fatal("expected an error for a 503")
	}
	if errors.Is(err, ErrEntityGone) {
		t.Error("a 503 must not report the release as gone")
	}

	var n int64
	if err := db.Model(&models.MusicbrainzMigration{}).Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("recorded %d migrations for a transient failure, want 0", n)
	}
}

func TestArtistRedirectIsDetected(t *testing.T) {
	db := withMigrationDB(t)
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(models.MusicBrainzArtistLookup{ID: "art-new", Name: "Radiohead"})
	})
	resetArtistLookupCache(t)

	if _, err := GetMusicBrainzArtist("art-old"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var m models.MusicbrainzMigration
	if err := db.Where("old_mb_id = ?", "art-old").First(&m).Error; err != nil {
		t.Fatalf("no migration recorded: %v", err)
	}
	if m.EntityType != models.MigrationEntityArtist || m.NewMBID != "art-new" || m.Name != "Radiohead" {
		t.Errorf("migration = %+v, want an artist redirect to art-new", m)
	}
}

// A deleted artist is recorded even when a cached copy can still be served: the
// cache answers this request, but the deletion is a fact about the collection that
// has to outlive the cache entry.
func TestDeletedArtistIsRecordedBehindTheCache(t *testing.T) {
	db := withMigrationDB(t)
	resetArtistLookupCache(t)

	status := http.StatusOK
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_ = json.NewEncoder(w).Encode(models.MusicBrainzArtistLookup{ID: "art-1", Name: "Boards of Canada"})
	})

	if _, err := GetMusicBrainzArtist("art-1"); err != nil {
		t.Fatalf("warming the cache: %v", err)
	}

	// Expire the entry so the next call goes to the network, which now 404s.
	expireEntityCache(t, models.MBEntityArtist, "art-1")
	status = http.StatusNotFound

	got, err := GetMusicBrainzArtist("art-1")
	if err != nil {
		t.Fatalf("a stale copy should still be served: %v", err)
	}
	if got.Name != "Boards of Canada" {
		t.Errorf("served artist = %+v, want the cached copy", got)
	}

	var m models.MusicbrainzMigration
	if err := db.Where("old_mb_id = ?", "art-1").First(&m).Error; err != nil {
		t.Fatalf("deletion not recorded behind the cache hit: %v", err)
	}
	if m.Kind != models.MigrationKindDeleted {
		t.Errorf("kind = %q, want deleted", m.Kind)
	}
}

// DropCachedRelease is what stops a dead or superseded ID from being re-fetched on
// every sync forever.
func TestDropCachedRelease(t *testing.T) {
	db := withMigrationDB(t)
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(models.MusicBrainzReleaseResponse{ID: "rel-1", Title: "Kid A"})
	})

	if _, err := QueryMusicBrainzReleaseData("rel-1", "9.9.9"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	DropCachedRelease("rel-1")

	if _, ok := cachedFreshRelease("rel-1"); ok {
		t.Error("release still in the in-memory cache")
	}
	var n int64
	if err := db.Model(&models.MusicbrainzReleaseCache{}).Where("mb_id = ?", "rel-1").Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("cache row still present (%d)", n)
	}
}

// A warm lookup cache answers an artist read without going near MusicBrainz, so a
// merge upstream is invisible to it. Expiring the entry first is how a forced pass
// (mirror.refreshOne) makes the request happen — and this is the surviving mechanism
// for finding an artist redirect, now that the discography sync no longer forgets the
// cached copy on every follow toggle.
func TestExpiringAnArtistDetectsARedirectBehindAWarmCache(t *testing.T) {
	db := withMigrationDB(t)
	resetArtistLookupCache(t)

	var hits int
	id := "art-old"
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		_ = json.NewEncoder(w).Encode(models.MusicBrainzArtistLookup{ID: id, Name: "Fever Ray"})
	})

	// Warm the cache with an answer under the requested ID: nothing to detect yet.
	if _, err := GetMusicBrainzArtist("art-old"); err != nil {
		t.Fatalf("warming: %v", err)
	}
	var n int64
	if err := db.Model(&models.MusicbrainzMigration{}).Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("recorded %d migrations before anything moved", n)
	}

	// The artist is merged upstream. A plain lookup would still be served from the
	// cache and would never notice.
	id = "art-new"
	if _, err := GetMusicBrainzArtist("art-old"); err != nil {
		t.Fatalf("cached lookup: %v", err)
	}
	if hits != 1 {
		t.Fatalf("server hit %d times; the second lookup should have been cached", hits)
	}

	// What a forced refresh does: expire, then read. Expiring rather than forgetting
	// keeps the stale copy as a fallback if the re-read then fails.
	MusicbrainzExpireEntity(models.MBEntityArtist, "art-old")
	if _, err := NewMetadataSource().GetArtist("art-old"); err != nil {
		t.Fatalf("forced artist read: %v", err)
	}
	if hits != 2 {
		t.Errorf("server hit %d times; an expired entry must go to the network", hits)
	}

	var m models.MusicbrainzMigration
	if err := db.Where("old_mb_id = ?", "art-old").First(&m).Error; err != nil {
		t.Fatalf("redirect behind a warm cache was not detected: %v", err)
	}
	if m.NewMBID != "art-new" || m.EntityType != models.MigrationEntityArtist {
		t.Errorf("migration = %+v, want an artist redirect to art-new", m)
	}
}

// A discography fetch is the other artist-shaped request, and a 404 there means the
// same thing it means anywhere else.
func TestGoneArtistDiscographyIsRecorded(t *testing.T) {
	db := withMigrationDB(t)
	withMockMB(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	_, complete, err := GetMusicBrainzArtistReleaseGroups("art-dead")
	if err == nil {
		t.Fatal("expected an error for a 404")
	}
	if complete {
		t.Error("a failed discography must never be reported as complete")
	}
	if !errors.Is(err, ErrEntityGone) {
		t.Errorf("error %v does not unwrap to ErrEntityGone", err)
	}

	var m models.MusicbrainzMigration
	if err := db.Where("old_mb_id = ?", "art-dead").First(&m).Error; err != nil {
		t.Fatalf("deletion not recorded: %v", err)
	}
	if m.Kind != models.MigrationKindDeleted {
		t.Errorf("kind = %q, want deleted", m.Kind)
	}
}

func resetArtistLookupCache(t *testing.T) {
	t.Helper()
	resetEntityCache(t)
}

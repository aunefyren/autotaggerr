package database

import (
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TestPinFallbackManagerRunsOnce: an upgraded install keeps the manager its
// unassigned libraries were silently using, and the migration never runs again — a
// library set to "no manager" afterwards is a choice it must not undo.
func TestPinFallbackManagerRunsOnce(t *testing.T) {
	db := testDB(t)
	off := models.Manager{Name: "Off", Type: models.ManagerTypeLidarr, Enabled: false}
	on := models.Manager{Name: "On", Type: models.ManagerTypeLidarr, Enabled: true}
	for _, m := range []*models.Manager{&off, &on} {
		if err := db.Create(m).Error; err != nil {
			t.Fatalf("manager: %v", err)
		}
	}
	unassigned := models.Library{Name: "Default", Path: "/a", Enabled: true}
	assigned := models.Library{Name: "Own", Path: "/b", Enabled: true, ManagerID: &off.ID}
	for _, l := range []*models.Library{&unassigned, &assigned} {
		if err := db.Create(l).Error; err != nil {
			t.Fatalf("library: %v", err)
		}
	}

	if err := ApplyDataMigrations(db); err != nil {
		t.Fatalf("ApplyDataMigrations: %v", err)
	}
	if got := managerOf(t, db, unassigned.ID); got == nil || *got != on.ID {
		t.Fatalf("unassigned library got %v, want the enabled manager %s", got, on.ID)
	}
	if got := managerOf(t, db, assigned.ID); got == nil || *got != off.ID {
		t.Errorf("an assigned library was changed: %v", got)
	}

	// The user now chooses no manager. A second start must leave it alone.
	if err := db.Model(&models.Library{}).Where("id = ?", unassigned.ID).Update("manager_id", nil).Error; err != nil {
		t.Fatal(err)
	}
	if err := ApplyDataMigrations(db); err != nil {
		t.Fatalf("second ApplyDataMigrations: %v", err)
	}
	if got := managerOf(t, db, unassigned.ID); got != nil {
		t.Errorf("the migration ran again and re-assigned %v", got)
	}
}

// TestPinFallbackManagerWithoutManagers: with no enabled manager nothing was ever
// falling back, so nothing is assigned — and the migration is still recorded, so a
// manager created later is never pinned onto libraries left without one on purpose.
func TestPinFallbackManagerWithoutManagers(t *testing.T) {
	db := testDB(t)
	lib := models.Library{Name: "L", Path: "/a", Enabled: true}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatal(err)
	}
	if err := ApplyDataMigrations(db); err != nil {
		t.Fatalf("ApplyDataMigrations: %v", err)
	}
	mgr := models.Manager{Name: "Later", Type: models.ManagerTypeLidarr, Enabled: true}
	if err := db.Create(&mgr).Error; err != nil {
		t.Fatal(err)
	}
	if err := ApplyDataMigrations(db); err != nil {
		t.Fatalf("second ApplyDataMigrations: %v", err)
	}
	if got := managerOf(t, db, lib.ID); got != nil {
		t.Errorf("library was assigned %v", got)
	}
}

func managerOf(t *testing.T, db *gorm.DB, id uuid.UUID) *uuid.UUID {
	t.Helper()
	var l models.Library
	if err := db.First(&l, "id = ?", id).Error; err != nil {
		t.Fatalf("read library: %v", err)
	}
	return l.ManagerID
}

// TestMoveBPMToLibrary: an upgraded install keeps its BPM behaviour. Each library gets
// the settings of the profile that tags it — the first profile for a library with
// none assigned, since that is the one resolveTaggerProfile hands it.
func TestMoveBPMToLibrary(t *testing.T) {
	db := testDB(t)
	// The columns as they were before the move.
	for _, stmt := range []string{
		"ALTER TABLE tagger_profiles ADD COLUMN bpm_write text",
		"ALTER TABLE tagger_profiles ADD COLUMN bpm_min_confidence real",
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatal(err)
		}
	}
	first := models.TaggerProfile{Name: "First", WriteTags: true}
	second := models.TaggerProfile{Name: "Second", WriteTags: true}
	for _, p := range []*models.TaggerProfile{&first, &second} {
		if err := db.Create(p).Error; err != nil {
			t.Fatal(err)
		}
	}
	db.Exec("UPDATE tagger_profiles SET bpm_write = ?, bpm_min_confidence = ? WHERE id = ?", models.BPMWriteFillEmpty, 0.6, first.ID)
	db.Exec("UPDATE tagger_profiles SET bpm_write = ?, bpm_min_confidence = ? WHERE id = ?", models.BPMWriteOverwrite, 0.8, second.ID)
	// Which one is "first" is decided by id order, the rule the resolver uses.
	if second.ID.String() < first.ID.String() {
		first, second = second, first
	}

	none := models.Library{Name: "None", Path: "/a"}
	onSecond := models.Library{Name: "Second", Path: "/b", TaggerProfileID: &second.ID}
	for _, l := range []*models.Library{&none, &onSecond} {
		if err := db.Create(l).Error; err != nil {
			t.Fatal(err)
		}
	}

	if err := ApplyDataMigrations(db); err != nil {
		t.Fatalf("ApplyDataMigrations: %v", err)
	}
	read := func(id uuid.UUID) models.Library {
		var l models.Library
		db.First(&l, "id = ?", id)
		return l
	}
	var want struct {
		mode string
		conf float64
	}
	db.Raw("SELECT bpm_write, bpm_min_confidence FROM tagger_profiles WHERE id = ?", first.ID).Row().Scan(&want.mode, &want.conf)
	if got := read(none.ID); got.BPMWrite != want.mode || got.BPMMinConfidence != want.conf {
		t.Errorf("library without a profile = %q/%v, want the first profile's %q/%v", got.BPMWrite, got.BPMMinConfidence, want.mode, want.conf)
	}
	db.Raw("SELECT bpm_write, bpm_min_confidence FROM tagger_profiles WHERE id = ?", second.ID).Row().Scan(&want.mode, &want.conf)
	if got := read(onSecond.ID); got.BPMWrite != want.mode || got.BPMMinConfidence != want.conf {
		t.Errorf("library on the second profile = %q/%v, want %q/%v", got.BPMWrite, got.BPMMinConfidence, want.mode, want.conf)
	}
}

// TestScopeLidarrCache: Lidarr cache rows keyed by a bare ID are dropped once; rows
// that name their Lidarr, and other providers' rows, are left alone.
func TestScopeLidarrCache(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	rows := []models.ProviderCache{
		{Source: models.ProviderCacheLidarrArtists, Key: "12", Payload: "{}", FetchedAt: now, ExpiresAt: now.Add(time.Hour)},
		{Source: models.ProviderCacheLidarrTrackFiles, Key: "12", Payload: "{}", FetchedAt: now, ExpiresAt: now.Add(time.Hour)},
		{Source: models.ProviderCacheLidarrArtists, Key: "http://lidarr:8686|12", Payload: "{}", FetchedAt: now, ExpiresAt: now.Add(time.Hour)},
		{Source: models.ProviderCachePlexAlbumKeys, Key: "12", Payload: "{}", FetchedAt: now, ExpiresAt: now.Add(time.Hour)},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := ApplyDataMigrations(db); err != nil {
		t.Fatalf("ApplyDataMigrations: %v", err)
	}

	var left []models.ProviderCache
	if err := db.Order("source, key").Find(&left).Error; err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range left {
		got[r.Source+" "+r.Key] = true
	}
	if len(left) != 2 || !got[models.ProviderCacheLidarrArtists+" http://lidarr:8686|12"] || !got[models.ProviderCachePlexAlbumKeys+" 12"] {
		t.Errorf("rows left = %v, want the scoped Lidarr row and the Plex row", got)
	}
}

// TestExpireEditionLists: cached edition lists predate inc=labels, so they are made
// due — and kept, as the stale fallback — while other entity kinds are left alone.
func TestExpireEditionLists(t *testing.T) {
	db := testDB(t)
	later := time.Now().Add(time.Hour)
	rows := []models.MusicbrainzEntityCache{
		{Entity: models.MBEntityEditions, MBID: "rg-1", Payload: "[]", FetchedAt: time.Now(), ExpiresAt: later},
		{Entity: models.MBEntityArtist, MBID: "a-1", Payload: "{}", FetchedAt: time.Now(), ExpiresAt: later},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := ApplyDataMigrations(db); err != nil {
		t.Fatalf("ApplyDataMigrations: %v", err)
	}

	var left []models.MusicbrainzEntityCache
	if err := db.Order("entity").Find(&left).Error; err != nil {
		t.Fatal(err)
	}
	if len(left) != 2 {
		t.Fatalf("rows = %d, want both kept", len(left))
	}
	for _, r := range left {
		expired := r.ExpiresAt.Before(time.Now())
		if want := r.Entity == models.MBEntityEditions; expired != want {
			t.Errorf("%s expired = %v, want %v", r.Entity, expired, want)
		}
	}
}

// TestHashUserAPIKeys: an upgraded install's plaintext key keeps working — moved into
// api_keys as a write key, stored only as its hash — and the plaintext is gone.
func TestHashUserAPIKeys(t *testing.T) {
	db := testDB(t)
	// The column as it was before keys had their own table.
	if err := db.Exec("ALTER TABLE users ADD COLUMN api_key text").Error; err != nil {
		t.Fatal(err)
	}
	withKey := models.User{Username: "admin", Role: models.UserRoleAdmin}
	without := models.User{Username: "other", Role: models.UserRoleAdmin}
	for _, u := range []*models.User{&withKey, &without} {
		if err := db.Create(u).Error; err != nil {
			t.Fatal(err)
		}
	}
	db.Exec("UPDATE users SET api_key = ? WHERE id = ?", "old-plaintext-key", withKey.ID)

	if err := hashUserAPIKeys(db); err != nil {
		t.Fatalf("hashUserAPIKeys: %v", err)
	}

	var keys []models.APIKey
	db.Find(&keys)
	if len(keys) != 1 {
		t.Fatalf("api_keys = %+v, want one row for the one user with a key", keys)
	}
	k := keys[0]
	if k.UserID != withKey.ID || k.Scope != models.APIKeyScopeWrite || k.Name != "default" ||
		k.Hash != models.HashAPIKey("old-plaintext-key") || k.Prefix != "old-pl" {
		t.Errorf("migrated key = %+v", k)
	}
	var left int64
	db.Raw("SELECT count(*) FROM users WHERE api_key IS NOT NULL").Scan(&left)
	if left != 0 {
		t.Errorf("%d plaintext key(s) left in users", left)
	}
}

// TestHashUserAPIKeysOnAFreshDatabase: no old column, nothing to do.
func TestHashUserAPIKeysOnAFreshDatabase(t *testing.T) {
	if err := hashUserAPIKeys(testDB(t)); err != nil {
		t.Fatalf("hashUserAPIKeys: %v", err)
	}
}

// TestFollowLibrarySets: the single follow library is carried into the set that
// replaced it; a Nil one ("every library") is left as the empty set, which means the same.
func TestFollowLibrarySets(t *testing.T) {
	db := testDB(t)
	if err := db.Exec("ALTER TABLE collection_artists ADD COLUMN follow_library_id text").Error; err != nil {
		t.Fatal(err)
	}
	lib := uuid.New()
	for _, a := range []models.CollectionArtist{{MBID: "named", Name: "Named"}, {MBID: "every", Name: "Every"}} {
		if err := db.Create(&a).Error; err != nil {
			t.Fatal(err)
		}
	}
	db.Exec("UPDATE collection_artists SET follow_library_id = ? WHERE mb_id = 'named'", lib.String())
	db.Exec("UPDATE collection_artists SET follow_library_id = ? WHERE mb_id = 'every'", uuid.Nil.String())

	if err := followLibrarySets(db); err != nil {
		t.Fatal(err)
	}
	var named, every models.CollectionArtist
	db.Where("mb_id = ?", "named").First(&named)
	db.Where("mb_id = ?", "every").First(&every)
	if len(named.FollowLibraryIDs) != 1 || named.FollowLibraryIDs[0] != lib {
		t.Errorf("named = %v, want the library carried across", named.FollowLibraryIDs)
	}
	if len(every.FollowLibraryIDs) != 0 {
		t.Errorf("every = %v, want empty", every.FollowLibraryIDs)
	}
}

// TestPinFallbackTaggerProfile: a library naming no profile, or one since deleted, is
// given the profile it was being tagged by — the first by id, the resolver's fallback.
// A library naming a real profile keeps it.
func TestPinFallbackTaggerProfile(t *testing.T) {
	db := testDB(t)
	first := models.TaggerProfile{Name: "Default", WriteTags: true}
	second := models.TaggerProfile{Name: "Strict", WriteTags: true}
	for _, p := range []*models.TaggerProfile{&first, &second} {
		if err := db.Create(p).Error; err != nil {
			t.Fatalf("profile: %v", err)
		}
	}
	gone := uuid.New()
	none := models.Library{Name: "None", Path: "/a", Enabled: true}
	dangling := models.Library{Name: "Dangling", Path: "/b", Enabled: true, TaggerProfileID: &gone}
	own := models.Library{Name: "Own", Path: "/c", Enabled: true, TaggerProfileID: &second.ID}
	for _, l := range []*models.Library{&none, &dangling, &own} {
		if err := db.Create(l).Error; err != nil {
			t.Fatalf("library: %v", err)
		}
	}

	if err := ApplyDataMigrations(db); err != nil {
		t.Fatalf("ApplyDataMigrations: %v", err)
	}
	for lib, want := range map[uuid.UUID]uuid.UUID{none.ID: first.ID, dangling.ID: first.ID, own.ID: second.ID} {
		var got models.Library
		if err := db.First(&got, "id = ?", lib).Error; err != nil {
			t.Fatal(err)
		}
		if got.TaggerProfileID == nil || *got.TaggerProfileID != want {
			t.Errorf("%s: tagger profile = %v, want %s", got.Name, got.TaggerProfileID, want)
		}
	}
}

// TestWriteRedundantArtist: every profile stops dropping a redundant ARTIST, once —
// a profile turned back on afterwards keeps the choice on the next start.
func TestWriteRedundantArtist(t *testing.T) {
	db := testDB(t)
	on := models.TaggerProfile{Name: "On", WriteTags: true, IgnoreRedundantContributingArtists: true}
	off := models.TaggerProfile{Name: "Off", WriteTags: true}
	for _, p := range []*models.TaggerProfile{&on, &off} {
		if err := db.Create(p).Error; err != nil {
			t.Fatalf("profile: %v", err)
		}
	}
	ignores := func(id uuid.UUID) bool {
		t.Helper()
		var p models.TaggerProfile
		if err := db.First(&p, "id = ?", id).Error; err != nil {
			t.Fatal(err)
		}
		return p.IgnoreRedundantContributingArtists
	}

	if err := ApplyDataMigrations(db); err != nil {
		t.Fatalf("ApplyDataMigrations: %v", err)
	}
	if ignores(on.ID) || ignores(off.ID) {
		t.Fatalf("a profile still ignores redundant artists: on=%v off=%v", ignores(on.ID), ignores(off.ID))
	}

	if err := db.Model(&models.TaggerProfile{}).Where("id = ?", on.ID).
		Update("ignore_redundant_contributing_artists", true).Error; err != nil {
		t.Fatal(err)
	}
	if err := ApplyDataMigrations(db); err != nil {
		t.Fatalf("second ApplyDataMigrations: %v", err)
	}
	if !ignores(on.ID) {
		t.Error("the migration ran again and undid a profile's choice")
	}
}

// TestDropLibraryItemContentHash: the never-written content_hash column is dropped
// from a database that still has it, and a database without it is left alone.
func TestDropLibraryItemContentHash(t *testing.T) {
	db := testDB(t)
	if err := db.Exec("ALTER TABLE library_items ADD COLUMN content_hash text").Error; err != nil {
		t.Fatal(err)
	}
	if err := dropLibraryItemContentHash(db); err != nil {
		t.Fatal(err)
	}
	if db.Migrator().HasColumn("library_items", "content_hash") {
		t.Error("content_hash is still there")
	}
	if err := dropLibraryItemContentHash(db); err != nil {
		t.Errorf("second run: %v", err)
	}
}

// TestSeedQualityProfilesOnce: the presets are created once, valid, and assigned to
// no library; deleting them all is a choice a later start does not undo.
func TestSeedQualityProfilesOnce(t *testing.T) {
	db := testDB(t)
	lib := models.Library{Name: "Music", Path: "/music", Enabled: true}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatal(err)
	}
	if err := ApplyDataMigrations(db); err != nil {
		t.Fatalf("ApplyDataMigrations: %v", err)
	}
	var profiles []models.QualityProfile
	if err := db.Order("id").Find(&profiles).Error; err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 3 || profiles[0].Name != "Lossless" {
		t.Fatalf("seeded %d profiles: %+v", len(profiles), profiles)
	}
	for _, p := range profiles {
		if err := p.Profile().Validate(); err != nil {
			t.Errorf("%s does not validate after a round trip: %v", p.Name, err)
		}
	}
	var reread models.Library
	db.First(&reread, "id = ?", lib.ID)
	if reread.QualityProfileID != nil {
		t.Error("a library was given a quality profile; upgrading must change nothing")
	}

	if err := db.Where("1 = 1").Delete(&models.QualityProfile{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := ApplyDataMigrations(db); err != nil {
		t.Fatal(err)
	}
	var count int64
	db.Model(&models.QualityProfile{}).Count(&count)
	if count != 0 {
		t.Errorf("the presets came back after being deleted: %d", count)
	}
}

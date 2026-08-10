package database

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// testDB opens a fresh sqlite database in a temp file and migrates the schema.
func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "test.db")
	db, err := Connect(models.DatabaseConfig{Type: "sqlite", DSN: dsn})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return db
}

func count(t *testing.T, db *gorm.DB, model any) int64 {
	t.Helper()
	var n int64
	if err := db.Model(model).Count(&n).Error; err != nil {
		t.Fatalf("count %T: %v", model, err)
	}
	return n
}

func TestConnectMigratesSchema(t *testing.T) {
	db := testDB(t)
	for _, m := range models.AllDBModels() {
		if !db.Migrator().HasTable(m) {
			t.Errorf("expected table for %T after AutoMigrate", m)
		}
	}
}

func TestConnectUnsupportedType(t *testing.T) {
	if _, err := Connect(models.DatabaseConfig{Type: "oracle"}); err == nil {
		t.Fatal("expected error for unsupported database type")
	}
}

// The connection settings below exist because of a production incident: during a
// Lidarr sync, readers (every authenticated request reads the users table, and the UI
// polls) waited on commit locks until one exceeded the driver's 5s timeout and the
// request failed. Under WAL a reader never waits for a writer.

func TestConnectEnablesWAL(t *testing.T) {
	db := testDB(t)

	var mode string
	if err := db.Raw("PRAGMA journal_mode").Scan(&mode).Error; err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Errorf("journal_mode = %q, want wal", mode)
	}

	// The pragma is only useful if the wait it configures is longer than the
	// driver's own 5s default, which is what the failing requests hit.
	var busy int
	if err := db.Raw("PRAGMA busy_timeout").Scan(&busy).Error; err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if busy < 10000 {
		t.Errorf("busy_timeout = %d, want at least 10000", busy)
	}
}

// TestConnectCapsTheConnectionPool: SQLite serialises writers whatever the pool says,
// so an unbounded pool buys no write concurrency and only adds contenders for the
// same lock.
func TestConnectCapsTheConnectionPool(t *testing.T) {
	db := testDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	if got := sqlDB.Stats().MaxOpenConnections; got != sqliteMaxConns {
		t.Errorf("MaxOpenConnections = %d, want %d", got, sqliteMaxConns)
	}
}

// There is deliberately no test asserting "a read is not blocked by a write".
//
// The first version of this file had one — hold a write transaction open, read from
// another goroutine, assert the read returns — and it passed identically with
// journal_mode(DELETE), so it guarded nothing. The reason is that a rollback-journal
// writer does *not* block readers for the life of its transaction: it holds RESERVED
// while writing, which readers tolerate, and only escalates to EXCLUSIVE for the
// commit itself. The window this configuration exists to close is that commit, whose
// length depends on how much is being flushed and how fast the disk is — not
// something to assert against a wall clock in a unit test without making it a flake.
//
// So the pragmas are asserted directly instead, and the reasoning for them lives in
// database.go. A test that passes for the wrong reason is worse than no test: it
// claims a guarantee nobody checked.

// TestSQLiteDSNLeavesACustomDSNAlone: the query string is the escape hatch for
// anyone who needs different settings, so appending to it would break it in exactly
// the case someone reached for it.
func TestSQLiteDSNLeavesACustomDSNAlone(t *testing.T) {
	custom := "config/x.db?_pragma=journal_mode(DELETE)"
	if got := sqliteDSN(models.DatabaseConfig{Type: "sqlite", DSN: custom}); got != custom {
		t.Errorf("sqliteDSN(%q) = %q, want it unchanged", custom, got)
	}

	// A bare path gets the defaults, including on the empty (default-path) case.
	for _, dsn := range []string{"", "config/autotaggerr.db"} {
		got := sqliteDSN(models.DatabaseConfig{Type: "sqlite", DSN: dsn})
		if !strings.Contains(got, "journal_mode%28WAL%29") {
			t.Errorf("sqliteDSN(%q) = %q, want WAL", dsn, got)
		}
	}
}

// TestBoolFieldsPersistFalse guards a GORM footgun: a bool column with a
// `default:true` tag drops a Go false from the INSERT, so a user-chosen false is
// silently overridden. These fields must persist false as false.
func TestBoolFieldsPersistFalse(t *testing.T) {
	db := testDB(t)

	profile := models.TaggerProfile{Name: "NoWrite", WriteTags: false}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("create profile: %v", err)
	}
	lib := models.Library{Name: "Off", Path: "/off", Enabled: false}
	if err := db.Create(&lib).Error; err != nil {
		t.Fatalf("create library: %v", err)
	}

	var gotProfile models.TaggerProfile
	if err := db.First(&gotProfile, profile.ID).Error; err != nil {
		t.Fatalf("load profile: %v", err)
	}
	if gotProfile.WriteTags {
		t.Error("WriteTags=false was overridden to true on persist")
	}

	var gotLib models.Library
	if err := db.First(&gotLib, lib.ID).Error; err != nil {
		t.Fatalf("load library: %v", err)
	}
	if gotLib.Enabled {
		t.Error("Enabled=false was overridden to true on persist")
	}
}

func TestSeedBaseline(t *testing.T) {
	db := testDB(t)

	creds, err := Seed(db)
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}

	// MusicBrainz (metadata) and the Cover Art Archive (album covers). Both are
	// credential-free, so both are usable the moment they exist; fanart.tv is not
	// seeded because it cannot work without a user's own API key.
	if got := count(t, db, &models.DataSource{}); got != 2 {
		t.Errorf("data sources = %d, want 2", got)
	}
	for _, want := range []string{models.DataSourceTypeMusicBrainz, models.DataSourceTypeCoverArtArchive} {
		var n int64
		db.Model(&models.DataSource{}).Where("type = ? AND enabled = ?", want, true).Count(&n)
		if n != 1 {
			t.Errorf("enabled %q data sources = %d, want 1", want, n)
		}
	}
	if got := count(t, db, &models.TaggerProfile{}); got != 1 {
		t.Errorf("tagger profiles = %d, want 1", got)
	}
	// Seeding never creates a manager: credentials live on the manager row, which is
	// made on Settings -> Managers. config.json used to carry lidarr_* keys for this
	// and they were read exactly once, on the first boot, then ignored forever.
	if got := count(t, db, &models.Manager{}); got != 0 {
		t.Errorf("managers = %d, want 0 — seeding does not create one", got)
	}
	// Nor a library. autotaggerr_libraries went the same way as the lidarr_* keys:
	// folders are added on Settings -> Libraries, where they are also edited.
	if got := count(t, db, &models.Library{}); got != 0 {
		t.Errorf("libraries = %d, want 0 — seeding does not create one", got)
	}

	// The default profile carries the defaults that used to live in config.json.
	var profile models.TaggerProfile
	if err := db.First(&profile).Error; err != nil {
		t.Fatalf("load profile: %v", err)
	}
	if !profile.WriteTags || profile.RemoveValues || profile.CustomArtistDelimiter != " & " ||
		!profile.UseCurrentArtistName || profile.MaxGenres != models.DefaultMaxGenres {
		t.Errorf("default tagger profile is wrong: %+v", profile)
	}

	// Admin credentials returned once, and the stored hash verifies.
	if creds == nil {
		t.Fatal("expected admin credentials on first seed")
	}
	if creds.Username != "admin" || creds.Password == "" || creds.APIKey == "" {
		t.Errorf("incomplete admin credentials: %+v", creds)
	}
	var admin models.User
	if err := db.Where("username = ?", "admin").First(&admin).Error; err != nil {
		t.Fatalf("load admin: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(creds.Password)); err != nil {
		t.Errorf("stored password hash does not verify: %v", err)
	}
	if admin.PasswordHash == creds.Password {
		t.Error("password stored in plaintext")
	}
}

func TestSeedIdempotent(t *testing.T) {
	db := testDB(t)

	if _, err := Seed(db); err != nil {
		t.Fatalf("first Seed: %v", err)
	}
	creds, err := Seed(db)
	if err != nil {
		t.Fatalf("second Seed: %v", err)
	}
	if creds != nil {
		t.Error("second seed should not recreate the admin user")
	}

	if got := count(t, db, &models.DataSource{}); got != 2 {
		t.Errorf("data sources after re-seed = %d, want 2", got)
	}
	if got := count(t, db, &models.TaggerProfile{}); got != 1 {
		t.Errorf("tagger profiles after re-seed = %d, want 1", got)
	}
	if got := count(t, db, &models.Manager{}); got != 0 {
		t.Errorf("managers after re-seed = %d, want 0", got)
	}
	if got := count(t, db, &models.User{}); got != 1 {
		t.Errorf("users after re-seed = %d, want 1", got)
	}
}

// A seed run must not disturb a profile someone has already edited: the defaults are
// for an empty database, not a correction applied on every boot.
func TestSeedLeavesAnEditedProfileAlone(t *testing.T) {
	db := testDB(t)

	edited := models.TaggerProfile{Name: "Mine", WriteTags: true, RemoveValues: true, MaxGenres: 2}
	if err := db.Create(&edited).Error; err != nil {
		t.Fatalf("create profile: %v", err)
	}

	if _, err := Seed(db); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	if got := count(t, db, &models.TaggerProfile{}); got != 1 {
		t.Errorf("tagger profiles = %d, want the one that already existed", got)
	}
	var profile models.TaggerProfile
	if err := db.First(&profile).Error; err != nil {
		t.Fatalf("load profile: %v", err)
	}
	if profile.Name != "Mine" || !profile.RemoveValues || profile.MaxGenres != 2 {
		t.Errorf("seed overwrote an existing profile: %+v", profile)
	}
}

// Credentials and folders set in config.json must not reach the database. They were
// seed-only and are gone from the config struct entirely; a stale copy left in a
// user's config.json is now inert, which is the point of removing them rather than
// merely warning.
func TestLegacyConfigKeysAreInert(t *testing.T) {
	db := testDB(t)

	raw := `{"lidarr_base_url":"https://lidarr.example.com","lidarr_api_key":"key123",` +
		`"autotaggerr_libraries":["/music"],"autotaggerr_remove_values":true}`
	var cfg models.ConfigStruct
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal legacy config: %v", err)
	}

	if _, err := Seed(db); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if got := count(t, db, &models.Manager{}); got != 0 {
		t.Errorf("managers = %d, want 0 — the lidarr_* keys must be inert", got)
	}
	if got := count(t, db, &models.Library{}); got != 0 {
		t.Errorf("libraries = %d, want 0 — autotaggerr_libraries must be inert", got)
	}
	var profile models.TaggerProfile
	if err := db.First(&profile).Error; err != nil {
		t.Fatalf("load profile: %v", err)
	}
	if profile.RemoveValues {
		t.Error("autotaggerr_remove_values reached the tagger profile; it must be inert")
	}
}

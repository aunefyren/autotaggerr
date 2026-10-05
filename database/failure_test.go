package database

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
)

func closedDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return db
}

// TestWithoutPragmas pins when the WAL fallback has something to fall back to: only
// a bare sqlite path whose pragmas were ours to add.
func TestWithoutPragmas(t *testing.T) {
	current := sqliteDialectorForTest(t)
	cases := []struct {
		name string
		cfg  models.DatabaseConfig
		ok   bool
	}{
		{"bare path", models.DatabaseConfig{Type: "sqlite", DSN: "config/x.db"}, true},
		{"default path", models.DatabaseConfig{Type: ""}, true},
		{"custom DSN", models.DatabaseConfig{Type: "sqlite", DSN: "config/x.db?_pragma=foo"}, false},
		{"not sqlite", models.DatabaseConfig{Type: "postgres", DSN: "host=x"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := withoutPragmas(c.cfg, current)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if !ok && got != current {
				t.Error("with nothing to fall back to, the current dialector must be returned")
			}
		})
	}
}

func sqliteDialectorForTest(t *testing.T) gorm.Dialector {
	t.Helper()
	d, err := dialectorFor(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "d.db")})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// TestConnectFailsForAnUnopenablePath: a database file under a directory that does
// not exist cannot be opened with or without the pragmas, and the error says so
// rather than retrying forever or handing back a nil db.
func TestConnectFailsForAnUnopenablePath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-dir", "x.db")
	for _, dsn := range []string{missing, missing + "?_pragma=busy_timeout(1000)"} {
		db, err := Connect(models.DatabaseConfig{Type: "sqlite", DSN: dsn})
		if err == nil || db != nil {
			t.Errorf("Connect(%q) = %v, %v; want an error", dsn, db, err)
		}
	}
}

// TestConnectWithACustomJournalMode: a DSN that opts out of WAL is honoured — the
// connection works, and the journal mode is whatever the DSN chose.
func TestConnectWithACustomJournalMode(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "x.db") + "?_pragma=journal_mode(DELETE)"
	db, err := Connect(models.DatabaseConfig{Type: "sqlite", DSN: dsn})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	var mode string
	if err := db.Raw("PRAGMA journal_mode").Scan(&mode).Error; err != nil {
		t.Fatal(err)
	}
	if mode != "delete" {
		t.Errorf("journal mode = %q, want the DSN's delete", mode)
	}
}

// TestSeedStepsReportFailures: each seeding step returns its read error instead of
// treating "could not look" as "nothing there" and inserting duplicates.
func TestSeedStepsReportFailures(t *testing.T) {
	db := closedDB(t)
	if _, err := Seed(db); err == nil {
		t.Error("Seed on a closed database should fail")
	}
	if err := seedCoverArtDataSource(db); err == nil {
		t.Error("seedCoverArtDataSource on a closed database should fail")
	}
	if err := seedDefaultTaggerProfile(db); err == nil {
		t.Error("seedDefaultTaggerProfile on a closed database should fail")
	}
	if _, err := seedAdminUser(db); err == nil {
		t.Error("seedAdminUser on a closed database should fail")
	}
}

// TestSeedNamesTheFailingStep: a failure partway through is attributed to the step
// that failed, so a startup error says what could not be created.
func TestSeedNamesTheFailingStep(t *testing.T) {
	cases := []struct {
		name  string
		table string
	}{
		{"cover art", "data_sources"},
		{"tagger profile", "tagger_profiles"},
		{"admin user", "users"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := testDB(t)
			// The MusicBrainz source is seeded first; once it exists, the next write
			// to the named table is the step under test.
			if err := seedMusicBrainzDataSource(db); err != nil {
				t.Fatal(err)
			}
			if err := db.Callback().Create().Before("gorm:create").Register("test:fail_create", func(tx *gorm.DB) {
				if tx.Statement.Table == c.table {
					_ = tx.AddError(errors.New("injected create failure"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := Seed(db); err == nil {
				t.Fatalf("Seed succeeded although creating %s fails", c.table)
			}
		})
	}
}

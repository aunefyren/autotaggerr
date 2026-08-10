// Package database owns the GORM connection and schema for Autotaggerr's domain
// data (managers, data sources, libraries, tagger profiles, the correlation
// index, the MB cache, and users). The dialector is selected from bootstrap
// config so the store can move from sqlite to postgres/mysql later without
// touching call sites.
package database

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// defaultSQLiteDSN is used when the sqlite driver is selected but no DSN is set.
const defaultSQLiteDSN = "config/autotaggerr.db"

// sqlitePragmas are appended to a bare sqlite DSN. Each one is here for a failure
// that actually happened.
//
//   - journal_mode(WAL): in the default rollback-journal mode a committing writer
//     takes an EXCLUSIVE lock, and readers wait it out. (Not for the whole
//     transaction — a writer holds RESERVED while it works, which readers tolerate —
//     but for each commit.) This app writes in long bursts: a Lidarr sync commits per
//     album and ends in a collection Rebuild, which is deliberately one large
//     transaction and so one large commit. Meanwhile the UI polls /scan/status and
//     /events, and every authenticated request reads the users table, so readers meet
//     a near-continuous stream of those windows and eventually one exceeds the
//     timeout. Under WAL a reader never waits for a writer at all.
//   - busy_timeout: how long a statement waits for a lock before giving up. The
//     driver already applies 5s of its own (glebarez/go-sqlite runs
//     `pragma BUSY_TIMEOUT(5000)` on every connection); this raises it, because
//     under WAL what is left to wait for is another *writer*, and briefly waiting
//     out a write is always better than failing the request.
//   - synchronous(NORMAL): the standard companion to WAL. Full fsync per commit
//     buys durability against OS-level crashes that NORMAL does not, at a cost per
//     write; NORMAL is still crash-safe for the process dying, which is the failure
//     this app can actually have.
var sqlitePragmas = []string{
	"journal_mode(WAL)",
	"busy_timeout(10000)",
	"synchronous(NORMAL)",
}

// sqliteTxLock makes every transaction the driver opens a `BEGIN IMMEDIATE`, taking the
// write lock up front instead of starting as a read snapshot and upgrading on the first
// write.
//
// It is a driver query parameter rather than a pragma, but it belongs beside them for
// the same reason: it is here for a failure that actually happened. A transaction that
// reads before it writes — `collection.RebuildScoped` and the two other explicit ones —
// takes a read snapshot on its first read, and the first write has to upgrade it. If
// anyone committed in between, SQLite refuses that upgrade with `SQLITE_BUSY_SNAPSHOT`
// (517) *immediately*: `busy_timeout` cannot help, because a stale snapshot is not
// something waiting will fix. The transaction is unsatisfiable and can only be re-run.
//
// Retrying (`collection.retryBusy`) treats the symptom and cannot win reliably — each
// retry takes a fresh snapshot that the competing writer can invalidate again, which is
// a livelock rather than bad luck. `TestRebuildSurvivesAConcurrentWriter` failed on
// essentially every `-race` run before this. Locking immediately removes the upgrade
// entirely: a competing writer now makes this one *wait* on busy_timeout, which is what
// that timeout was always for.
//
// The cost is that a read-only explicit transaction would take the write lock it does
// not need. There are none — all three call sites read then write — and plain reads are
// unaffected, since GORM opens no transaction for them. GORM's own per-write
// transactions (SkipDefaultTransaction is left at its default `false`) become immediate
// too, which is what a write wants regardless.
const sqliteTxLock = "_txlock=immediate"

// sqliteMaxConns caps the connection pool. SQLite permits exactly one writer at a
// time whatever the pool says, so an unbounded pool does not buy write concurrency —
// it manufactures contention for the same lock, and each new connection re-runs the
// pragmas above. A small pool keeps concurrent readers (which WAL does serve) while
// keeping the writers queued in Go rather than in SQLite.
const sqliteMaxConns = 8

// Connect opens the database using the configured dialector and applies the
// schema via AutoMigrate.
func Connect(cfg models.DatabaseConfig) (*gorm.DB, error) {
	dialector, err := dialectorFor(cfg)
	if err != nil {
		return nil, err
	}

	// Log real problems (and slow queries) but not the expected "record not found"
	// that our existence-check reads produce during seeding.
	gormLogger := gormlogger.New(
		log.New(os.Stdout, "", log.LstdFlags),
		gormlogger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  gormlogger.Warn,
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		},
	)

	db, err := gorm.Open(dialector, &gorm.Config{Logger: gormLogger})
	if err != nil {
		// A DSN carrying pragmas can be rejected by the *filesystem* rather than by
		// SQLite: WAL needs shared memory, which network mounts (SMB/NFS, and some
		// Docker volume drivers) do not provide. That is a supported way to run this
		// app, so it degrades to the old behaviour with a warning rather than
		// refusing to start. Anything else fails as before — retrying a genuinely
		// broken database would only hide the real error.
		plain, ok := withoutPragmas(cfg, dialector)
		if !ok {
			return nil, fmt.Errorf("failed to open database: %w", err)
		}
		logger.Log.Warnf("could not open the database with WAL enabled (%s); falling back to the default journal mode. "+
			"Reads will block during long writes — a local filesystem for the database avoids this.", err.Error())
		if db, err = gorm.Open(plain, &gorm.Config{Logger: gormLogger}); err != nil {
			return nil, fmt.Errorf("failed to open database: %w", err)
		}
	}

	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(sqliteMaxConns)
		// Idle equal to open: a connection dropped from the pool has to re-run the
		// pragmas when it is replaced, and the pool is small enough to keep whole.
		sqlDB.SetMaxIdleConns(sqliteMaxConns)
	}

	// Report what was actually achieved. `PRAGMA journal_mode=WAL` can be *accepted*
	// and still not take effect on a filesystem that cannot support it, in which case
	// the app runs with the contention this configuration exists to remove — and
	// nothing else would ever say so.
	if isSQLite(cfg) {
		var mode string
		if err := db.Raw("PRAGMA journal_mode").Scan(&mode).Error; err != nil {
			logger.Log.Debugf("could not read the sqlite journal mode: %s", err.Error())
		} else if !strings.EqualFold(mode, "wal") {
			logger.Log.Warnf("sqlite journal mode is %q, not WAL — readers will block during long writes.", mode)
		}
	}

	if err := db.AutoMigrate(models.AllDBModels()...); err != nil {
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}

	return db, nil
}

// dialectorFor maps the configured database type to a GORM dialector. Only sqlite
// (pure-Go, CGO-free) is wired today; postgres/mysql slot in here later.
func dialectorFor(cfg models.DatabaseConfig) (gorm.Dialector, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Type)) {
	case "", "sqlite":
		return sqlite.Open(sqliteDSN(cfg)), nil
	default:
		return nil, fmt.Errorf("unsupported database type %q", cfg.Type)
	}
}

func isSQLite(cfg models.DatabaseConfig) bool {
	t := strings.ToLower(strings.TrimSpace(cfg.Type))
	return t == "" || t == "sqlite"
}

// sqliteDSN appends the pragmas above to the configured path.
//
// A DSN that already carries query parameters is left exactly as written: it is the
// escape hatch for anyone who needs a setting this default disagrees with, and
// silently appending to it would make that hatch unreliable in the one case someone
// reached for it.
func sqliteDSN(cfg models.DatabaseConfig) string {
	dsn := strings.TrimSpace(cfg.DSN)
	if dsn == "" {
		dsn = defaultSQLiteDSN
	}
	if strings.Contains(dsn, "?") {
		return dsn
	}
	params := make([]string, 0, len(sqlitePragmas)+1)
	for _, p := range sqlitePragmas {
		params = append(params, "_pragma="+url.QueryEscape(p))
	}
	params = append(params, sqliteTxLock)
	return dsn + "?" + strings.Join(params, "&")
}

// withoutPragmas rebuilds the dialector from the bare path, for the fallback in
// Connect. It reports false when there is nothing to fall back *to* — a non-sqlite
// database, or a DSN the caller wrote themselves, where the pragmas were never ours
// to remove.
func withoutPragmas(cfg models.DatabaseConfig, current gorm.Dialector) (gorm.Dialector, bool) {
	if !isSQLite(cfg) {
		return current, false
	}
	dsn := strings.TrimSpace(cfg.DSN)
	if dsn == "" {
		dsn = defaultSQLiteDSN
	}
	if strings.Contains(dsn, "?") {
		return current, false
	}
	return sqlite.Open(dsn), true
}

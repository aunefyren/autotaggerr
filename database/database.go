// Package database owns the GORM connection and schema for Autotaggerr's domain
// data (managers, data sources, libraries, tagger profiles, the correlation
// index, the MB cache, and users). The dialector is selected from bootstrap
// config so the store can move from sqlite to postgres/mysql later without
// touching call sites.
package database

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// defaultSQLiteDSN is used when the sqlite driver is selected but no DSN is set.
const defaultSQLiteDSN = "config/autotaggerr.db"

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
		return nil, fmt.Errorf("failed to open database: %w", err)
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
		dsn := strings.TrimSpace(cfg.DSN)
		if dsn == "" {
			dsn = defaultSQLiteDSN
		}
		return sqlite.Open(dsn), nil
	default:
		return nil, fmt.Errorf("unsupported database type %q", cfg.Type)
	}
}

package database

import (
	"strings"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MigrateLegacyPlex turns the Plex server an older config.json named into a media
// server row, and the per-library Plex columns into links to it. Run at every startup
// with whatever files.LegacyPlex holds; once the keys are gone from config.json and the
// columns from libraries, it does nothing.
//
// A library is linked unless it had Plex turned off (plex_skip), keeping its section,
// so an upgrade refreshes exactly the albums it refreshed before. Libraries are linked
// only when the row is created here: a row already pointing at the same base URL means
// an earlier boot seeded it (and then failed before clearing the keys), and linking
// again — with plex_skip already dropped — would link the libraries it had skipped.
//
// The columns are dropped afterwards whether or not there were keys: with Plex now
// linked per library, a stale plex_section_key would look like a setting and be one
// nobody reads. Not a data migration (database/migrate.go) because it needs the config
// file's keys, which only main has.
func MigrateLegacyPlex(db *gorm.DB, baseURL, token string) error {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	token = strings.TrimSpace(token)
	return db.Transaction(func(tx *gorm.DB) error {
		migrator := tx.Migrator()
		hasSection := migrator.HasColumn(&models.Library{}, "plex_section_key")
		hasSkip := migrator.HasColumn(&models.Library{}, "plex_skip")

		switch {
		case baseURL != "" && token != "":
			server, created, err := legacyPlexServer(tx, baseURL, token)
			if err != nil {
				return err
			}
			if created {
				if err := linkLegacyPlexLibraries(tx, server.ID, hasSection, hasSkip); err != nil {
					return err
				}
			}
		case baseURL != "" || token != "":
			// The old client needed both; one alone never refreshed anything.
			logger.Log.Warnf("config.json named only half a Plex server (base URL or token); it is dropped — add the server on the Media servers page")
		}

		for _, column := range []string{"plex_section_key", "plex_skip"} {
			if migrator.HasColumn(&models.Library{}, column) {
				// Plain SQL: the migrator drops by schema field, and these are no longer
				// fields on the model. SQLite has had DROP COLUMN since 3.35.
				if err := tx.Exec("ALTER TABLE libraries DROP COLUMN " + column).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func legacyPlexServer(tx *gorm.DB, baseURL, token string) (models.MediaServer, bool, error) {
	var server models.MediaServer
	err := tx.Where("type = ? AND base_url = ?", models.MediaServerTypePlex, baseURL).Limit(1).Find(&server).Error
	if err != nil || server.ID != uuid.Nil {
		return server, false, err
	}
	name := "Plex"
	var taken int64
	tx.Model(&models.MediaServer{}).Where("name = ?", name).Count(&taken)
	if taken > 0 {
		name = "Plex (from config.json)"
	}
	server = models.MediaServer{Name: name, Type: models.MediaServerTypePlex, Enabled: true, BaseURL: baseURL, Token: token}
	if err := tx.Create(&server).Error; err != nil {
		return server, false, err
	}
	logger.Log.Infof("moved the Plex server in config.json to the media server %q", name)
	return server, true, nil
}

func linkLegacyPlexLibraries(tx *gorm.DB, serverID uuid.UUID, hasSection, hasSkip bool) error {
	columns := []string{"id"}
	if hasSection {
		columns = append(columns, "plex_section_key")
	}
	if hasSkip {
		columns = append(columns, "plex_skip")
	}
	var rows []struct {
		ID             uuid.UUID
		PlexSectionKey string
		PlexSkip       bool
	}
	if err := tx.Table("libraries").Select(columns).Scan(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if row.PlexSkip {
			continue
		}
		link := models.LibraryMediaServer{LibraryID: row.ID, MediaServerID: serverID, SectionKey: strings.TrimSpace(row.PlexSectionKey)}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&link).Error; err != nil {
			return err
		}
	}
	return nil
}

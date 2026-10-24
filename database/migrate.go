package database

import (
	"errors"
	"fmt"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ApplyDataMigrations runs each one-time data change not yet recorded in
// data_migrations, then records it. Safe on every startup; a fresh database runs
// them against no rows and records them, so they never reach a later choice.
func ApplyDataMigrations(db *gorm.DB) error {
	for _, m := range []struct {
		name string
		run  func(*gorm.DB) error
	}{
		{"pin_fallback_manager", pinFallbackManager},
		{"move_bpm_to_library", moveBPMToLibrary},
		{"scope_lidarr_cache", scopeLidarrCache},
	} {
		var done models.DataMigration
		err := db.Where("name = ?", m.name).Limit(1).Find(&done).Error
		if err != nil {
			return fmt.Errorf("read data migration %q: %w", m.name, err)
		}
		if done.Name != "" {
			continue
		}
		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := m.run(tx); err != nil {
				return err
			}
			return tx.Create(&models.DataMigration{Name: m.name, AppliedAt: time.Now()}).Error
		}); err != nil {
			return fmt.Errorf("data migration %q: %w", m.name, err)
		}
	}
	return nil
}

// pinFallbackManager assigns each library without a manager the one it was actually
// governed by. Until "no manager" came to mean no manager, such a library silently
// got the first enabled manager, and the form offered it as "Default (first
// configured manager)" — so an install with a Lidarr manager and libraries left on
// the default was running on Lidarr. Taking that away on upgrade would re-correlate
// every file from its embedded tags instead (shouldSkip compares the manager), which
// for Lidarr's MP3s means losing their identity. Pinning keeps them where they are,
// and makes the choice visible where it used to be implicit.
//
// The same rule the old fallback used: enabled managers, first by id.
func pinFallbackManager(db *gorm.DB) error {
	var first models.Manager
	if err := db.Where("enabled = ?", true).Order("id").First(&first).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // nothing was ever falling back to a manager
		}
		return err
	}
	res := db.Model(&models.Library{}).Where("manager_id IS NULL").Update("manager_id", first.ID)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		logger.Log.Infof("assigned manager %q to %d library(ies) that had none and were already using it as the default", first.Name, res.RowsAffected)
	}
	return nil
}

// moveBPMToLibrary copies each tagger profile's BPM settings onto the libraries that
// use it, after the settings moved from the profile to the library. A library with no
// profile assigned is tagged by the first profile by id (components.resolveTaggerProfile),
// so it gets that one's. The old columns are read raw: the fields are gone from the
// model, and on a database created after the move the columns never existed.
func moveBPMToLibrary(db *gorm.DB) error {
	if !db.Migrator().HasColumn("tagger_profiles", "bpm_write") {
		return nil
	}
	type old struct {
		ID               string
		BPMWrite         string
		BPMMinConfidence float64
	}
	var profiles []old
	if err := db.Raw("SELECT id, bpm_write, bpm_min_confidence FROM tagger_profiles ORDER BY id").Scan(&profiles).Error; err != nil {
		return err
	}
	for i, p := range profiles {
		if p.BPMWrite == "" && p.BPMMinConfidence == 0 {
			continue // nothing set; the library default is the same
		}
		set := map[string]any{"bpm_write": p.BPMWrite, "bpm_min_confidence": p.BPMMinConfidence}
		q := db.Model(&models.Library{}).Where("tagger_profile_id = ?", p.ID)
		if i == 0 {
			q = db.Model(&models.Library{}).Where("tagger_profile_id = ? OR tagger_profile_id IS NULL", p.ID)
		}
		if err := q.Updates(set).Error; err != nil {
			return err
		}
	}
	return nil
}

// scopeLidarrCache drops the Lidarr cache rows written before their keys named the
// Lidarr they came from. Those keys are bare Lidarr IDs, which two instances share,
// and nothing reads them any more (modules.LidarrClient.cacheKey); left alone they
// would sit in provider_cache forever, since only a fetch under the same key ever
// overwrites a row. Everything in them is at most an hour old and is re-asked on the
// next lookup.
//
// The key column goes through clause.Like rather than a raw fragment so GORM quotes
// it: `key` is a reserved word in MySQL.
func scopeLidarrCache(db *gorm.DB) error {
	res := db.Where("source IN ?", []string{
		models.ProviderCacheLidarrArtists, models.ProviderCacheLidarrAlbums,
		models.ProviderCacheLidarrTracks, models.ProviderCacheLidarrTrackFiles,
	}).Not(clause.Like{Column: clause.Column{Name: "key"}, Value: "%|%"}).Delete(&models.ProviderCache{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		logger.Log.Infof("dropped %d Lidarr cache row(s) that did not say which Lidarr they came from", res.RowsAffected)
	}
	return nil
}

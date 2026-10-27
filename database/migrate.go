package database

import (
	"errors"
	"fmt"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
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
		{"hash_user_api_keys", hashUserAPIKeys},
		{"seed_authority_owned", seedAuthorityOwned},
		{"unscoped_library_columns", unscopedLibraryColumns},
		{"follow_library_sets", followLibrarySets},
		{"expire_edition_lists", expireEditionLists},
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

// expireEditionLists marks every cached edition list as due. They were fetched
// without inc=labels, so they carry no label or catalog number for the import's
// Browse step to set against the files' tags, and would keep carrying none for up to
// a week. Expired rather than deleted: the payload stays as the stale answer
// GetMusicBrainzReleaseGroupReleases serves when MusicBrainz is down.
func expireEditionLists(db *gorm.DB) error {
	return db.Model(&models.MusicbrainzEntityCache{}).Where("entity = ?", models.MBEntityEditions).
		UpdateColumn("expires_at", time.Unix(0, 0)).Error
}

// seedAuthorityOwned fills the disk counts the catalog comparison now reads
// (models.CollectionReleaseGroup.AuthorityOwned) from the ones it used to read. Until
// the next Rebuild writes them properly, an album would otherwise compare as owning
// nothing and every discrepancy would vanish; copying keeps exactly the answer the
// page gave before, which was right for the single-library installs that predate it.
func seedAuthorityOwned(db *gorm.DB) error {
	return db.Model(&models.CollectionReleaseGroup{}).Where("owned = ?", true).
		UpdateColumns(map[string]any{
			"authority_owned":        gorm.Expr("owned"),
			"authority_owned_tracks": gorm.Expr("owned_tracks"),
		}).Error
}

// hashUserAPIKeys moves each user's single plaintext key (the old users.api_key
// column) into api_keys, hashed, as a write key named "default" — the scope it
// always had, so nothing using it stops working. The plaintext is then cleared:
// leaving it would keep exactly the copy hashing exists to remove. The column is read
// raw because the field is gone from the model, and on a database created after the
// move it never existed.
func hashUserAPIKeys(db *gorm.DB) error {
	if !db.Migrator().HasColumn("users", "api_key") {
		return nil
	}
	var users []struct {
		ID     string
		APIKey string
	}
	if err := db.Raw("SELECT id, api_key FROM users WHERE api_key IS NOT NULL AND api_key <> ''").Scan(&users).Error; err != nil {
		return err
	}
	for _, u := range users {
		userID, err := uuid.Parse(u.ID)
		if err != nil {
			return fmt.Errorf("user %q: %w", u.ID, err)
		}
		prefix := u.APIKey
		if len(prefix) > models.APIKeyPrefixLength {
			prefix = prefix[:models.APIKeyPrefixLength]
		}
		if err := db.Create(&models.APIKey{
			UserID: userID, Name: "default", Scope: models.APIKeyScopeWrite,
			Hash: models.HashAPIKey(u.APIKey), Prefix: prefix,
		}).Error; err != nil {
			return err
		}
	}
	if err := db.Exec("UPDATE users SET api_key = NULL").Error; err != nil {
		return err
	}
	if len(users) > 0 {
		logger.Log.Infof("moved %d API key(s) into api_keys, stored hashed", len(users))
	}
	return nil
}

// unscopedLibraryColumns writes uuid.Nil into the library columns added to wants and
// follow settings, on the rows that predate them. AutoMigrate adds the columns as
// NULL, which reads back as uuid.Nil in Go but matches nothing in a `library_id = ?`
// filter, so a want from before libraries could not be found by the queries that
// look for "not tied to a library". Nil is what every such row means.
func unscopedLibraryColumns(db *gorm.DB) error {
	if err := db.Model(&models.CollectionDesire{}).Where("library_id IS NULL").
		UpdateColumn("library_id", uuid.Nil).Error; err != nil {
		return err
	}
	// The follow column has since been retired for follow_library_ids (see
	// followLibrarySets); a database created after that has nothing here to fill.
	if !db.Migrator().HasColumn("collection_artists", "follow_library_id") {
		return nil
	}
	return db.Table("collection_artists").Where("follow_library_id IS NULL").
		Update("follow_library_id", uuid.Nil).Error
}

// followLibrarySets carries the single library following wanted into
// (follow_library_id) into the set that replaced it (follow_library_ids), when following
// became able to want into several native libraries with one set of settings. A Nil
// library meant "every native library", which an empty set still means, so only named
// ones are copied. The old column is left in place, unread; AutoMigrate never drops
// one, and a database created since never had it.
func followLibrarySets(db *gorm.DB) error {
	if !db.Migrator().HasColumn("collection_artists", "follow_library_id") {
		return nil
	}
	var rows []struct {
		MBID            string `gorm:"column:mb_id"`
		FollowLibraryID string `gorm:"column:follow_library_id"`
	}
	if err := db.Table("collection_artists").Select("mb_id", "follow_library_id").
		Where("follow_library_id IS NOT NULL AND follow_library_id <> ?", uuid.Nil.String()).
		Scan(&rows).Error; err != nil {
		return err
	}
	for _, r := range rows {
		id, err := uuid.Parse(r.FollowLibraryID)
		if err != nil || id == uuid.Nil {
			continue
		}
		if err := db.Model(&models.CollectionArtist{}).Where("mb_id = ?", r.MBID).
			Update("follow_library_ids", models.UUIDSet{id}).Error; err != nil {
			return err
		}
	}
	if len(rows) > 0 {
		logger.Log.Infof("carried %d artist follow(s) into the follow library set", len(rows))
	}
	return nil
}

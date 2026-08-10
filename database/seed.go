package database

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// AdminCredentials carries the plaintext secrets for a freshly created admin
// user. It is returned only when the user is created (never re-derived), so the
// caller can surface the password/API key exactly once.
type AdminCredentials struct {
	Username string
	Password string
	APIKey   string
}

// Seed brings an empty database up to a usable baseline derived from the existing
// config.json, so current (Lidarr) users keep working with zero manual setup. It
// is idempotent: every step no-ops when its rows already exist, so it is safe to
// run on every startup. When it creates the first admin user it returns that
// user's one-time credentials; otherwise it returns nil.
func Seed(db *gorm.DB, cfg models.ConfigStruct) (*AdminCredentials, error) {
	dataSourceID, err := seedMusicBrainzDataSource(db)
	if err != nil {
		return nil, fmt.Errorf("seed data source: %w", err)
	}

	if err := seedCoverArtDataSource(db); err != nil {
		return nil, fmt.Errorf("seed cover art data source: %w", err)
	}

	taggerID, err := seedDefaultTaggerProfile(db, cfg)
	if err != nil {
		return nil, fmt.Errorf("seed tagger profile: %w", err)
	}

	managerID, err := lidarrManagerID(db)
	if err != nil {
		return nil, fmt.Errorf("seed lidarr manager: %w", err)
	}

	if err := seedLibraries(db, cfg, managerID, dataSourceID, taggerID); err != nil {
		return nil, fmt.Errorf("seed libraries: %w", err)
	}

	creds, err := seedAdminUser(db)
	if err != nil {
		return nil, fmt.Errorf("seed admin user: %w", err)
	}

	return creds, nil
}

func seedMusicBrainzDataSource(db *gorm.DB) (*uuid.UUID, error) {
	var existing models.DataSource
	err := db.Where("type = ?", models.DataSourceTypeMusicBrainz).First(&existing).Error
	if err == nil {
		return &existing.ID, nil
	}
	if err != gorm.ErrRecordNotFound {
		return nil, err
	}

	ds := models.DataSource{
		Name:      "MusicBrainz",
		Type:      models.DataSourceTypeMusicBrainz,
		BaseURL:   "https://musicbrainz.org/ws/2",
		RateLimit: 1, // MB allows ~1 req/s
		Enabled:   true,
	}
	if err := db.Create(&ds).Error; err != nil {
		return nil, err
	}
	return &ds.ID, nil
}

// seedCoverArtDataSource enables album covers out of the box. It needs no
// credential and no configuration, and the collection pages are far harder to
// browse without it, so it is on by default — unlike fanart.tv, which cannot work
// until a user supplies their own API key and is therefore left to be added by
// hand. No ID is returned: nothing references artwork the way a library
// references its metadata source.
func seedCoverArtDataSource(db *gorm.DB) error {
	var existing models.DataSource
	err := db.Where("type = ?", models.DataSourceTypeCoverArtArchive).First(&existing).Error
	if err == nil {
		return nil
	}
	if err != gorm.ErrRecordNotFound {
		return err
	}

	return db.Create(&models.DataSource{
		Name:      "Cover Art Archive",
		Type:      models.DataSourceTypeCoverArtArchive,
		BaseURL:   "https://coverartarchive.org",
		RateLimit: 2,
		Enabled:   true,
	}).Error
}

func seedDefaultTaggerProfile(db *gorm.DB, cfg models.ConfigStruct) (*uuid.UUID, error) {
	var count int64
	if err := db.Model(&models.TaggerProfile{}).Count(&count).Error; err != nil {
		return nil, err
	}
	if count > 0 {
		var first models.TaggerProfile
		if err := db.Order("id").First(&first).Error; err != nil {
			return nil, err
		}
		return &first.ID, nil
	}

	profile := models.TaggerProfile{
		Name:                               "Default",
		WriteTags:                          true,
		RemoveValues:                       cfg.AutotaggerrRemoveValues,
		UseCurrentArtistName:               cfg.AutotaggerrUseCurrentArtistName,
		UseCustomArtistDelimiter:           cfg.AutotaggerrUseCustomArtistDelimiter,
		CustomArtistDelimiter:              cfg.AutotaggerrCustomArtistDelimiter,
		CustomArtistDelimiterCommas:        cfg.AutotaggerrCustomArtistDelimiterCommas,
		IgnoreRedundantContributingArtists: cfg.AutotaggerrIgnoreRedundantContributingArtists,
		MaxGenres:                          cfg.AutotaggerrMaxGenres,
		MP3MultiValueTags:                  cfg.AutotaggerrMP3MultiValueTags,
	}
	if err := db.Create(&profile).Error; err != nil {
		return nil, err
	}
	return &profile.ID, nil
}

// lidarrManagerID returns the Lidarr manager's ID so seeded libraries can be linked to
// it, or nil when there is none (libraries are then created unassigned for the user to
// wire up on Settings → Libraries).
//
// It does not create one. config.json used to carry lidarr_base_url, lidarr_api_key and
// lidarr_header_cookie for exactly that, and they were seed-only — read once on the
// first boot and ignored on every boot after, which made editing them look like fixing
// a stale credential while the manager kept using the old value. The manager row is the
// single copy now, created on Settings → Managers.
//
// A fresh install therefore starts with no manager, which is the deliberate trade: one
// place credentials live, rather than two where one silently wins.
func lidarrManagerID(db *gorm.DB) (*uuid.UUID, error) {
	var existing models.Manager
	err := db.Where("type = ?", models.ManagerTypeLidarr).First(&existing).Error
	if err == nil {
		return &existing.ID, nil
	}
	if err != gorm.ErrRecordNotFound {
		return nil, err
	}
	return nil, nil
}

func seedLibraries(db *gorm.DB, cfg models.ConfigStruct, managerID, dataSourceID, taggerID *uuid.UUID) error {
	for _, path := range cfg.AutotaggerrLibraries {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}

		var count int64
		if err := db.Model(&models.Library{}).Where("path = ?", path).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			continue
		}

		library := models.Library{
			Name:            libraryNameFromPath(path),
			Path:            path,
			ManagerID:       managerID,
			DataSourceID:    dataSourceID,
			TaggerProfileID: taggerID,
			Enabled:         true,
			Cron:            cfg.AutotaggerrProcessCronSchedule,
		}
		if err := db.Create(&library).Error; err != nil {
			return err
		}
	}
	return nil
}

// libraryNameFromPath derives a friendly name from a library path, tolerating
// both Windows and Unix separators regardless of the host OS.
func libraryNameFromPath(path string) string {
	trimmed := strings.TrimRight(strings.ReplaceAll(path, "\\", "/"), "/")
	base := filepath.Base(trimmed)
	if base == "" || base == "." || base == "/" {
		return path
	}
	return base
}

// seedAdminUser creates a single admin user with generated credentials when no
// user exists yet. Returns the one-time credentials on creation, nil otherwise.
func seedAdminUser(db *gorm.DB) (*AdminCredentials, error) {
	var count int64
	if err := db.Model(&models.User{}).Count(&count).Error; err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, nil
	}

	password, err := generateToken(18)
	if err != nil {
		return nil, err
	}
	apiKey, err := generateToken(32)
	if err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	user := models.User{
		Username:     "admin",
		PasswordHash: string(hash),
		Role:         models.UserRoleAdmin,
		APIKey:       apiKey,
	}
	if err := db.Create(&user).Error; err != nil {
		return nil, err
	}

	return &AdminCredentials{Username: user.Username, Password: password, APIKey: apiKey}, nil
}

// generateToken returns a URL-safe random string derived from n random bytes.
func generateToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

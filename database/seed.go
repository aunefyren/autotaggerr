package database

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/aunefyren/autotaggerr/logger"
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

	managerID, err := seedLidarrManager(db, cfg)
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

// seedLidarrManager creates a Lidarr manager from config when Lidarr is
// configured. Returns the manager ID to link libraries to, or nil when Lidarr is
// not configured (libraries are then created unassigned for the user to wire up).
func seedLidarrManager(db *gorm.DB, cfg models.ConfigStruct) (*uuid.UUID, error) {
	var existing models.Manager
	err := db.Where("type = ?", models.ManagerTypeLidarr).First(&existing).Error
	if err == nil {
		warnLidarrConfigIgnored(cfg, existing)
		return &existing.ID, nil
	}
	if err != gorm.ErrRecordNotFound {
		return nil, err
	}

	if strings.TrimSpace(cfg.LidarrBaseURL) == "" || strings.TrimSpace(cfg.LidarrAPIKey) == "" {
		return nil, nil // Lidarr not configured
	}

	manager := models.Manager{
		Name:               "Lidarr",
		Type:               models.ManagerTypeLidarr,
		Enabled:            true,
		LidarrBaseURL:      cfg.LidarrBaseURL,
		LidarrAPIKey:       cfg.LidarrAPIKey,
		LidarrHeaderCookie: cfg.LidarrHeaderCookie,
	}
	if err := db.Create(&manager).Error; err != nil {
		return nil, err
	}
	return &manager.ID, nil
}

// warnLidarrConfigIgnored reports config.json's three lidarr_* keys as ignored once
// the manager row they seeded exists.
//
// They are seed-only: seedLidarrManager returns on the row it finds and never reads
// them again, so from the second boot onwards the manager's own credentials are the
// only ones anything uses. That is documented, but a key that silently does nothing
// is still a trap — editing lidarr_header_cookie there when a session expires looks
// exactly like fixing it, and the manager keeps using the stale value.
//
// Only a *divergence* is worth a line. Values identical to the manager's are the
// historical seed sitting where it was left, and warning about them every boot would
// train the user to ignore the message that matters. The key is named but never its
// value: a cookie or an API key does not belong in a log.
func warnLidarrConfigIgnored(cfg models.ConfigStruct, manager models.Manager) {
	ignored := []string{}
	for _, key := range []struct{ name, configured, active string }{
		{"lidarr_base_url", cfg.LidarrBaseURL, manager.LidarrBaseURL},
		{"lidarr_api_key", cfg.LidarrAPIKey, manager.LidarrAPIKey},
		{"lidarr_header_cookie", cfg.LidarrHeaderCookie, manager.LidarrHeaderCookie},
	} {
		if strings.TrimSpace(key.configured) != "" && key.configured != key.active {
			ignored = append(ignored, key.name)
		}
	}
	if len(ignored) == 0 {
		return
	}

	logger.Log.Warnf("config.json sets %s to a value the %q manager does not use — these keys only seed the manager on first run; edit the credentials on the manager itself (Settings → Managers) or they will keep being ignored",
		strings.Join(ignored, ", "), manager.Name)
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

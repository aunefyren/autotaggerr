package database

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"github.com/aunefyren/autotaggerr/models"
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

// Seed brings an empty database up to a usable baseline: the metadata and artwork
// sources everyone needs, a default tagger profile, and an admin to sign in as. It
// is idempotent — every step no-ops when its rows already exist, so it is safe to
// run on every startup. When it creates the first admin user it returns that
// user's one-time credentials; otherwise it returns nil.
//
// It takes no config. It used to read config.json for the library folders and the
// tag-writing flags, and those keys are gone for the reason the lidarr_* ones went
// before them: they were read once, on the first boot after the database existed,
// and ignored on every boot after — which made editing them look like configuration
// while the database row quietly won. Libraries are added on Settings → Libraries and
// the profile's defaults live here.
func Seed(db *gorm.DB) (*AdminCredentials, error) {
	if err := seedMusicBrainzDataSource(db); err != nil {
		return nil, fmt.Errorf("seed data source: %w", err)
	}

	if err := seedCoverArtDataSource(db); err != nil {
		return nil, fmt.Errorf("seed cover art data source: %w", err)
	}

	if err := seedDefaultTaggerProfile(db); err != nil {
		return nil, fmt.Errorf("seed tagger profile: %w", err)
	}

	creds, err := seedAdminUser(db)
	if err != nil {
		return nil, fmt.Errorf("seed admin user: %w", err)
	}

	return creds, nil
}

func seedMusicBrainzDataSource(db *gorm.DB) error {
	var existing models.DataSource
	err := db.Where("type = ?", models.DataSourceTypeMusicBrainz).First(&existing).Error
	if err == nil {
		return nil
	}
	if err != gorm.ErrRecordNotFound {
		return err
	}

	return db.Create(&models.DataSource{
		Name:      "MusicBrainz",
		Type:      models.DataSourceTypeMusicBrainz,
		BaseURL:   "https://musicbrainz.org/ws/2",
		RateLimit: 1, // MB allows ~1 req/s
		Enabled:   true,
	}).Error
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

// seedDefaultTaggerProfile creates the profile a library is given when nobody has
// said otherwise. Its defaults are written here rather than read from config.json:
// the eight autotaggerr_* tag-writing keys that used to supply them are gone, and a
// default that lives in one place cannot disagree with itself.
func seedDefaultTaggerProfile(db *gorm.DB) error {
	var count int64
	if err := db.Model(&models.TaggerProfile{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	return db.Create(&models.TaggerProfile{
		Name:                               "Default",
		WriteTags:                          true,
		RemoveValues:                       false,
		UseCurrentArtistName:               true,
		UseCustomArtistDelimiter:           true,
		CustomArtistDelimiter:              " & ",
		CustomArtistDelimiterCommas:        true,
		IgnoreRedundantContributingArtists: true,
		MaxGenres:                          models.DefaultMaxGenres,
		MP3MultiValueTags:                  false,
	}).Error
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

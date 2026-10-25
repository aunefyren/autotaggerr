package auth

import (
	"fmt"
	"strings"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// CreateAPIKey mints a key for a user and stores its hash. The plaintext is returned
// once, here, and is not recoverable afterwards.
func CreateAPIKey(db *gorm.DB, userID uuid.UUID, name, scope string) (string, models.APIKey, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", models.APIKey{}, fmt.Errorf("a key needs a name")
	}
	if !models.ValidAPIKeyScope(scope) {
		return "", models.APIKey{}, fmt.Errorf("scope must be %q or %q", models.APIKeyScopeRead, models.APIKeyScopeWrite)
	}
	key, err := NewAPIKey()
	if err != nil {
		return "", models.APIKey{}, err
	}
	row := models.APIKey{
		UserID: userID, Name: name, Scope: scope,
		Hash: models.HashAPIKey(key), Prefix: key[:models.APIKeyPrefixLength],
	}
	if err := db.Create(&row).Error; err != nil {
		return "", models.APIKey{}, err
	}
	return key, row, nil
}

// NewAPIKey returns a fresh random API key: 32 random bytes, URL-safe base64.
func NewAPIKey() (string, error) { return randomToken() }

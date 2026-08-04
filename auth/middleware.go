package auth

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const contextUserKey = "auth.user"

// errUnauthenticated marks the failures that are genuinely about the *credential*:
// none supplied, one that does not parse, or one naming a user who does not exist.
//
// Everything else — a database that cannot be read right now — is not an
// authentication result at all, and the distinction is the whole point of this
// sentinel. Before it, `userFromRequest` returned one undifferentiated error and the
// middleware called all of them 401. A SQLITE_BUSY during a long write (a Lidarr
// sync, whose final Rebuild is deliberately one big transaction) therefore reported
// "your credential is bad" for what was really "ask me again in a second" — and the
// web UI clears the session on any 401, so a lock wait logged the user out.
var errUnauthenticated = errors.New("unauthenticated")

// Middleware authenticates each request by either a Bearer session token or an
// X-Api-Key header, resolves the user from the database, and stores it in the
// request context. A future OAuth flow issues the same Bearer token, so this
// layer never needs to change.
//
// It answers 401 only when the credential is at fault, and 503 when the lookup could
// not be performed. Saying 401 for an infrastructure failure is a claim about the
// caller's identity that the server has not actually established, and clients act on
// that claim by discarding a session that was never invalid.
func Middleware(db *gorm.DB, signingKey []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, err := userFromRequest(c, db, signingKey)
		switch {
		case err == nil:
		case errors.Is(err, errUnauthenticated):
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		default:
			// Logged because it is the only record that the request failed for a
			// reason no client can see or fix, and it is invisible in an access log
			// showing a plain 503.
			logger.Log.Warnf("could not authenticate a request: %s", err.Error())
			c.Header("Retry-After", "1")
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error": "could not verify the session right now — the database is busy. Retry in a moment.",
			})
			return
		}
		c.Set(contextUserKey, user)
		c.Next()
	}
}

func userFromRequest(c *gin.Context, db *gorm.DB, signingKey []byte) (models.User, error) {
	var user models.User

	// API key — programmatic access.
	if apiKey := strings.TrimSpace(c.GetHeader("X-Api-Key")); apiKey != "" {
		if err := db.Where("api_key = ?", apiKey).First(&user).Error; err != nil {
			return user, credentialError(err)
		}
		return user, nil
	}

	// Bearer session token — interactive/UI access.
	if raw, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer "); ok {
		claims, err := ParseToken(strings.TrimSpace(raw), signingKey)
		if err != nil {
			return user, fmt.Errorf("%w: %v", errUnauthenticated, err)
		}
		if err := db.Where("id = ?", claims.UserID).First(&user).Error; err != nil {
			return user, credentialError(err)
		}
		return user, nil
	}

	return user, fmt.Errorf("%w: no credentials provided", errUnauthenticated)
}

// credentialError classifies a user lookup. "No such row" is the credential naming
// nobody — a real 401. Any other error is the store failing to answer, which says
// nothing about the credential and must not be reported as if it did.
func credentialError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("%w: %v", errUnauthenticated, err)
	}
	return err
}

// CurrentUser returns the user stored by Middleware, if any.
func CurrentUser(c *gin.Context) (models.User, bool) {
	v, ok := c.Get(contextUserKey)
	if !ok {
		return models.User{}, false
	}
	u, ok := v.(models.User)
	return u, ok
}

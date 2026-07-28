package auth

import (
	"errors"
	"net/http"
	"strings"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const contextUserKey = "auth.user"

// Middleware authenticates each request by either a Bearer session token or an
// X-Api-Key header, resolves the user from the database, and stores it in the
// request context. A future OAuth flow issues the same Bearer token, so this
// layer never needs to change.
func Middleware(db *gorm.DB, signingKey []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, err := userFromRequest(c, db, signingKey)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
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
			return user, err
		}
		return user, nil
	}

	// Bearer session token — interactive/UI access.
	if raw, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer "); ok {
		claims, err := ParseToken(strings.TrimSpace(raw), signingKey)
		if err != nil {
			return user, err
		}
		if err := db.Where("id = ?", claims.UserID).First(&user).Error; err != nil {
			return user, err
		}
		return user, nil
	}

	return user, errors.New("no credentials provided")
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

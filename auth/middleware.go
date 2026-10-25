package auth

import (
	"context"
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

// Option adjusts what Middleware accepts on the routes it guards.
type Option func(*options)

type options struct {
	bearerAPIKey bool
}

// BearerAPIKey also accepts an API key sent as `Authorization: Bearer <key>`.
//
// Opt-in per route because it exists for one kind of client: MCP clients, many of
// which can be configured with a bearer token but not with an arbitrary header. A
// Bearer value is tried as a session token first; only one that does not parse as a
// token is looked up as a key, so a session keeps working on the same route.
func BearerAPIKey() Option {
	return func(o *options) { o.bearerAPIKey = true }
}

// Middleware authenticates each request by either a Bearer session token or an
// X-Api-Key header, resolves the user from the database, and stores it in the
// request context. A future OAuth flow issues the same Bearer token, so this
// layer never needs to change.
//
// The user is stored twice: on the Gin context for handlers (CurrentUser), and on
// the request's context.Context for code that is handed only that — the MCP tool
// handlers, which never see a *gin.Context (UserFromContext).
//
// It answers 401 only when the credential is at fault, and 503 when the lookup could
// not be performed. Saying 401 for an infrastructure failure is a claim about the
// caller's identity that the server has not actually established, and clients act on
// that claim by discarding a session that was never invalid.
func Middleware(db *gorm.DB, signingKey []byte, opts ...Option) gin.HandlerFunc {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return func(c *gin.Context) {
		user, err := userFromRequest(c, db, signingKey, o)
		switch {
		case err == nil:
		case errors.Is(err, errUnauthenticated):
			// RFC 9110 requires the challenge on a 401, and MCP clients start their
			// auth flow from it. Browsers show no prompt for the Bearer scheme, so the
			// SPA is unaffected.
			c.Header("WWW-Authenticate", `Bearer realm="autotaggerr"`)
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
		c.Request = c.Request.WithContext(WithUser(c.Request.Context(), user))
		c.Next()
	}
}

func userFromRequest(c *gin.Context, db *gorm.DB, signingKey []byte, o options) (models.User, error) {
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
		raw = strings.TrimSpace(raw)
		claims, err := ParseToken(raw, signingKey)
		if err != nil && o.bearerAPIKey && raw != "" {
			if err := db.Where("api_key = ?", raw).First(&user).Error; err != nil {
				return user, credentialError(err)
			}
			return user, nil
		}
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

type userContextKey struct{}

// WithUser returns ctx carrying user, for UserFromContext.
func WithUser(ctx context.Context, user models.User) context.Context {
	return context.WithValue(ctx, userContextKey{}, user)
}

// UserFromContext returns the user Middleware stored on the request's context.
func UserFromContext(ctx context.Context) (models.User, bool) {
	u, ok := ctx.Value(userContextKey{}).(models.User)
	return u, ok
}

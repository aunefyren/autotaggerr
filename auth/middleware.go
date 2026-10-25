package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aunefyren/autotaggerr/logger"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	contextUserKey       = "auth.user"
	contextCredentialKey = "auth.credential"
)

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
	bearerAPIKey    bool
	scopeByEndpoint bool
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

// ScopeCheckedByEndpoint lets a read-scoped key through whatever the HTTP method.
//
// By default a read key is refused on anything but GET/HEAD/OPTIONS, because on the
// REST API the method is what says whether a request writes. The MCP endpoint is all
// POSTs whatever the tool does, so it enforces the scope itself — by which tools a read
// key is shown at all — and opts out of the method rule here.
func ScopeCheckedByEndpoint() Option {
	return func(o *options) { o.scopeByEndpoint = true }
}

// Credential kinds.
const (
	CredentialSession = "session"
	CredentialAPIKey  = "api_key"
)

// Credential is how a request authenticated, beside who it is. Scope is the API key's
// scope; a session is always write, since it is the user at the UI.
type Credential struct {
	Kind  string
	Scope string
	// KeyID is the API key used; zero for a session.
	KeyID uuid.UUID
}

// CanWrite reports whether the credential may change anything.
func (c Credential) CanWrite() bool { return c.Scope == models.APIKeyScopeWrite }

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
		user, cred, err := userFromRequest(c, db, signingKey, o)
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
		if !cred.CanWrite() && !o.scopeByEndpoint && !readOnlyMethod(c.Request.Method) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "this API key is read-only"})
			return
		}
		c.Set(contextUserKey, user)
		c.Set(contextCredentialKey, cred)
		ctx := WithCredential(WithUser(c.Request.Context(), user), cred)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func userFromRequest(c *gin.Context, db *gorm.DB, signingKey []byte, o options) (models.User, Credential, error) {
	// API key — programmatic access.
	if apiKey := strings.TrimSpace(c.GetHeader("X-Api-Key")); apiKey != "" {
		return userFromAPIKey(db, apiKey)
	}

	// Bearer session token — interactive/UI access.
	if raw, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer "); ok {
		raw = strings.TrimSpace(raw)
		claims, err := ParseToken(raw, signingKey)
		if err != nil && o.bearerAPIKey && raw != "" {
			return userFromAPIKey(db, raw)
		}
		if err != nil {
			return models.User{}, Credential{}, fmt.Errorf("%w: %v", errUnauthenticated, err)
		}
		var user models.User
		if err := db.Where("id = ?", claims.UserID).First(&user).Error; err != nil {
			return user, Credential{}, credentialError(err)
		}
		return user, Credential{Kind: CredentialSession, Scope: models.APIKeyScopeWrite}, nil
	}

	return models.User{}, Credential{}, fmt.Errorf("%w: no credentials provided", errUnauthenticated)
}

// userFromAPIKey resolves a key by its hash, then its user.
func userFromAPIKey(db *gorm.DB, key string) (models.User, Credential, error) {
	var user models.User
	var row models.APIKey
	if err := db.Where("hash = ?", models.HashAPIKey(key)).First(&row).Error; err != nil {
		return user, Credential{}, credentialError(err)
	}
	if err := db.Where("id = ?", row.UserID).First(&user).Error; err != nil {
		return user, Credential{}, credentialError(err)
	}
	touchAPIKey(db, row)
	return user, Credential{Kind: CredentialAPIKey, Scope: row.Scope, KeyID: row.ID}, nil
}

// apiKeyTouchInterval is how stale APIKey.LastUsedAt may get. It answers "is this key
// still in use", which needs minutes, not a write on every request.
const apiKeyTouchInterval = 5 * time.Minute

var apiKeyTouched sync.Map // key ID → time.Time of the last write

// touchAPIKey records that a key was used, at most once per interval and off the
// request path: authentication must not wait on a write lock (a long sync holds one
// for seconds), and a lost timestamp costs nothing.
func touchAPIKey(db *gorm.DB, row models.APIKey) {
	now := time.Now()
	if row.LastUsedAt != nil && now.Sub(*row.LastUsedAt) < apiKeyTouchInterval {
		return
	}
	if last, ok := apiKeyTouched.Load(row.ID); ok && now.Sub(last.(time.Time)) < apiKeyTouchInterval {
		return
	}
	apiKeyTouched.Store(row.ID, now)
	go func() {
		if err := db.Model(&models.APIKey{}).Where("id = ?", row.ID).Update("last_used_at", now).Error; err != nil {
			logger.Log.Debugf("could not record API key use: %s", err.Error())
		}
	}()
}

func readOnlyMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
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

type credentialContextKey struct{}

// WithCredential returns ctx carrying cred, for CredentialFromContext.
func WithCredential(ctx context.Context, cred Credential) context.Context {
	return context.WithValue(ctx, credentialContextKey{}, cred)
}

// CredentialFromContext returns how the request authenticated, as Middleware stored it.
func CredentialFromContext(ctx context.Context) (Credential, bool) {
	c, ok := ctx.Value(credentialContextKey{}).(Credential)
	return c, ok
}

// CurrentCredential is CredentialFromContext for a Gin handler.
func CurrentCredential(c *gin.Context) (Credential, bool) {
	v, ok := c.Get(contextCredentialKey)
	if !ok {
		return Credential{}, false
	}
	cred, ok := v.(Credential)
	return cred, ok
}

package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// The middleware is the whole authorization boundary: every protected route in the
// app is protected by exactly this function, so each way in (and each way it must
// refuse) is worth stating as its own case. These live here rather than in routers
// because that is where the code lives — a handler test proves the route is wired,
// not that the boundary holds.

func middlewareUser(t *testing.T, db *gorm.DB) models.User {
	t.Helper()
	hash, err := HashPassword("pw")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	user := models.User{Username: "admin", PasswordHash: hash, Role: models.UserRoleAdmin, APIKey: "api-key-123"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

// guardedRouter mounts one route behind the middleware, reporting which user the
// middleware resolved — so a 200 alone can never pass the test.
func guardedRouter(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/guarded", Middleware(db, testKey), func(c *gin.Context) {
		user, ok := CurrentUser(c)
		if !ok {
			// Reaching the handler without a user in the context would mean the
			// middleware let a request through unauthenticated.
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no user in context"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"username": user.Username, "id": user.ID.String()})
	})
	return r
}

func guardedGet(r *gin.Engine, header, value string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/guarded", nil)
	if header != "" {
		req.Header.Set(header, value)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestMiddlewareAcceptsBearerToken(t *testing.T) {
	db := oidcTestDB(t)
	user := middlewareUser(t, db)
	token, err := IssueToken(user, testKey, DefaultTokenTTL)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	w := guardedGet(guardedRouter(db), "Authorization", "Bearer "+token)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	// The resolved identity matters, not merely that something was resolved.
	if got := w.Body.String(); !strings.Contains(got, user.ID.String()) {
		t.Errorf("body = %s; want the token's own user %s", got, user.ID)
	}
}

func TestMiddlewareAcceptsAPIKey(t *testing.T) {
	db := oidcTestDB(t)
	middlewareUser(t, db)

	w := guardedGet(guardedRouter(db), "X-Api-Key", "api-key-123")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
}

func TestMiddlewareRejectsMissingCredentials(t *testing.T) {
	db := oidcTestDB(t)
	middlewareUser(t, db)

	w := guardedGet(guardedRouter(db), "", "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestMiddlewareRejectsBadCredentials(t *testing.T) {
	db := oidcTestDB(t)
	user := middlewareUser(t, db)
	valid, err := IssueToken(user, testKey, DefaultTokenTTL)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	foreign, err := IssueToken(user, []byte("someone-elses-key"), DefaultTokenTTL)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	cases := []struct {
		name          string
		header, value string
	}{
		{"unknown api key", "X-Api-Key", "not-a-key"},
		{"empty api key falls through to no credentials", "X-Api-Key", ""},
		{"token signed with another key", "Authorization", "Bearer " + foreign},
		{"garbage token", "Authorization", "Bearer not.a.jwt"},
		// A token this service issued, presented without the scheme, is not a
		// credential — accepting a bare value would make the scheme decorative.
		{"token without the Bearer scheme", "Authorization", valid},
		{"wrong scheme", "Authorization", "Basic " + valid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := guardedGet(guardedRouter(db), tc.header, tc.value)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestMiddlewareRejectsTokenForDeletedUser(t *testing.T) {
	db := oidcTestDB(t)
	user := middlewareUser(t, db)
	token, err := IssueToken(user, testKey, DefaultTokenTTL)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	// A validly signed token whose subject no longer exists must not authenticate:
	// the signature proves the token was issued, not that the account survives.
	if err := db.Unscoped().Delete(&user).Error; err != nil {
		t.Fatalf("delete user: %v", err)
	}

	w := guardedGet(guardedRouter(db), "Authorization", "Bearer "+token)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestCurrentUserWithoutMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	// A handler mounted outside the middleware must get a clean "no user" rather
	// than a zero-valued one that reads as a real account.
	if _, ok := CurrentUser(c); ok {
		t.Error("CurrentUser reported a user on a context the middleware never touched")
	}

	// A context key holding the wrong type is a programming error elsewhere; it
	// must still not yield a user.
	c.Set(contextUserKey, "not-a-user")
	if _, ok := CurrentUser(c); ok {
		t.Error("CurrentUser accepted a non-User context value")
	}
}

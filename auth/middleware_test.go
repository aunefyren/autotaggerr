package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
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
	user := models.User{Username: "admin", PasswordHash: hash, Role: models.UserRoleAdmin}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	storeTestKey(t, db, user.ID, "api-key-123", models.APIKeyScopeWrite)
	return user
}

// storeTestKey stores a known key the way CreateAPIKey would, so a test can present it.
func storeTestKey(t *testing.T, db *gorm.DB, userID uuid.UUID, key, scope string) models.APIKey {
	t.Helper()
	row := models.APIKey{UserID: userID, Name: key, Scope: scope, Hash: models.HashAPIKey(key), Prefix: key[:6]}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("store key: %v", err)
	}
	return row
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

// TestMiddlewareReportsAnUnreadableStoreAsUnavailable is the guard for a production
// incident: during a Lidarr sync the database was locked long enough for the user
// lookup to time out (SQLITE_BUSY), the middleware called that 401, and the web UI —
// which clears the session on any 401 — logged the user out mid-sync.
//
// A failure to *check* a credential is not a statement about the credential. The
// store being unreachable is simulated by dropping the table out from under it,
// which produces a driver error rather than "no rows" — the same shape as a lock
// timeout, and the distinction the middleware now has to make.
func TestMiddlewareReportsAnUnreadableStoreAsUnavailable(t *testing.T) {
	db := oidcTestDB(t)
	user := middlewareUser(t, db)
	token, err := IssueToken(user, testKey, DefaultTokenTTL)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if err := db.Migrator().DropTable(&models.User{}); err != nil {
		t.Fatalf("drop users: %v", err)
	}

	for _, tc := range []struct{ name, header, value string }{
		{"bearer token", "Authorization", "Bearer " + token},
		{"api key", "X-Api-Key", "api-key-123"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := guardedGet(guardedRouter(db), tc.header, tc.value)
			if w.Code != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want 503 — a 401 here tells the client to discard a valid session: %s",
					w.Code, w.Body.String())
			}
			// Clients back off on this; without it they retry as fast as they polled.
			if got := w.Header().Get("Retry-After"); got == "" {
				t.Error("no Retry-After on a 503")
			}
		})
	}
}

// TestMiddlewareStillRejectsBadCredentialsWhenTheStoreWorks pins the other half of
// the split: the 503 above must not become a blanket excuse that lets a bad
// credential through as "probably infrastructure".
func TestMiddlewareStillRejectsBadCredentialsWhenTheStoreWorks(t *testing.T) {
	db := oidcTestDB(t)
	middlewareUser(t, db)

	w := guardedGet(guardedRouter(db), "X-Api-Key", "not-a-key")
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

// bearerKeyRouter is guardedRouter with the MCP route's option, reporting the user
// found on the request's context.Context as well as the Gin one — the MCP tool
// handlers only ever see the former.
func bearerKeyRouter(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/guarded", Middleware(db, testKey, BearerAPIKey()), func(c *gin.Context) {
		fromGin, ok := CurrentUser(c)
		fromCtx, ctxOK := UserFromContext(c.Request.Context())
		if !ok || !ctxOK || fromGin.ID != fromCtx.ID {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "user missing from one of the contexts"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"id": fromCtx.ID.String()})
	})
	return r
}

func TestMiddlewareBearerAPIKeyOption(t *testing.T) {
	db := oidcTestDB(t)
	user := middlewareUser(t, db)
	token, err := IssueToken(user, testKey, DefaultTokenTTL)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	r := bearerKeyRouter(db)

	for _, tc := range []struct{ name, header, value string }{
		{"API key as Bearer", "Authorization", "Bearer api-key-123"},
		// A session token still works on the same route: it is tried first.
		{"session token", "Authorization", "Bearer " + token},
		{"X-Api-Key", "X-Api-Key", "api-key-123"},
	} {
		w := guardedGet(r, tc.header, tc.value)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), user.ID.String()) {
			t.Errorf("%s = %d, want 200 as %s: %s", tc.name, w.Code, user.ID, w.Body.String())
		}
	}

	for _, tc := range []struct{ name, value string }{
		{"unknown key", "Bearer not-a-key"},
		{"empty Bearer", "Bearer "},
	} {
		if w := guardedGet(r, "Authorization", tc.value); w.Code != http.StatusUnauthorized {
			t.Errorf("%s = %d, want 401", tc.name, w.Code)
		}
	}

	// Without the option the same header is refused: the default boundary is unmoved.
	if w := guardedGet(guardedRouter(db), "Authorization", "Bearer api-key-123"); w.Code != http.StatusUnauthorized {
		t.Errorf("API key as Bearer without the option = %d, want 401", w.Code)
	}
}

func TestMiddlewareChallengesOn401(t *testing.T) {
	db := oidcTestDB(t)
	middlewareUser(t, db)
	w := guardedGet(guardedRouter(db), "", "")
	if got := w.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer ") {
		t.Errorf("WWW-Authenticate = %q, want a Bearer challenge", got)
	}
}

func TestNewAPIKeyIsFreshEachTime(t *testing.T) {
	a, err := NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	b, _ := NewAPIKey()
	if len(a) < 40 || a == b {
		t.Errorf("keys %q and %q: want two distinct, long keys", a, b)
	}
}

// scopedRouter mounts a GET and a POST behind the middleware, reporting the credential.
func scopedRouter(db *gorm.DB, opts ...Option) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	handler := func(c *gin.Context) {
		cred, ok := CurrentCredential(c)
		fromCtx, ctxOK := CredentialFromContext(c.Request.Context())
		if !ok || !ctxOK || cred != fromCtx {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "credential missing from one of the contexts"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"kind": cred.Kind, "scope": cred.Scope})
	}
	r.GET("/guarded", Middleware(db, testKey, opts...), handler)
	r.POST("/guarded", Middleware(db, testKey, opts...), handler)
	return r
}

func scopedDo(r *gin.Engine, method, header, value string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/guarded", nil)
	req.Header.Set(header, value)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestMiddlewareEnforcesKeyScope: a read key reads and may not write, by method —
// unless the route enforces scope itself, as the MCP endpoint does. A write key and a
// session may do both.
func TestMiddlewareEnforcesKeyScope(t *testing.T) {
	db := oidcTestDB(t)
	user := middlewareUser(t, db) // api-key-123 is a write key
	storeTestKey(t, db, user.ID, "read-key-456", models.APIKeyScopeRead)
	token, err := IssueToken(user, testKey, DefaultTokenTTL)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	r := scopedRouter(db)

	for _, tc := range []struct {
		name, method, header, value string
		want                        int
		body                        string
	}{
		{"read key GET", http.MethodGet, "X-Api-Key", "read-key-456", http.StatusOK, `"scope":"read"`},
		{"read key POST", http.MethodPost, "X-Api-Key", "read-key-456", http.StatusForbidden, "read-only"},
		{"write key POST", http.MethodPost, "X-Api-Key", "api-key-123", http.StatusOK, `"kind":"api_key"`},
		{"session POST", http.MethodPost, "Authorization", "Bearer " + token, http.StatusOK, `"kind":"session"`},
	} {
		w := scopedDo(r, tc.method, tc.header, tc.value)
		if w.Code != tc.want || !strings.Contains(w.Body.String(), tc.body) {
			t.Errorf("%s = %d %s, want %d mentioning %s", tc.name, w.Code, w.Body.String(), tc.want, tc.body)
		}
	}

	byEndpoint := scopedRouter(db, ScopeCheckedByEndpoint())
	if w := scopedDo(byEndpoint, http.MethodPost, "X-Api-Key", "read-key-456"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"scope":"read"`) {
		t.Errorf("read key POST on a scope-by-endpoint route = %d %s, want 200 carrying the read scope", w.Code, w.Body.String())
	}
}

// TestMiddlewareRecordsKeyUse: a key's last use is recorded, off the request path.
func TestMiddlewareRecordsKeyUse(t *testing.T) {
	db := oidcTestDB(t)
	user := middlewareUser(t, db)
	key := storeTestKey(t, db, user.ID, "fresh-key-789", models.APIKeyScopeRead)
	if w := scopedDo(scopedRouter(db), http.MethodGet, "X-Api-Key", "fresh-key-789"); w.Code != http.StatusOK {
		t.Fatalf("GET = %d", w.Code)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		var row models.APIKey
		db.First(&row, "id = ?", key.ID)
		if row.LastUsedAt != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("last_used_at was never recorded")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCreateAPIKey(t *testing.T) {
	db := oidcTestDB(t)
	user := middlewareUser(t, db)
	key, row, err := CreateAPIKey(db, user.ID, " assistant ", models.APIKeyScopeRead)
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	if row.Name != "assistant" || row.Hash != models.HashAPIKey(key) || !strings.HasPrefix(key, row.Prefix) {
		t.Errorf("row = %+v for key %q", row, key)
	}
	if w := scopedDo(scopedRouter(db), http.MethodGet, "X-Api-Key", key); w.Code != http.StatusOK {
		t.Errorf("new key = %d, want it to authenticate", w.Code)
	}
	if _, _, err := CreateAPIKey(db, user.ID, "", models.APIKeyScopeRead); err == nil {
		t.Error("a key with no name was created")
	}
	if _, _, err := CreateAPIKey(db, user.ID, "x", "admin"); err == nil {
		t.Error("a key with an unknown scope was created")
	}
}

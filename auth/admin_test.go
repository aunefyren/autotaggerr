package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// adminRouter mounts RequireAdmin behind a stand-in for Middleware that puts user
// (or nothing, when nil) in the context, and reports IsAdmin from the handler.
func adminRouter(user *models.User) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if user != nil {
			c.Set(contextUserKey, *user)
		}
	})
	r.GET("/admin", RequireAdmin(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"admin": IsAdmin(c)})
	})
	r.GET("/open", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"admin": IsAdmin(c)})
	})
	return r
}

func adminGet(r *gin.Engine, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestRequireAdmin(t *testing.T) {
	admin := models.User{Username: "a", Role: models.UserRoleAdmin}
	plain := models.User{Username: "u", Role: "user"}

	cases := []struct {
		name string
		user *models.User
		code int
	}{
		{"admin passes", &admin, http.StatusOK},
		{"non-admin refused", &plain, http.StatusForbidden},
		// A mis-ordered mount (no Middleware ahead of it) must fail closed.
		{"no user refused", nil, http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := adminGet(adminRouter(c.user), "/admin")
			if w.Code != c.code {
				t.Errorf("GET /admin = %d, want %d: %s", w.Code, c.code, w.Body.String())
			}
		})
	}
}

func TestIsAdmin(t *testing.T) {
	admin := models.User{Role: models.UserRoleAdmin}
	plain := models.User{Role: "user"}
	for name, c := range map[string]struct {
		user *models.User
		want string
	}{
		"admin":     {&admin, `{"admin":true}`},
		"non-admin": {&plain, `{"admin":false}`},
		"anonymous": {nil, `{"admin":false}`},
	} {
		if got := adminGet(adminRouter(c.user), "/open").Body.String(); got != c.want {
			t.Errorf("%s: IsAdmin body = %s, want %s", name, got, c.want)
		}
	}
}

// TestParseTokenRejectsNonHMAC: a token signed with another algorithm — "none" being
// the classic forgery — is refused before its key is ever consulted.
func TestParseTokenRejectsNonHMAC(t *testing.T) {
	forged, err := jwt.NewWithClaims(jwt.SigningMethodNone, &Claims{}).
		SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := ParseToken(forged, []byte("key")); err == nil {
		t.Fatal("ParseToken accepted an unsigned token")
	}
}

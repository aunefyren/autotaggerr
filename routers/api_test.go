package routers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/aunefyren/autotaggerr/artwork"
	"github.com/aunefyren/autotaggerr/auth"
	"github.com/aunefyren/autotaggerr/collection"
	"github.com/aunefyren/autotaggerr/database"
	"github.com/aunefyren/autotaggerr/mirror"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/process"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/aunefyren/autotaggerr/logger"
	"io"
)

func init() {
	// Handlers (via scan/components) log through logger.Log.
	logger.Log = logrus.New()
	logger.Log.SetOutput(io.Discard)
}

func setupAPI(t *testing.T) (*gin.Engine, *API) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	hash, _ := auth.HashPassword("pw")
	if err := db.Create(&models.User{Username: "admin", PasswordHash: hash, Role: "admin", APIKey: "key-123"}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	scanRunner := process.NewRunner(db, nil, models.ConfigStruct{AutotaggerrVersion: "test"})
	api := &API{
		DB:         db,
		Scan:       scanRunner,
		Mirror:     mirror.NewRunner(db, func() bool { return scanRunner.Status().Running }, models.ConfigStruct{}),
		Artwork:    artwork.NewRunner(db, models.ConfigStruct{}),
		Rebuilder:  collection.NewRebuilder(db),
		SigningKey: []byte("signing-key"),
		AppName:    "AT",
		Version:    "test",
	}
	// A rebuild kicked off by a handler runs in the background; without this it can
	// outlive the test and write to a database whose temp directory has been
	// removed, which shows up as unrelated "readonly database" noise.
	t.Cleanup(api.Rebuilder.Quiesce)

	r := gin.New()
	api.Register(r.Group("/api/v1"))
	return r, api
}

func do(r *gin.Engine, method, path, token string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func loginToken(t *testing.T, r *gin.Engine) string {
	t.Helper()
	w := do(r, "POST", "/api/v1/auth/login", "", map[string]string{"username": "admin", "password": "pw"})
	if w.Code != http.StatusOK {
		t.Fatalf("login failed: code %d body %s", w.Code, w.Body.String())
	}
	var resp struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Token == "" {
		t.Fatalf("no token in login response: %s", w.Body.String())
	}
	return resp.Token
}

func TestLoginRejectsBadPassword(t *testing.T) {
	r, _ := setupAPI(t)
	w := do(r, "POST", "/api/v1/auth/login", "", map[string]string{"username": "admin", "password": "nope"})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("bad login code = %d, want 401", w.Code)
	}
}

func TestMeRequiresAuth(t *testing.T) {
	r, _ := setupAPI(t)
	if w := do(r, "GET", "/api/v1/auth/me", "", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("me without token = %d, want 401", w.Code)
	}

	token := loginToken(t, r)
	w := do(r, "GET", "/api/v1/auth/me", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("me with token = %d", w.Code)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"username":"admin"`)) {
		t.Errorf("me response missing username: %s", w.Body.String())
	}
	// The password hash and API key must never be serialized.
	if bytes.Contains(w.Body.Bytes(), []byte("password")) || bytes.Contains(w.Body.Bytes(), []byte("key-123")) {
		t.Errorf("me leaked a secret: %s", w.Body.String())
	}
}

func TestApiKeyAuth(t *testing.T) {
	r, _ := setupAPI(t)
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	req.Header.Set("X-Api-Key", "key-123")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("health via API key = %d, want 200", w.Code)
	}

	// A wrong API key is rejected.
	req = httptest.NewRequest("GET", "/api/v1/health", nil)
	req.Header.Set("X-Api-Key", "bogus")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("health via bad API key = %d, want 401", w.Code)
	}
}

func TestManagerSecretsHidden(t *testing.T) {
	r, api := setupAPI(t)
	m := models.Manager{Name: "L", Type: "lidarr", Enabled: true, LidarrBaseURL: "http://x", LidarrAPIKey: "SECRETKEY", LidarrHeaderCookie: "COOKIEVAL"}
	if err := api.DB.Create(&m).Error; err != nil {
		t.Fatalf("create manager: %v", err)
	}

	token := loginToken(t, r)
	w := do(r, "GET", "/api/v1/managers", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("managers = %d", w.Code)
	}
	if bytes.Contains(w.Body.Bytes(), []byte("SECRETKEY")) || bytes.Contains(w.Body.Bytes(), []byte("COOKIEVAL")) {
		t.Errorf("manager secrets leaked: %s", w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("http://x")) {
		t.Errorf("non-secret base URL missing: %s", w.Body.String())
	}
}

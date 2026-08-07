package routers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/aunefyren/autotaggerr/auth"
	"github.com/aunefyren/autotaggerr/files"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/aunefyren/autotaggerr/settings"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// settingsFixture redirects config.json at a temp dir and seeds a known config, so
// these tests never touch the real ./config.
func settingsFixture(t *testing.T) {
	t.Helper()
	t.Cleanup(files.SetConfigPaths(t.TempDir()))
	previous := files.ConfigFile
	t.Cleanup(func() { files.ConfigFile = previous })
	files.ConfigFile = models.ConfigStruct{
		AutotaggerrName:                "Autotaggerr",
		AutotaggerrPort:                8080,
		AutotaggerrLogLevel:            "info",
		AutotaggerrEnvironment:         "prod",
		AutotaggerrProcessCronSchedule: "0 0 18 * * 7",
		AutotaggerrMirrorCronSchedule:  "0 0 3 * * *",
		AutotaggerrHealthCronSchedule:  "0 */5 * * * *",
		AutotaggerrProcessConcurrency:  4,
		Database:                       models.DatabaseConfig{Type: "sqlite", DSN: "config/autotaggerr.db"},
		PrivateKey:                     "signing-key",
		SMTPPassword:                   "hunter2",
	}
}

// userToken logs in a second, non-admin account so the gate can be tested from both
// sides with the same routes.
func userToken(t *testing.T, r *gin.Engine, db *gorm.DB) string {
	t.Helper()
	hash, _ := auth.HashPassword("pw")
	if err := db.Create(&models.User{Username: "viewer", PasswordHash: hash, Role: "user", APIKey: "key-viewer"}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	w := do(r, "POST", "/api/v1/auth/login", "", map[string]string{"username": "viewer", "password": "pw"})
	if w.Code != http.StatusOK {
		t.Fatalf("viewer login = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return resp.Token
}

func TestSettingsRequireAdmin(t *testing.T) {
	settingsFixture(t)
	r, api := setupAPI(t)
	viewer := userToken(t, r, api.DB)

	for _, call := range []struct{ method, path string }{
		{"GET", "/api/v1/settings"},
		{"PUT", "/api/v1/settings"},
		{"GET", "/api/v1/settings/secrets/smtp_password"},
	} {
		w := do(r, call.method, call.path, viewer, map[string]any{"values": map[string]any{"autotaggerr_port": 9000}})
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s as a non-admin = %d, want 403", call.method, call.path, w.Code)
		}
	}

	// And the setting was not changed by the refused request.
	if files.ConfigFile.AutotaggerrPort != 8080 {
		t.Error("a refused request changed the config anyway")
	}

	// No credential at all is still 401, not 403 — the gate must not swallow the
	// distinction between "who are you" and "not you".
	if w := do(r, "GET", "/api/v1/settings", "", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("anonymous = %d, want 401", w.Code)
	}
}

func TestSettingsGet(t *testing.T) {
	settingsFixture(t)
	r, _ := setupAPI(t)
	tok := loginToken(t, r)

	w := do(r, "GET", "/api/v1/settings", tok, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get = %d: %s", w.Code, w.Body.String())
	}

	var view settings.View
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(view.Sections) == 0 || len(view.Managed) == 0 {
		t.Fatalf("view is empty: %+v", view)
	}
	// The response must not carry any secret's value.
	if strings.Contains(w.Body.String(), "hunter2") || strings.Contains(w.Body.String(), "signing-key") {
		t.Error("the settings response leaked a secret")
	}
}

func TestSettingsUpdate(t *testing.T) {
	settingsFixture(t)
	r, _ := setupAPI(t)
	tok := loginToken(t, r)

	w := do(r, "PUT", "/api/v1/settings", tok, map[string]any{
		"values": map[string]any{"autotaggerr_log_level": "debug", "autotaggerr_port": 9090},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("update = %d: %s", w.Code, w.Body.String())
	}

	var result settings.Result
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Changed) != 2 {
		t.Errorf("changed = %v", result.Changed)
	}
	// A nil Settings runtime (this API has none) still applies process-global effects
	// and reports the rest as needing a restart.
	if len(result.RestartRequired) != 1 || result.RestartRequired[0] != "autotaggerr_port" {
		t.Errorf("restart required = %v", result.RestartRequired)
	}
	if files.ConfigFile.AutotaggerrPort != 9090 {
		t.Error("the running config was not updated")
	}
}

func TestSettingsUpdateRejects(t *testing.T) {
	settingsFixture(t)
	r, _ := setupAPI(t)
	tok := loginToken(t, r)

	// An empty body is a client mistake, not an empty save.
	if w := do(r, "PUT", "/api/v1/settings", tok, map[string]any{"values": map[string]any{}}); w.Code != http.StatusBadRequest {
		t.Errorf("empty values = %d, want 400", w.Code)
	}

	w := do(r, "PUT", "/api/v1/settings", tok, map[string]any{
		"values": map[string]any{"autotaggerr_process_cron_schedule": "nightly please"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad cron = %d, want 400", w.Code)
	}
	// The message has to name the field and say what a valid value looks like.
	if !strings.Contains(w.Body.String(), "Scan schedule") {
		t.Errorf("error %s should name the field", w.Body.String())
	}
}

func TestSettingsRevealSecret(t *testing.T) {
	settingsFixture(t)
	r, _ := setupAPI(t)
	tok := loginToken(t, r)

	w := do(r, "GET", "/api/v1/settings/secrets/smtp_password", tok, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("reveal = %d: %s", w.Code, w.Body.String())
	}
	var resp struct{ Value string }
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Value != "hunter2" {
		t.Errorf("revealed %q", resp.Value)
	}

	// The signing key is never handed back, even to an admin.
	if w := do(r, "GET", "/api/v1/settings/secrets/private_key", tok, nil); w.Code != http.StatusBadRequest {
		t.Errorf("reveal private_key = %d, want 400", w.Code)
	}
}

// TestSendTestEmailRequiresAdmin: the endpoint sends mail from the instance's own
// address, so it sits behind the same gate as the rest of the settings surface.
func TestSendTestEmailRequiresAdmin(t *testing.T) {
	settingsFixture(t)
	r, api := setupAPI(t)
	viewer := userToken(t, r, api.DB)

	if w := do(r, "POST", "/api/v1/settings/email/test", viewer, nil); w.Code != http.StatusForbidden {
		t.Errorf("as a non-admin = %d, want 403", w.Code)
	}
	if w := do(r, "POST", "/api/v1/settings/email/test", "", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("anonymous = %d, want 401", w.Code)
	}
}

// TestSendTestEmailReportsWhy is the feature's whole point: an admin pressing the
// button on a half-configured instance must be told what is missing, in words, not
// handed a generic failure.
func TestSendTestEmailReportsWhy(t *testing.T) {
	settingsFixture(t)
	r, _ := setupAPI(t)
	tok := loginToken(t, r)

	// Nothing configured: the missing recipient is the first thing in the way.
	w := do(r, "POST", "/api/v1/settings/email/test", tok, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unconfigured = %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "test recipient") {
		t.Errorf("error did not name the missing recipient: %s", w.Body.String())
	}

	// With a recipient but no server, the next thing in the way is the host — and
	// the body's address is used when one is given.
	w = do(r, "POST", "/api/v1/settings/email/test", tok, map[string]string{"to": "admin@example.com"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("no host = %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "disabled") && !strings.Contains(w.Body.String(), "host") {
		t.Errorf("error did not name what is missing: %s", w.Body.String())
	}
}

// TestSendTestEmailRefusesOnATestInstanceWithNoSink pins the endpoint's half of the
// test-environment rule: a test instance with nowhere safe to send refuses, rather
// than honouring the address in the request.
//
// The redirect *itself* is asserted in mail (which has a server to deliver to);
// what belongs here is that the API cannot be talked past it — the request names a
// real address and still gets the refusal.
func TestSendTestEmailRefusesOnATestInstanceWithNoSink(t *testing.T) {
	settingsFixture(t)
	files.ConfigFile.AutotaggerrEnvironment = "test"
	files.ConfigFile.AutotaggerrTestEmail = ""
	// Otherwise the send is refused for being unconfigured, and this proves nothing.
	files.ConfigFile.SMTPEnabled = true
	files.ConfigFile.SMTPHost = "127.0.0.1"
	files.ConfigFile.SMTPPort = 1
	files.ConfigFile.SMTPFrom = "autotaggerr@example.com"

	r, _ := setupAPI(t)
	tok := loginToken(t, r)

	w := do(r, "POST", "/api/v1/settings/email/test", tok, map[string]string{"to": "real@example.com"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("= %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "test environment") {
		t.Errorf("error = %s, want a refusal naming the test environment", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "could not reach") {
		t.Error("the send was attempted on a test instance with no test recipient")
	}
}

package routers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aunefyren/autotaggerr/models"
	"gorm.io/gorm"
)

func seedProvider(t *testing.T, db *gorm.DB, enabled bool) models.AuthProvider {
	t.Helper()
	p := models.AuthProvider{
		Name: "Test IdP", Type: models.AuthProviderTypeOIDC, Enabled: enabled,
		Issuer: "https://id.example.com", ClientID: "cid", ClientSecret: "secret",
	}
	if err := db.Create(&p).Error; err != nil {
		t.Fatalf("create provider: %v", err)
	}
	return p
}

// TestLoginProvidersArePublic: the login page must be able to list providers
// before anyone is authenticated, and the response must not leak configuration.
func TestLoginProvidersArePublic(t *testing.T) {
	r, api := setupAPI(t)
	p := seedProvider(t, api.DB, true)

	w := do(r, "GET", "/api/v1/auth/providers", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var got []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0]["name"] != p.Name {
		t.Fatalf("providers = %v", got)
	}
	for _, leaked := range []string{"client_secret", "client_id", "issuer"} {
		if _, ok := got[0][leaked]; ok {
			t.Errorf("public provider list leaked %q", leaked)
		}
	}
}

// TestDisabledProviderIsHiddenAndUnusable: turning a provider off must remove the
// button *and* close the flow, not just hide the UI.
func TestDisabledProviderIsHiddenAndUnusable(t *testing.T) {
	r, api := setupAPI(t)
	p := seedProvider(t, api.DB, false)

	w := do(r, "GET", "/api/v1/auth/providers", "", nil)
	var got []map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(got) != 0 {
		t.Errorf("disabled provider is listed: %v", got)
	}

	w = do(r, "GET", "/api/v1/auth/oidc/"+p.ID.String()+"/start", "", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("start on disabled provider = %d, want 404", w.Code)
	}
}

// TestCallbackWithoutFlowCookieRedirects: a callback with no flow cookie (a forged
// or replayed link) must not authenticate anyone — it bounces back to the login
// page rather than issuing a token.
func TestCallbackWithoutFlowCookieRedirects(t *testing.T) {
	r, api := setupAPI(t)
	p := seedProvider(t, api.DB, true)

	w := do(r, "GET", "/api/v1/auth/oidc/"+p.ID.String()+"/callback?code=x&state=y", "", nil)
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	loc := w.Header().Get("Location")
	if loc == "" || loc[0] != '/' {
		t.Fatalf("Location = %q", loc)
	}
	if containsToken(loc) {
		t.Errorf("callback issued a token without a valid flow: %q", loc)
	}
}

func containsToken(s string) bool {
	for i := 0; i+6 <= len(s); i++ {
		if s[i:i+6] == "token=" {
			return true
		}
	}
	return false
}

// TestAuthProviderCRUDHidesSecret: the admin API must never return client_secret,
// and omitting it on update must keep the stored value.
func TestAuthProviderCRUDHidesSecret(t *testing.T) {
	r, api := setupAPI(t)
	token := loginToken(t, r)

	w := do(r, "POST", "/api/v1/auth-providers", token, map[string]any{
		"name": "IdP", "type": "oidc", "issuer": "https://id.example.com",
		"client_id": "cid", "client_secret": "s3cret",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", w.Code, w.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if _, ok := created["client_secret"]; ok {
		t.Error("create response leaked client_secret")
	}

	id, _ := created["id"].(string)
	w = do(r, "PUT", "/api/v1/auth-providers/"+id, token, map[string]any{"name": "Renamed"})
	if w.Code != http.StatusOK {
		t.Fatalf("update = %d: %s", w.Code, w.Body.String())
	}
	var stored models.AuthProvider
	if err := api.DB.First(&stored, "id = ?", id).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.ClientSecret != "s3cret" {
		t.Errorf("secret was lost on update: %q", stored.ClientSecret)
	}
	if stored.Name != "Renamed" {
		t.Errorf("name = %q", stored.Name)
	}
}

// TestAuthProviderValidation rejects configs that cannot complete a login, at save
// time rather than as a broken button.
func TestAuthProviderValidation(t *testing.T) {
	r, _ := setupAPI(t)
	token := loginToken(t, r)

	cases := []struct {
		name string
		body map[string]any
	}{
		{"no name", map[string]any{"issuer": "https://id.example.com", "client_id": "c", "client_secret": "s"}},
		{"no secret", map[string]any{"name": "X", "issuer": "https://id.example.com", "client_id": "c"}},
		{"relative issuer", map[string]any{"name": "X", "issuer": "id.example.com", "client_id": "c", "client_secret": "s"}},
		{"plaintext issuer", map[string]any{"name": "X", "issuer": "http://id.example.com", "client_id": "c", "client_secret": "s"}},
		{"wrong type", map[string]any{"name": "X", "type": "saml", "issuer": "https://id.example.com", "client_id": "c", "client_secret": "s"}},
	}
	for _, c := range cases {
		w := do(r, "POST", "/api/v1/auth-providers", token, c.body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", c.name, w.Code, w.Body.String())
		}
	}
}

// TestAuthProvidersRequireAuth: provider administration is not public, unlike the
// login-page list.
func TestAuthProvidersRequireAuth(t *testing.T) {
	r, _ := setupAPI(t)
	if w := do(r, "GET", "/api/v1/auth-providers", "", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

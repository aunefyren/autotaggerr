package routers

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/auth"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// OIDC login, end to end, against a fake identity provider.
//
// This is the one flow in the app that cannot be reasoned about from its parts: it
// spans two redirects, a signed flow cookie, PKCE, a code exchange, and JWKS
// signature verification, and every one of those can fail in a way that still looks
// like a login page. So the provider is faked rather than mocked out — real
// discovery document, real JWKS, real RS256 ID token — and the test drives the same
// two requests a browser makes.
//
// It does not replace trying a real IdP (issuers differ in ways no fake predicts),
// but it does mean a break in the plumbing fails here first.

// fakeIdP is an OIDC provider good enough for go-oidc's discovery and verification.
type fakeIdP struct {
	server   *httptest.Server
	key      *rsa.PrivateKey
	clientID string

	// nonce is captured from the authorization request and echoed into the ID token,
	// which is what a conforming provider does.
	nonce string
	// subject, email and emailVerified are what the ID token will claim.
	subject       string
	email         string
	emailVerified bool
	// breakNonce makes the provider return a mismatched nonce, so the replay guard
	// can be exercised.
	breakNonce bool
	// omitIDToken drops the id_token from the exchange response.
	omitIDToken bool
	// exchanges counts token-endpoint calls.
	exchanges int
	// gotVerifier is the PKCE code_verifier presented at the token endpoint.
	gotVerifier string
}

func newFakeIdP(t *testing.T, clientID string) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	idp := &fakeIdP{
		key: key, clientID: clientID,
		subject: "idp-subject-1", email: "user@example.com", emailVerified: true,
	}

	mux := http.NewServeMux()
	idp.server = httptest.NewServer(mux)
	t.Cleanup(idp.server.Close)

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// The issuer must match byte for byte or go-oidc refuses the document — the
		// single most common misconfiguration with a real IdP.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                idp.server.URL,
			"authorization_endpoint":                idp.server.URL + "/authorize",
			"token_endpoint":                        idp.server.URL + "/token",
			"jwks_uri":                              idp.server.URL + "/jwks",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})

	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA",
				"alg": "RS256",
				"use": "sig",
				"kid": "test-key",
				"n":   base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big(key.PublicKey.E)),
			}},
		})
	})

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		idp.exchanges++
		_ = r.ParseForm()
		idp.gotVerifier = r.PostForm.Get("code_verifier")

		response := map[string]any{
			"access_token": "fake-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
		}
		if !idp.omitIDToken {
			nonce := idp.nonce
			if idp.breakNonce {
				nonce = "a-different-nonce"
			}
			response["id_token"] = idp.signIDToken(t, nonce)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	})

	return idp
}

// big renders an RSA public exponent as bytes for the JWK.
func big(e int) []byte {
	out := []byte{}
	for e > 0 {
		out = append([]byte{byte(e & 0xff)}, out...)
		e >>= 8
	}
	return out
}

func (f *fakeIdP) signIDToken(t *testing.T, nonce string) string {
	t.Helper()
	now := time.Now()
	claims := jwt.MapClaims{
		"iss":            f.server.URL,
		"aud":            f.clientID,
		"sub":            f.subject,
		"exp":            now.Add(time.Hour).Unix(),
		"iat":            now.Unix(),
		"nonce":          nonce,
		"email":          f.email,
		"email_verified": f.emailVerified,
		"name":           "Test User",
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "test-key"
	signed, err := token.SignedString(f.key)
	if err != nil {
		t.Fatalf("sign ID token: %v", err)
	}
	return signed
}

// oidcSetup wires an API with one enabled provider pointing at the fake IdP.
func oidcSetup(t *testing.T, allowSignup bool) (*gin.Engine, *API, *fakeIdP, models.AuthProvider) {
	t.Helper()
	// Discovery is memoized by issuer, and each test gets a fresh issuer URL, but a
	// stale entry from a previous test's closed server would be poison.
	auth.ResetProviderCache()
	t.Cleanup(auth.ResetProviderCache)

	r, api := setupAPI(t)
	idp := newFakeIdP(t, "test-client")
	provider := models.AuthProvider{
		Name: "Fake IdP", Type: models.AuthProviderTypeOIDC, Enabled: true,
		Issuer: idp.server.URL, ClientID: idp.clientID, ClientSecret: "test-secret",
		AllowSignup: allowSignup, DefaultRole: models.UserRoleAdmin,
	}
	if err := api.DB.Create(&provider).Error; err != nil {
		t.Fatalf("create provider: %v", err)
	}
	return r, api, idp, provider
}

// startLogin performs the first leg and returns the authorization URL and the flow
// cookie the callback must present.
func startLogin(t *testing.T, r *gin.Engine, providerID string) (*url.URL, *http.Cookie) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/"+providerID+"/start", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("start = %d, want 302: %s", w.Code, w.Body.String())
	}
	location, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	var flow *http.Cookie
	for _, cookie := range w.Result().Cookies() {
		if cookie.Value != "" {
			flow = cookie
		}
	}
	if flow == nil {
		t.Fatal("start set no flow cookie, so the callback can never prove it owns the flow")
	}
	return location, flow
}

func callback(t *testing.T, r *gin.Engine, providerID, query string, flow *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/"+providerID+"/callback?"+query, nil)
	if flow != nil {
		req.AddCookie(flow)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestOIDCStartRedirectsWithPKCEAndNonce(t *testing.T) {
	r, _, idp, provider := oidcSetup(t, true)

	location, flow := startLogin(t, r, provider.ID.String())
	if !strings.HasPrefix(location.String(), idp.server.URL+"/authorize") {
		t.Fatalf("Location = %q, want the provider's authorization endpoint", location)
	}

	q := location.Query()
	if q.Get("client_id") != idp.clientID {
		t.Errorf("client_id = %q, want %q", q.Get("client_id"), idp.clientID)
	}
	if q.Get("response_type") != "code" {
		t.Errorf("response_type = %q, want code", q.Get("response_type"))
	}
	if q.Get("state") == "" {
		t.Error("no state — the callback would have nothing to prove it owns the flow")
	}
	if q.Get("nonce") == "" {
		t.Error("no nonce — an ID token could be replayed from another flow")
	}
	// PKCE is always on: it costs nothing and removes the value of an intercepted
	// authorization code.
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge = %q, method = %q; want an S256 challenge",
			q.Get("code_challenge"), q.Get("code_challenge_method"))
	}
	if !strings.Contains(q.Get("scope"), "openid") {
		t.Errorf("scope = %q, want it to include openid", q.Get("scope"))
	}
	if flow.HttpOnly != true {
		t.Error("the flow cookie is not HttpOnly")
	}
}

// TestOIDCFullLoginCreatesAndAuthenticatesAUser: the whole flow, ending in a session
// token that actually works on a protected route.
func TestOIDCFullLoginCreatesAndAuthenticatesAUser(t *testing.T) {
	r, api, idp, provider := oidcSetup(t, true)

	location, flow := startLogin(t, r, provider.ID.String())
	idp.nonce = location.Query().Get("nonce")

	w := callback(t, r, provider.ID.String(),
		url.Values{"code": {"auth-code"}, "state": {location.Query().Get("state")}}.Encode(), flow)
	if w.Code != http.StatusFound {
		t.Fatalf("callback = %d, want 302: %s", w.Code, w.Body.String())
	}

	// The session token comes back in the URL *fragment*: fragments are never sent to
	// a server, so it stays out of access logs and the Referer header.
	redirect := w.Header().Get("Location")
	fragment := "/login#token="
	if !strings.HasPrefix(redirect, fragment) {
		t.Fatalf("Location = %q, want a token in the fragment of %q", redirect, fragment)
	}
	token, err := url.QueryUnescape(strings.TrimPrefix(redirect, fragment))
	if err != nil {
		t.Fatalf("unescape token: %v", err)
	}

	// The exchange happened, with the PKCE verifier this flow generated.
	if idp.exchanges != 1 {
		t.Errorf("token exchanges = %d, want 1", idp.exchanges)
	}
	if idp.gotVerifier == "" {
		t.Error("no code_verifier was presented, so PKCE was not completed")
	}

	// A user was created and linked to the external subject.
	var user models.User
	if err := api.DB.Where("email = ?", idp.email).First(&user).Error; err != nil {
		t.Fatalf("no user was created for the verified identity: %v", err)
	}

	// And the token it handed back is a real session on a protected route.
	if w := do(r, "GET", "/api/v1/auth/me", token, nil); w.Code != http.StatusOK {
		t.Errorf("GET /auth/me with the issued token = %d, want 200: %s", w.Code, w.Body.String())
	}
}

// TestOIDCLoginLinksAnExistingUser: signup disabled, but a verified email already
// has a local account — that account is used rather than a second one created.
func TestOIDCLoginLinksAnExistingUser(t *testing.T) {
	r, api, idp, provider := oidcSetup(t, false)

	hash, _ := auth.HashPassword("pw")
	existing := models.User{Username: "existing", Email: idp.email, PasswordHash: hash, Role: models.UserRoleAdmin}
	if err := api.DB.Create(&existing).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	location, flow := startLogin(t, r, provider.ID.String())
	idp.nonce = location.Query().Get("nonce")
	w := callback(t, r, provider.ID.String(),
		url.Values{"code": {"c"}, "state": {location.Query().Get("state")}}.Encode(), flow)

	if !strings.HasPrefix(w.Header().Get("Location"), "/login#token=") {
		t.Fatalf("callback did not log the existing user in: %q", w.Header().Get("Location"))
	}
	var count int64
	api.DB.Model(&models.User{}).Where("email = ?", idp.email).Count(&count)
	if count != 1 {
		t.Errorf("users with that email = %d, want 1 — a duplicate account was created", count)
	}
}

// TestOIDCLoginRefusesUnknownIdentityWhenSignupIsOff.
func TestOIDCLoginRefusesUnknownIdentityWhenSignupIsOff(t *testing.T) {
	r, api, idp, provider := oidcSetup(t, false)

	location, flow := startLogin(t, r, provider.ID.String())
	idp.nonce = location.Query().Get("nonce")
	w := callback(t, r, provider.ID.String(),
		url.Values{"code": {"c"}, "state": {location.Query().Get("state")}}.Encode(), flow)

	assertLoginError(t, w)
	var count int64
	api.DB.Model(&models.User{}).Where("email = ?", idp.email).Count(&count)
	if count != 0 {
		t.Error("an account was created even though signup is disabled")
	}
}

// TestOIDCLoginRejectsUnverifiedEmail: linking on an unverified address would let
// anyone who can set their email at the IdP take over a local account.
func TestOIDCLoginRejectsUnverifiedEmail(t *testing.T) {
	r, api, idp, provider := oidcSetup(t, false)
	idp.emailVerified = false

	hash, _ := auth.HashPassword("pw")
	if err := api.DB.Create(&models.User{
		Username: "victim", Email: idp.email, PasswordHash: hash, Role: models.UserRoleAdmin,
	}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	location, flow := startLogin(t, r, provider.ID.String())
	idp.nonce = location.Query().Get("nonce")
	w := callback(t, r, provider.ID.String(),
		url.Values{"code": {"c"}, "state": {location.Query().Get("state")}}.Encode(), flow)

	assertLoginError(t, w)
}

// TestOIDCCallbackRejectsTamperedFlows covers every way the callback can be lied to.
// All of them must land on the login page rather than issuing a session.
func TestOIDCCallbackRejectsTamperedFlows(t *testing.T) {
	t.Run("state mismatch", func(t *testing.T) {
		r, _, idp, provider := oidcSetup(t, true)
		location, flow := startLogin(t, r, provider.ID.String())
		idp.nonce = location.Query().Get("nonce")
		// A callback carrying someone else's state is the CSRF case the state
		// parameter exists to stop.
		w := callback(t, r, provider.ID.String(),
			url.Values{"code": {"c"}, "state": {"not-the-state"}}.Encode(), flow)
		assertLoginError(t, w)
	})

	t.Run("missing flow cookie", func(t *testing.T) {
		r, _, idp, provider := oidcSetup(t, true)
		location, _ := startLogin(t, r, provider.ID.String())
		idp.nonce = location.Query().Get("nonce")
		w := callback(t, r, provider.ID.String(),
			url.Values{"code": {"c"}, "state": {location.Query().Get("state")}}.Encode(), nil)
		assertLoginError(t, w)
	})

	t.Run("flow cookie signed by someone else", func(t *testing.T) {
		r, _, idp, provider := oidcSetup(t, true)
		location, flow := startLogin(t, r, provider.ID.String())
		idp.nonce = location.Query().Get("nonce")
		forged := &http.Cookie{Name: flow.Name, Value: flow.Value[:len(flow.Value)-4] + "AAAA"}
		w := callback(t, r, provider.ID.String(),
			url.Values{"code": {"c"}, "state": {location.Query().Get("state")}}.Encode(), forged)
		assertLoginError(t, w)
	})

	t.Run("nonce mismatch", func(t *testing.T) {
		r, _, idp, provider := oidcSetup(t, true)
		location, flow := startLogin(t, r, provider.ID.String())
		idp.nonce = location.Query().Get("nonce")
		// An ID token minted for a different flow must not be accepted in this one.
		idp.breakNonce = true
		w := callback(t, r, provider.ID.String(),
			url.Values{"code": {"c"}, "state": {location.Query().Get("state")}}.Encode(), flow)
		assertLoginError(t, w)
	})

	t.Run("no id token", func(t *testing.T) {
		r, _, idp, provider := oidcSetup(t, true)
		location, flow := startLogin(t, r, provider.ID.String())
		idp.nonce = location.Query().Get("nonce")
		// An OAuth2-only response is not an OIDC login: without an ID token there is
		// no verified identity, only an access token.
		idp.omitIDToken = true
		w := callback(t, r, provider.ID.String(),
			url.Values{"code": {"c"}, "state": {location.Query().Get("state")}}.Encode(), flow)
		assertLoginError(t, w)
	})

	t.Run("provider reported an error", func(t *testing.T) {
		r, _, _, provider := oidcSetup(t, true)
		location, flow := startLogin(t, r, provider.ID.String())
		w := callback(t, r, provider.ID.String(),
			url.Values{"error": {"access_denied"}, "state": {location.Query().Get("state")}}.Encode(), flow)
		assertLoginError(t, w)
	})
}

func TestOIDCStartRejectsUnknownOrDisabledProvider(t *testing.T) {
	r, api, _, provider := oidcSetup(t, true)

	for _, id := range []string{"not-a-uuid", "019fafd0-0000-7000-8000-000000000000"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/"+id+"/start", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code == http.StatusFound && strings.Contains(w.Header().Get("Location"), "authorize") {
			t.Errorf("provider %q started a login", id)
		}
	}

	// A disabled provider is not a login option, even by direct URL — the login page
	// hides it, and the endpoint must refuse it too.
	if err := api.DB.Model(&provider).Update("enabled", false).Error; err != nil {
		t.Fatalf("disable provider: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/"+provider.ID.String()+"/start", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code == http.StatusFound && strings.Contains(w.Header().Get("Location"), "authorize") {
		t.Error("a disabled provider started a login")
	}
}

// TestOIDCDiscoveryFailureIsHandled: an unreachable or misconfigured issuer must
// land on the login page with a message, not a stack trace or a hang.
func TestOIDCDiscoveryFailureIsHandled(t *testing.T) {
	auth.ResetProviderCache()
	t.Cleanup(auth.ResetProviderCache)

	r, api := setupAPI(t)
	provider := models.AuthProvider{
		Name: "Broken", Type: models.AuthProviderTypeOIDC, Enabled: true,
		// Nothing listens here, which is what a typo in the issuer looks like.
		Issuer: "http://127.0.0.1:1", ClientID: "cid", ClientSecret: "secret",
	}
	if err := api.DB.Create(&provider).Error; err != nil {
		t.Fatalf("create provider: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/"+provider.ID.String()+"/start", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want a 302 back to the login page", w.Code)
	}
	if !strings.HasPrefix(w.Header().Get("Location"), "/login?error=") {
		t.Errorf("Location = %q, want the login page with a message", w.Header().Get("Location"))
	}
}

// assertLoginError: a refused login goes back to /login with a message, and never
// carries a token.
func assertLoginError(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	location := w.Header().Get("Location")
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302: %s", w.Code, w.Body.String())
	}
	if strings.Contains(location, "#token=") {
		t.Fatalf("a session token was issued for a refused login: %q", location)
	}
	if !strings.HasPrefix(location, "/login?error=") {
		t.Errorf("Location = %q, want /login?error=…", location)
	}
}

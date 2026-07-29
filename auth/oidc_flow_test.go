package auth

import (
	"context"
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

	"github.com/aunefyren/autotaggerr/models"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// StartLogin and CompleteLogin against a fake identity provider — real discovery
// document, real JWKS, real RS256 ID token.
//
// The handler-level version of this flow is covered in routers/oidc_flow_test.go.
// The fake provider is deliberately duplicated rather than shared: the two tests
// need different halves of it (this one no cookies or redirects, that one no direct
// calls), and a shared helper would have to live in a package of its own that ships
// in the release archive for no runtime purpose.

type idpStub struct {
	server        *httptest.Server
	key           *rsa.PrivateKey
	clientID      string
	nonce         string
	subject       string
	email         string
	emailVerified bool
	// signWithAnotherKey mints the ID token with a key the JWKS does not publish.
	signWithAnotherKey bool
	// audience overrides the aud claim.
	audience string
	// expired backdates the token past its expiry.
	expired bool
}

func newIDPStub(t *testing.T) *idpStub {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	stub := &idpStub{
		key: key, clientID: "test-client",
		subject: "sub-1", email: "person@example.com", emailVerified: true,
	}

	mux := http.NewServeMux()
	stub.server = httptest.NewServer(mux)
	t.Cleanup(stub.server.Close)

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                stub.server.URL,
			"authorization_endpoint":                stub.server.URL + "/authorize",
			"token_endpoint":                        stub.server.URL + "/token",
			"jwks_uri":                              stub.server.URL + "/jwks",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})

	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "test-key",
				"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(exponentBytes(key.PublicKey.E)),
			}},
		})
	})

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at", "token_type": "Bearer", "expires_in": 3600,
			"id_token": stub.idToken(t),
		})
	})

	return stub
}

func exponentBytes(e int) []byte {
	out := []byte{}
	for e > 0 {
		out = append([]byte{byte(e & 0xff)}, out...)
		e >>= 8
	}
	return out
}

func (s *idpStub) idToken(t *testing.T) string {
	t.Helper()
	now := time.Now()
	exp := now.Add(time.Hour)
	if s.expired {
		exp = now.Add(-time.Hour)
	}
	audience := s.audience
	if audience == "" {
		audience = s.clientID
	}
	claims := jwt.MapClaims{
		"iss": s.server.URL, "aud": audience, "sub": s.subject,
		"exp": exp.Unix(), "iat": now.Unix(), "nonce": s.nonce,
		"email": s.email, "email_verified": s.emailVerified,
		"preferred_username": "person", "name": "A Person",
	}
	signing := s.key
	if s.signWithAnotherKey {
		other, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatalf("generate key: %v", err)
		}
		signing = other
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "test-key"
	signed, err := token.SignedString(signing)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signed
}

func (s *idpStub) provider() models.AuthProvider {
	return models.AuthProvider{
		Name: "Stub", Type: models.AuthProviderTypeOIDC, Enabled: true,
		Issuer: s.server.URL, ClientID: s.clientID, ClientSecret: "secret",
	}
}

// start runs StartLogin and returns the state and nonce it generated, plus the flow
// cookie the callback has to present.
func (s *idpStub) start(t *testing.T, p models.AuthProvider) (state, flow string) {
	t.Helper()
	authURL, flow, err := StartLogin(context.Background(), p, "http://app.example/callback", testKey)
	if err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse auth URL: %v", err)
	}
	s.nonce = parsed.Query().Get("nonce")
	return parsed.Query().Get("state"), flow
}

func TestStartLoginBuildsAnAuthorizationURL(t *testing.T) {
	ResetProviderCache()
	t.Cleanup(ResetProviderCache)
	stub := newIDPStub(t)
	p := stub.provider()
	p.ID = mustUUID(t)

	authURL, flow, err := StartLogin(context.Background(), p, "http://app.example/callback", testKey)
	if err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	if flow == "" {
		t.Fatal("no flow cookie — the callback would have nothing to verify against")
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := parsed.Query()
	if q.Get("client_id") != stub.clientID || q.Get("response_type") != "code" {
		t.Errorf("query = %v", q)
	}
	if q.Get("state") == "" || q.Get("nonce") == "" {
		t.Error("state and nonce are both required: one stops CSRF, the other replay")
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %q, want S256", q.Get("code_challenge_method"))
	}
	if scope := q.Get("scope"); !strings.Contains(scope, "openid") {
		t.Errorf("scope = %q, want openid in it", scope)
	}
	// Discovery is memoized, so a second login must not refetch it.
	if _, _, err := StartLogin(context.Background(), p, "http://app.example/callback", testKey); err != nil {
		t.Fatalf("second StartLogin: %v", err)
	}
}

func TestStartLoginRequiresASigningKey(t *testing.T) {
	ResetProviderCache()
	t.Cleanup(ResetProviderCache)
	stub := newIDPStub(t)
	// Without a key the flow cookie could not be signed, and an unsigned flow cookie
	// is a forgeable one — so this must fail rather than proceed unsigned.
	if _, _, err := StartLogin(context.Background(), stub.provider(), "http://app.example/callback", nil); err == nil {
		t.Error("StartLogin accepted an empty signing key")
	}
}

func TestStartLoginFailsOnUnreachableIssuer(t *testing.T) {
	ResetProviderCache()
	t.Cleanup(ResetProviderCache)
	p := models.AuthProvider{Issuer: "http://127.0.0.1:1", ClientID: "cid"}
	if _, _, err := StartLogin(context.Background(), p, "http://app.example/callback", testKey); err == nil {
		t.Error("discovery against a dead issuer was reported as success")
	}
}

func TestCompleteLoginVerifiesTheIdentity(t *testing.T) {
	ResetProviderCache()
	t.Cleanup(ResetProviderCache)
	stub := newIDPStub(t)
	p := stub.provider()
	p.ID = mustUUID(t)

	state, flow := stub.start(t, p)
	id, err := CompleteLogin(context.Background(), p, "http://app.example/callback", "code-1", state, flow, testKey)
	if err != nil {
		t.Fatalf("CompleteLogin: %v", err)
	}
	if id.Subject != stub.subject {
		t.Errorf("subject = %q, want %q", id.Subject, stub.subject)
	}
	if id.Email != stub.email || !id.EmailVerified {
		t.Errorf("identity = %+v, want the verified email", id)
	}
	if id.PreferredUsername != "person" || id.Name != "A Person" {
		t.Errorf("identity = %+v, want the profile claims", id)
	}
}

func TestCompleteLoginRejectsBadFlows(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, stub *idpStub, p *models.AuthProvider, state, flow *string)
		wantIn string
	}{
		{
			name:   "state does not match the flow cookie",
			mutate: func(_ *testing.T, _ *idpStub, _ *models.AuthProvider, state, _ *string) { *state = "other-state" },
			wantIn: "state mismatch",
		},
		{
			name:   "empty state",
			mutate: func(_ *testing.T, _ *idpStub, _ *models.AuthProvider, state, _ *string) { *state = "" },
			wantIn: "state mismatch",
		},
		{
			name: "flow cookie signed by someone else",
			mutate: func(t *testing.T, _ *idpStub, p *models.AuthProvider, state, flow *string) {
				forged, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
					"pid": p.ID.String(), "sub": *state, "exp": time.Now().Add(time.Hour).Unix(),
				}).SignedString([]byte("not-the-app-key"))
				if err != nil {
					t.Fatalf("sign forged cookie: %v", err)
				}
				*flow = forged
			},
			wantIn: "invalid login flow",
		},
		{
			name: "flow cookie belongs to another provider",
			mutate: func(t *testing.T, stub *idpStub, p *models.AuthProvider, state, flow *string) {
				other := stub.provider()
				other.ID = mustUUID(t)
				*state, *flow = stub.start(t, other)
			},
			wantIn: "different provider",
		},
		{
			name: "ID token signed by an unpublished key",
			mutate: func(_ *testing.T, stub *idpStub, _ *models.AuthProvider, _, _ *string) {
				stub.signWithAnotherKey = true
			},
			wantIn: "verification failed",
		},
		{
			name: "ID token for another audience",
			mutate: func(_ *testing.T, stub *idpStub, _ *models.AuthProvider, _, _ *string) {
				stub.audience = "someone-elses-client"
			},
			wantIn: "verification failed",
		},
		{
			name: "expired ID token",
			mutate: func(_ *testing.T, stub *idpStub, _ *models.AuthProvider, _, _ *string) {
				stub.expired = true
			},
			wantIn: "verification failed",
		},
		{
			name: "nonce from another flow",
			mutate: func(_ *testing.T, stub *idpStub, _ *models.AuthProvider, _, _ *string) {
				stub.nonce = "a-different-nonce"
			},
			wantIn: "nonce mismatch",
		},
		{
			name: "ID token with no subject",
			mutate: func(_ *testing.T, stub *idpStub, _ *models.AuthProvider, _, _ *string) {
				stub.subject = ""
			},
			// go-oidc rejects a subject-less token during verification, before the
			// explicit check — either way it must not become an identity.
			wantIn: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ResetProviderCache()
			t.Cleanup(ResetProviderCache)
			stub := newIDPStub(t)
			p := stub.provider()
			p.ID = mustUUID(t)

			state, flow := stub.start(t, p)
			tc.mutate(t, stub, &p, &state, &flow)

			id, err := CompleteLogin(context.Background(), p, "http://app.example/callback", "code-1", state, flow, testKey)
			if err == nil {
				t.Fatalf("CompleteLogin succeeded and returned %+v", id)
			}
			if tc.wantIn != "" && !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("err = %q, want it to mention %q", err.Error(), tc.wantIn)
			}
		})
	}
}

func TestCompleteLoginRejectsAFailedExchange(t *testing.T) {
	ResetProviderCache()
	t.Cleanup(ResetProviderCache)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": server.URL, "authorization_endpoint": server.URL + "/authorize",
			"token_endpoint": server.URL + "/token", "jwks_uri": server.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "alg": "RS256", "kid": "k",
			"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(exponentBytes(key.PublicKey.E)),
		}}})
	})
	// A rejected code: wrong client secret, reused code, expired code.
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
	})

	p := models.AuthProvider{
		Name: "Stub", Type: models.AuthProviderTypeOIDC, Enabled: true,
		Issuer: server.URL, ClientID: "cid", ClientSecret: "secret",
	}
	p.ID = mustUUID(t)
	authURL, flow, err := StartLogin(context.Background(), p, "http://app.example/callback", testKey)
	if err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	parsed, _ := url.Parse(authURL)

	_, err = CompleteLogin(context.Background(), p, "http://app.example/callback",
		"code-1", parsed.Query().Get("state"), flow, testKey)
	if err == nil {
		t.Fatal("a rejected code exchange was reported as success")
	}
	if !strings.Contains(err.Error(), "exchange") {
		t.Errorf("err = %q, want it to name the exchange", err.Error())
	}
}

// TestCompleteLoginRejectsAResponseWithNoIDToken: an OAuth2-only response carries an
// access token but no verified identity, which is not a login.
func TestCompleteLoginRejectsAResponseWithNoIDToken(t *testing.T) {
	ResetProviderCache()
	t.Cleanup(ResetProviderCache)

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": server.URL, "authorization_endpoint": server.URL + "/authorize",
			"token_endpoint": server.URL + "/token", "jwks_uri": server.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer"})
	})

	p := models.AuthProvider{Issuer: server.URL, ClientID: "cid", ClientSecret: "secret"}
	p.ID = mustUUID(t)
	authURL, flow, err := StartLogin(context.Background(), p, "http://app.example/callback", testKey)
	if err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	parsed, _ := url.Parse(authURL)

	_, err = CompleteLogin(context.Background(), p, "http://app.example/callback",
		"code-1", parsed.Query().Get("state"), flow, testKey)
	if err == nil || !strings.Contains(err.Error(), "ID token") {
		t.Errorf("err = %v, want it to say no ID token was returned", err)
	}
}

func TestScopesFallBackToTheDefault(t *testing.T) {
	if got := scopes(models.AuthProvider{Scopes: "   "}); strings.Join(got, " ") != DefaultScopes {
		t.Errorf("scopes = %v, want the default %q", got, DefaultScopes)
	}
	if got := scopes(models.AuthProvider{Scopes: "openid groups"}); len(got) != 2 {
		t.Errorf("scopes = %v, want the configured pair", got)
	}
}

// mustUUID gives a provider a stable identity, which the flow cookie binds itself to.
func mustUUID(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid: %v", err)
	}
	return id
}

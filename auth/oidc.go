package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
	"gorm.io/gorm"
)

// FlowTTL bounds how long a login round-trip may take. The flow cookie carries the
// state/nonce/PKCE verifier, so this is also how long those stay usable.
const FlowTTL = 10 * time.Minute

// DefaultScopes is used when a provider does not specify its own.
const DefaultScopes = "openid profile email"

// Identity is what an external provider told us about the user. It is deliberately
// small: authentication resolves an identity, ResolveUser maps it onto a
// models.User, and IssueToken mints the same session token password login produces.
type Identity struct {
	Subject           string
	Email             string
	EmailVerified     bool
	PreferredUsername string
	Name              string
}

// flowClaims is the short-lived signed cookie that ties a callback back to the
// request that started it. Keeping it in a signed cookie rather than server memory
// means a restart mid-login fails closed instead of erroring confusingly, and there
// is no session store to grow.
type flowClaims struct {
	ProviderID uuid.UUID `json:"pid"`
	Nonce      string    `json:"nonce"`
	Verifier   string    `json:"pkce"`
	jwt.RegisteredClaims
}

// providerCache memoizes OIDC discovery. Discovery is an HTTP round-trip to the
// issuer and its result (endpoints + JWKS location) is stable, so refetching it on
// every login would add latency and load the IdP for nothing.
var (
	providerMu    sync.Mutex
	providerCache = map[string]*oidc.Provider{}
)

func discover(ctx context.Context, issuer string) (*oidc.Provider, error) {
	providerMu.Lock()
	defer providerMu.Unlock()
	if p, ok := providerCache[issuer]; ok {
		return p, nil
	}
	p, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery failed for %q: %w", issuer, err)
	}
	providerCache[issuer] = p
	return p, nil
}

// ResetProviderCache drops memoized discovery documents. Called when providers are
// reconfigured so a corrected issuer takes effect without a restart.
func ResetProviderCache() {
	providerMu.Lock()
	defer providerMu.Unlock()
	providerCache = map[string]*oidc.Provider{}
}

// randomToken returns a URL-safe 256-bit random string, used for state, nonce, and
// generated API keys.
func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func scopes(p models.AuthProvider) []string {
	s := strings.TrimSpace(p.Scopes)
	if s == "" {
		s = DefaultScopes
	}
	return strings.Fields(s)
}

func oauthConfig(provider *oidc.Provider, p models.AuthProvider, redirectURL string) oauth2.Config {
	return oauth2.Config{
		ClientID:     p.ClientID,
		ClientSecret: p.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  redirectURL,
		Scopes:       scopes(p),
	}
}

// StartLogin builds the provider's authorization URL and the signed flow cookie
// that must come back with the callback. PKCE is always used: it costs nothing and
// removes the value of an intercepted authorization code.
func StartLogin(ctx context.Context, p models.AuthProvider, redirectURL string, signingKey []byte) (authURL string, flowCookie string, err error) {
	if len(signingKey) == 0 {
		return "", "", errors.New("empty signing key")
	}
	provider, err := discover(ctx, p.Issuer)
	if err != nil {
		return "", "", err
	}

	nonce, err := randomToken()
	if err != nil {
		return "", "", err
	}
	state, err := randomToken()
	if err != nil {
		return "", "", err
	}
	verifier := oauth2.GenerateVerifier()

	now := time.Now()
	flow := flowClaims{
		ProviderID: p.ID,
		Nonce:      nonce,
		Verifier:   verifier,
		RegisteredClaims: jwt.RegisteredClaims{
			// The state parameter is the cookie's subject: the callback proves it
			// owns this flow by presenting a state that matches the signed cookie.
			Subject:   state,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(FlowTTL)),
		},
	}
	flowCookie, err = jwt.NewWithClaims(jwt.SigningMethodHS256, flow).SignedString(signingKey)
	if err != nil {
		return "", "", err
	}

	cfg := oauthConfig(provider, p, redirectURL)
	authURL = cfg.AuthCodeURL(state,
		oidc.Nonce(nonce),
		oauth2.S256ChallengeOption(verifier),
	)
	return authURL, flowCookie, nil
}

// CompleteLogin verifies the callback and returns the authenticated identity. It
// checks, in order: the flow cookie's signature and expiry, that the returned state
// matches it, that the code exchange succeeds, that an ID token was returned, that
// the ID token's signature/issuer/audience/expiry verify against the provider's
// JWKS, and that the nonce matches the one this flow issued.
func CompleteLogin(ctx context.Context, p models.AuthProvider, redirectURL, code, state, flowCookie string, signingKey []byte) (Identity, error) {
	var id Identity

	flow := &flowClaims{}
	if _, err := jwt.ParseWithClaims(flowCookie, flow, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return signingKey, nil
	}); err != nil {
		return id, fmt.Errorf("invalid login flow: %w", err)
	}
	if flow.ProviderID != p.ID {
		return id, errors.New("login flow was started for a different provider")
	}
	// Constant-time-ish equality is unnecessary here (state is single-use and
	// short-lived), but the check itself is what stops CSRF on the callback.
	if state == "" || state != flow.Subject {
		return id, errors.New("state mismatch")
	}

	provider, err := discover(ctx, p.Issuer)
	if err != nil {
		return id, err
	}
	cfg := oauthConfig(provider, p, redirectURL)

	token, err := cfg.Exchange(ctx, code, oauth2.VerifierOption(flow.Verifier))
	if err != nil {
		return id, fmt.Errorf("code exchange failed: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return id, errors.New("provider returned no ID token")
	}

	idToken, err := provider.Verifier(&oidc.Config{ClientID: p.ClientID}).Verify(ctx, rawIDToken)
	if err != nil {
		return id, fmt.Errorf("ID token verification failed: %w", err)
	}
	if idToken.Nonce != flow.Nonce {
		return id, errors.New("nonce mismatch")
	}

	var claims struct {
		Email             string `json:"email"`
		EmailVerified     *bool  `json:"email_verified"`
		PreferredUsername string `json:"preferred_username"`
		Name              string `json:"name"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return id, fmt.Errorf("failed to read ID token claims: %w", err)
	}

	id = Identity{
		Subject:           idToken.Subject,
		Email:             strings.TrimSpace(claims.Email),
		EmailVerified:     claims.EmailVerified != nil && *claims.EmailVerified,
		PreferredUsername: strings.TrimSpace(claims.PreferredUsername),
		Name:              strings.TrimSpace(claims.Name),
	}
	if id.Subject == "" {
		return id, errors.New("ID token has no subject")
	}
	return id, nil
}

// ResolveUser maps an external identity onto a local account.
//
// Matching order matters:
//  1. The (provider, subject) link — `sub` is immutable at the IdP, so an already
//     linked account is found even if its email or username changed there.
//  2. A *verified* email. Linking on an unverified email would let anyone who can
//     set their address at the IdP take over a local account, so unverified emails
//     never match an existing user.
//  3. Signup, if the provider allows it.
func ResolveUser(db *gorm.DB, p models.AuthProvider, id Identity) (models.User, error) {
	var user models.User

	if err := db.Where("auth_provider_id = ? AND external_subject = ?", p.ID, id.Subject).
		First(&user).Error; err == nil {
		return user, nil
	}

	if id.Email != "" && id.EmailVerified {
		if err := db.Where("email = ?", id.Email).First(&user).Error; err == nil {
			// Adopt the account: record the link so future logins take path 1.
			if err := db.Model(&user).Updates(map[string]any{
				"auth_provider_id": p.ID,
				"external_subject": id.Subject,
			}).Error; err != nil {
				return user, err
			}
			user.AuthProviderID = &p.ID
			user.ExternalSubject = id.Subject
			return user, nil
		}
	}

	if !p.AllowSignup {
		return user, errors.New("no local account is linked to this identity, and signup is disabled for this provider")
	}

	username, err := uniqueUsername(db, id)
	if err != nil {
		return user, err
	}
	role := strings.TrimSpace(p.DefaultRole)
	if role == "" {
		role = models.UserRoleAdmin
	}
	apiKey, err := randomToken()
	if err != nil {
		return user, err
	}
	providerID := p.ID
	user = models.User{
		Username:        username,
		Email:           id.Email,
		Role:            role,
		APIKey:          apiKey,
		AuthProviderID:  &providerID,
		ExternalSubject: id.Subject,
	}
	if err := db.Create(&user).Error; err != nil {
		return user, err
	}
	return user, nil
}

// uniqueUsername picks a display username that is not already taken. Usernames are
// unique locally but nothing stops two IdPs handing out the same one, so collisions
// get a numeric suffix rather than failing the login.
func uniqueUsername(db *gorm.DB, id Identity) (string, error) {
	base := id.PreferredUsername
	if base == "" {
		base = id.Email
		if i := strings.Index(base, "@"); i > 0 {
			base = base[:i]
		}
	}
	if base == "" {
		base = "user"
	}

	candidate := base
	for i := 2; i < 100; i++ {
		var count int64
		if err := db.Model(&models.User{}).Where("username = ?", candidate).Count(&count).Error; err != nil {
			return "", err
		}
		if count == 0 {
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s%d", base, i)
	}
	return "", errors.New("could not find an available username")
}

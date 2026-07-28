package auth

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/database"
	"github.com/aunefyren/autotaggerr/models"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var testKey = []byte("test-signing-key")

func oidcTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := database.Connect(models.DatabaseConfig{Type: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return db
}

func testProvider(t *testing.T, db *gorm.DB, allowSignup bool) models.AuthProvider {
	t.Helper()
	p := models.AuthProvider{
		Name: "Test IdP", Type: models.AuthProviderTypeOIDC, Enabled: true,
		Issuer: "https://id.example.com", ClientID: "cid", ClientSecret: "secret",
		AllowSignup: allowSignup,
	}
	if err := db.Create(&p).Error; err != nil {
		t.Fatalf("create provider: %v", err)
	}
	return p
}

// TestResolveUserBySubject: an already-linked account is found by (provider, sub)
// even when its email no longer matches what the IdP sends.
func TestResolveUserBySubject(t *testing.T) {
	db := oidcTestDB(t)
	p := testProvider(t, db, false)

	pid := p.ID
	existing := models.User{
		Username: "alice", Email: "old@example.com", Role: models.UserRoleAdmin,
		AuthProviderID: &pid, ExternalSubject: "sub-1",
	}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	got, err := ResolveUser(db, p, Identity{Subject: "sub-1", Email: "new@example.com", EmailVerified: true})
	if err != nil {
		t.Fatalf("ResolveUser: %v", err)
	}
	if got.ID != existing.ID {
		t.Errorf("resolved %s, want the linked account %s", got.ID, existing.ID)
	}
}

// TestResolveUserLinksVerifiedEmail: a pre-existing local account is adopted on
// first federated login, and the link is persisted so later logins match by subject.
func TestResolveUserLinksVerifiedEmail(t *testing.T) {
	db := oidcTestDB(t)
	p := testProvider(t, db, false)

	existing := models.User{Username: "alice", Email: "alice@example.com", Role: models.UserRoleAdmin}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	got, err := ResolveUser(db, p, Identity{Subject: "sub-1", Email: "alice@example.com", EmailVerified: true})
	if err != nil {
		t.Fatalf("ResolveUser: %v", err)
	}
	if got.ID != existing.ID {
		t.Fatalf("resolved %s, want %s", got.ID, existing.ID)
	}

	var stored models.User
	if err := db.First(&stored, "id = ?", existing.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.AuthProviderID == nil || *stored.AuthProviderID != p.ID || stored.ExternalSubject != "sub-1" {
		t.Errorf("link was not persisted: %+v", stored)
	}
}

// TestResolveUserIgnoresUnverifiedEmail is the account-takeover guard: if the IdP
// does not vouch for the address, matching on it would let anyone who can set their
// email at the IdP claim an existing local account.
func TestResolveUserIgnoresUnverifiedEmail(t *testing.T) {
	db := oidcTestDB(t)
	p := testProvider(t, db, false)

	existing := models.User{Username: "alice", Email: "alice@example.com", Role: models.UserRoleAdmin}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	if _, err := ResolveUser(db, p, Identity{Subject: "attacker", Email: "alice@example.com", EmailVerified: false}); err == nil {
		t.Fatal("ResolveUser accepted an unverified email match")
	}

	var stored models.User
	if err := db.First(&stored, "id = ?", existing.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.AuthProviderID != nil || stored.ExternalSubject != "" {
		t.Errorf("existing account was linked to an unverified identity: %+v", stored)
	}
}

// TestResolveUserSignupDisabled: unknown identities are rejected unless the
// provider opts into signup.
func TestResolveUserSignupDisabled(t *testing.T) {
	db := oidcTestDB(t)
	p := testProvider(t, db, false)

	if _, err := ResolveUser(db, p, Identity{Subject: "sub-new", Email: "new@example.com", EmailVerified: true}); err == nil {
		t.Fatal("ResolveUser created or accepted an unknown identity with signup disabled")
	}
}

// TestResolveUserSignupCreates: with signup on, a new linked account is created and
// gets its own API key.
func TestResolveUserSignupCreates(t *testing.T) {
	db := oidcTestDB(t)
	p := testProvider(t, db, true)

	got, err := ResolveUser(db, p, Identity{
		Subject: "sub-new", Email: "new@example.com", EmailVerified: true, PreferredUsername: "newbie",
	})
	if err != nil {
		t.Fatalf("ResolveUser: %v", err)
	}
	if got.Username != "newbie" || got.Email != "new@example.com" {
		t.Errorf("created user = %+v", got)
	}
	if got.APIKey == "" {
		t.Error("created user has no API key")
	}
	if got.AuthProviderID == nil || *got.AuthProviderID != p.ID {
		t.Errorf("created user is not linked: %+v", got)
	}
}

// TestResolveUserSignupAvoidsUsernameCollision: two IdP accounts can want the same
// preferred_username; the second must not fail the unique index.
func TestResolveUserSignupAvoidsUsernameCollision(t *testing.T) {
	db := oidcTestDB(t)
	p := testProvider(t, db, true)

	if err := db.Create(&models.User{Username: "newbie", Role: models.UserRoleAdmin}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := ResolveUser(db, p, Identity{Subject: "sub-new", PreferredUsername: "newbie"})
	if err != nil {
		t.Fatalf("ResolveUser: %v", err)
	}
	if got.Username != "newbie2" {
		t.Errorf("username = %q, want newbie2", got.Username)
	}
}

// makeFlow builds a flow cookie the way StartLogin does, so callback checks can be
// tested without reaching a real provider.
func makeFlow(t *testing.T, providerID uuid.UUID, state, nonce string, expires time.Time) string {
	t.Helper()
	claims := flowClaims{
		ProviderID: providerID, Nonce: nonce, Verifier: "verifier",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   state,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(expires),
		},
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(testKey)
	if err != nil {
		t.Fatalf("sign flow: %v", err)
	}
	return s
}

// TestCompleteLoginRejectsBadFlow covers the checks that run before any network
// call, so they are exercised without a live IdP. Each is a real attack: a forged
// or replayed callback, a cookie from another provider, and an expired flow.
func TestCompleteLoginRejectsBadFlow(t *testing.T) {
	p := models.AuthProvider{Base: models.Base{ID: uuid.New()}, Issuer: "https://id.invalid", ClientID: "cid"}
	other := uuid.New()
	future := time.Now().Add(time.Minute)

	cases := []struct {
		name  string
		flow  string
		state string
	}{
		{"state mismatch", makeFlow(t, p.ID, "real-state", "n", future), "attacker-state"},
		{"missing state", makeFlow(t, p.ID, "real-state", "n", future), ""},
		{"cookie for another provider", makeFlow(t, other, "s", "n", future), "s"},
		{"expired flow", makeFlow(t, p.ID, "s", "n", time.Now().Add(-time.Minute)), "s"},
		{"unsigned garbage", "not-a-token", "s"},
		{"empty cookie", "", "s"},
	}
	for _, c := range cases {
		_, err := CompleteLogin(context.Background(), p, "https://app/cb", "code", c.state, c.flow, testKey)
		if err == nil {
			t.Errorf("%s: CompleteLogin accepted an invalid callback", c.name)
		}
	}
}

// TestCompleteLoginRejectsForeignSignature: a flow cookie signed with a different
// key must not be accepted, or anyone could mint their own login flow.
func TestCompleteLoginRejectsForeignSignature(t *testing.T) {
	p := models.AuthProvider{Base: models.Base{ID: uuid.New()}, Issuer: "https://id.invalid", ClientID: "cid"}
	claims := flowClaims{
		ProviderID: p.ID, Nonce: "n", Verifier: "v",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "s",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
		},
	}
	forged, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("attacker-key"))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := CompleteLogin(context.Background(), p, "https://app/cb", "code", "s", forged, testKey); err == nil {
		t.Fatal("CompleteLogin accepted a cookie signed with the wrong key")
	}
}

func TestScopesDefault(t *testing.T) {
	if got := scopes(models.AuthProvider{}); len(got) != 3 || got[0] != "openid" {
		t.Errorf("default scopes = %v", got)
	}
	if got := scopes(models.AuthProvider{Scopes: "openid email"}); len(got) != 2 {
		t.Errorf("explicit scopes = %v", got)
	}
}

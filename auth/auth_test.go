package auth

import (
	"testing"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/google/uuid"
)

func TestPasswordHashCheck(t *testing.T) {
	h, err := HashPassword("s3cret")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if h == "s3cret" {
		t.Error("password stored in plaintext")
	}
	if !CheckPassword(h, "s3cret") {
		t.Error("correct password rejected")
	}
	if CheckPassword(h, "wrong") {
		t.Error("wrong password accepted")
	}
}

func testUser() models.User {
	u := models.User{Role: "admin"}
	u.ID = uuid.New()
	return u
}

func TestTokenRoundTrip(t *testing.T) {
	key := []byte("test-signing-key")
	u := testUser()
	tok, err := IssueToken(u, key, time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	claims, err := ParseToken(tok, key)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if claims.UserID != u.ID {
		t.Errorf("uid = %v, want %v", claims.UserID, u.ID)
	}
	if claims.Role != "admin" {
		t.Errorf("role = %q, want admin", claims.Role)
	}
}

func TestTokenWrongKey(t *testing.T) {
	tok, _ := IssueToken(testUser(), []byte("key-one"), time.Hour)
	if _, err := ParseToken(tok, []byte("key-two")); err == nil {
		t.Error("expected verification failure with the wrong key")
	}
}

func TestTokenExpired(t *testing.T) {
	tok, _ := IssueToken(testUser(), []byte("k"), -time.Minute) // already expired
	if _, err := ParseToken(tok, []byte("k")); err == nil {
		t.Error("expected expiry failure")
	}
}

func TestIssueTokenEmptyKey(t *testing.T) {
	if _, err := IssueToken(testUser(), nil, time.Hour); err == nil {
		t.Error("expected error for empty signing key")
	}
}

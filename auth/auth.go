// Package auth handles authentication and session tokens. It deliberately splits
// two concerns so OAuth 2.0 / OIDC can be added later without touching the rest
// of the app:
//
//   - Authentication ("who is this user?") — password login today; an OAuth
//     callback tomorrow. Each method just needs to resolve a models.User.
//   - Session issuance ("prove it on later requests") — a signed JWT minted by
//     IssueToken. Every authentication method funnels through this one issuer, so
//     middleware, handlers, and clients never care how the user logged in.
package auth

import (
	"errors"
	"time"

	"github.com/aunefyren/autotaggerr/models"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// DefaultTokenTTL is how long an issued session token stays valid.
const DefaultTokenTTL = 7 * 24 * time.Hour

// Claims is the session-token payload. It records the authenticated identity, not
// the authentication method, so it is identical whether the user logged in with a
// password or (later) via OAuth.
type Claims struct {
	UserID uuid.UUID `json:"uid"`
	Role   string    `json:"role"`
	jwt.RegisteredClaims
}

// HashPassword returns a bcrypt hash suitable for storage.
func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(h), err
}

// CheckPassword reports whether password matches the stored bcrypt hash.
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// IssueToken mints a signed session token for an already-authenticated user. The
// signing key is the app private key (HS256). Kept separate from authentication
// so every method of logging in produces the same kind of token.
func IssueToken(user models.User, signingKey []byte, ttl time.Duration) (string, error) {
	if len(signingKey) == 0 {
		return "", errors.New("empty signing key")
	}
	now := time.Now()
	claims := Claims{
		UserID: user.ID,
		Role:   user.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.ID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(signingKey)
}

// ParseToken validates a session token's signature and expiry and returns its claims.
func ParseToken(tokenString string, signingKey []byte) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return signingKey, nil
	})
	if err != nil {
		return nil, err
	}
	return claims, nil
}

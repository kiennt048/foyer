package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// GenerateSessionToken generates a 32-byte cryptographically secure session token.
func GenerateSessionToken() (token string, tokenHash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("failed to generate random session token: %w", err)
	}

	token = base64.RawURLEncoding.EncodeToString(b)
	hash := sha256.Sum256([]byte(token))
	tokenHash = hex.EncodeToString(hash[:])

	return token, tokenHash, nil
}

// HashSessionToken computes the SHA-256 hex digest of a session token string.
func HashSessionToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

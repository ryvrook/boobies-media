package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// Base58Alphabet is the Bitcoin alphabet: it omits 0, O, I and l so a share
// link read aloud or retyped by hand is unambiguous.
const Base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// tokenBytes is the entropy behind session tokens and API keys.
const tokenBytes = 32

// NewSessionToken returns a fresh opaque session token. The plaintext goes in
// the cookie; only HashToken(token) is ever stored.
func NewSessionToken() (string, error) {
	return randomString(tokenBytes)
}

// NewAPIKey returns a fresh API key. The plaintext is shown to the operator
// exactly once; only HashToken(key) is stored.
func NewAPIKey() (string, error) {
	s, err := randomString(tokenBytes)
	if err != nil {
		return "", err
	}
	return "bm_" + s, nil
}

func randomString(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("auth: read random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashToken returns the lowercase hex SHA-256 of a bearer secret. Session
// tokens and API keys are both stored this way, so a database or backup leak
// does not hand out live credentials.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NewItemID returns a random 8-character base58 identifier (~2^47 of space).
// It doubles as the public, unguessable share slug.
func NewItemID() (string, error) {
	const n = 8
	// 232 == 58*4: rejecting bytes at or above it keeps the mapping uniform.
	const limit = 232

	out := make([]byte, 0, n)
	buf := make([]byte, n)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("auth: read random bytes: %w", err)
		}
		for _, b := range buf {
			if b >= limit {
				continue
			}
			out = append(out, Base58Alphabet[b%58])
			if len(out) == n {
				break
			}
		}
	}
	return string(out), nil
}

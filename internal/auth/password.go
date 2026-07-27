// Package auth holds password hashing, token generation, and the login rate
// limiter. It knows nothing about HTTP or the database.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// ErrInvalidHash means a stored password hash could not be parsed.
var ErrInvalidHash = errors.New("auth: malformed password hash")

// Argon2Params are the cost parameters for one hash. They are encoded into the
// stored hash, so old hashes keep verifying after the defaults change.
type Argon2Params struct {
	Memory  uint32 // KiB
	Time    uint32 // iterations
	Threads uint8
	SaltLen uint32 // bytes
	KeyLen  uint32 // bytes
}

// DefaultParams follow the OWASP argon2id recommendation.
var DefaultParams = Argon2Params{Memory: 19456, Time: 2, Threads: 1, SaltLen: 16, KeyLen: 32}

// HashPassword hashes a password with DefaultParams.
func HashPassword(password string) (string, error) {
	return HashPasswordWithParams(password, DefaultParams)
}

// HashPasswordWithParams hashes a password and returns a PHC-encoded string:
// $argon2id$v=19$m=<mem>,t=<time>,p=<threads>$<salt>$<key>
func HashPasswordWithParams(password string, p Argon2Params) (string, error) {
	if p.SaltLen == 0 || p.KeyLen == 0 || p.Time == 0 || p.Memory == 0 || p.Threads == 0 {
		return "", fmt.Errorf("auth: argon2 params must all be non-zero, got %+v", p)
	}
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: read salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword reports whether password matches the PHC-encoded hash. The
// cost parameters are read back out of encoded, so hashes made with older
// parameters still verify.
func VerifyPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, ErrInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, ErrInvalidHash
	}
	if version != argon2.Version {
		return false, ErrInvalidHash
	}
	var p Argon2Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil {
		return false, ErrInvalidHash
	}
	if p.Memory == 0 || p.Time == 0 || p.Threads == 0 {
		return false, ErrInvalidHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return false, ErrInvalidHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, ErrInvalidHash
	}
	got := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

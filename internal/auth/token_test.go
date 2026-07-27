package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestNewSessionTokenIsUniqueAndURLSafe(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		tok, err := NewSessionToken()
		if err != nil {
			t.Fatalf("NewSessionToken: %v", err)
		}
		if seen[tok] {
			t.Fatalf("NewSessionToken repeated a value: %q", tok)
		}
		seen[tok] = true
		if len(tok) < 40 {
			t.Fatalf("token %q is only %d chars; want >=40 for 32 random bytes", tok, len(tok))
		}
		if strings.ContainsAny(tok, "+/=") {
			t.Fatalf("token %q contains characters that are unsafe in a cookie/URL", tok)
		}
	}
}

func TestNewAPIKeyIsPrefixed(t *testing.T) {
	key, err := NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	if !strings.HasPrefix(key, "bm_") {
		t.Errorf("API key %q does not start with \"bm_\"", key)
	}
	if strings.ContainsAny(strings.TrimPrefix(key, "bm_"), "+/=") {
		t.Errorf("API key %q contains characters unsafe in an Authorization header", key)
	}
}

func TestHashTokenIsHexSHA256AndStable(t *testing.T) {
	got := HashToken("hello")
	sum := sha256.Sum256([]byte("hello"))
	want := hex.EncodeToString(sum[:])
	if got != want {
		t.Errorf("HashToken(\"hello\") = %q, want %q", got, want)
	}
	if len(got) != 64 {
		t.Errorf("hash length = %d, want 64 hex chars", len(got))
	}
	if got != HashToken("hello") {
		t.Error("HashToken is not deterministic")
	}
	if got == HashToken("hellp") {
		t.Error("HashToken collided on different inputs")
	}
}

func TestNewItemIDShapeAndAlphabet(t *testing.T) {
	for i := 0; i < 200; i++ {
		id, err := NewItemID()
		if err != nil {
			t.Fatalf("NewItemID: %v", err)
		}
		if len(id) != 8 {
			t.Fatalf("id %q has length %d, want 8", id, len(id))
		}
		for _, r := range id {
			if !strings.ContainsRune(Base58Alphabet, r) {
				t.Fatalf("id %q contains %q, which is outside the base58 alphabet", id, r)
			}
		}
	}
}

func TestBase58AlphabetExcludesAmbiguousCharacters(t *testing.T) {
	if len(Base58Alphabet) != 58 {
		t.Fatalf("alphabet has %d characters, want 58", len(Base58Alphabet))
	}
	for _, bad := range []string{"0", "O", "I", "l"} {
		if strings.Contains(Base58Alphabet, bad) {
			t.Errorf("alphabet contains the ambiguous character %q", bad)
		}
	}
}

func TestNewItemIDIsNotObviouslyRepeating(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		id, err := NewItemID()
		if err != nil {
			t.Fatalf("NewItemID: %v", err)
		}
		if seen[id] {
			t.Fatalf("NewItemID repeated %q within 500 draws", id)
		}
		seen[id] = true
	}
}

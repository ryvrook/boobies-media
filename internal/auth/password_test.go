package auth

import (
	"strings"
	"testing"
)

// cheapParams keep the test suite fast; VerifyPassword reads params from the
// encoded hash, so this exercises the same code path as production values.
var cheapParams = Argon2Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}

func TestDefaultParamsMatchSpec(t *testing.T) {
	if DefaultParams.Memory != 19456 || DefaultParams.Time != 2 || DefaultParams.Threads != 1 {
		t.Errorf("DefaultParams = %+v, want m=19456 t=2 p=1", DefaultParams)
	}
	if DefaultParams.SaltLen != 16 || DefaultParams.KeyLen != 32 {
		t.Errorf("DefaultParams = %+v, want 16-byte salt and 32-byte key", DefaultParams)
	}
}

func TestHashVerifyRoundTrip(t *testing.T) {
	encoded, err := HashPasswordWithParams("correct horse battery staple", cheapParams)
	if err != nil {
		t.Fatalf("HashPasswordWithParams: %v", err)
	}
	ok, err := VerifyPassword(encoded, "correct horse battery staple")
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if !ok {
		t.Error("VerifyPassword returned false for the correct password")
	}
}

func TestVerifyRejectsWrongPassword(t *testing.T) {
	encoded, err := HashPasswordWithParams("hunter2", cheapParams)
	if err != nil {
		t.Fatalf("HashPasswordWithParams: %v", err)
	}
	ok, err := VerifyPassword(encoded, "hunter3")
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if ok {
		t.Error("VerifyPassword returned true for the wrong password")
	}
}

func TestHashIsSaltedPerCall(t *testing.T) {
	a, err := HashPasswordWithParams("same", cheapParams)
	if err != nil {
		t.Fatalf("HashPasswordWithParams: %v", err)
	}
	b, err := HashPasswordWithParams("same", cheapParams)
	if err != nil {
		t.Fatalf("HashPasswordWithParams: %v", err)
	}
	if a == b {
		t.Error("two hashes of the same password are identical; the salt is not random")
	}
}

func TestEncodingFormat(t *testing.T) {
	encoded, err := HashPasswordWithParams("x", cheapParams)
	if err != nil {
		t.Fatalf("HashPasswordWithParams: %v", err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Errorf("encoded = %q, want the PHC argon2id prefix carrying the params", encoded)
	}
	if n := len(strings.Split(encoded, "$")); n != 6 {
		t.Errorf("encoded has %d $-separated fields, want 6", n)
	}
}

func TestVerifyRejectsMalformedHashes(t *testing.T) {
	cases := map[string]string{
		"empty":           "",
		"not phc":         "plaintext-password",
		"wrong algorithm": "$argon2i$v=19$m=64,t=1,p=1$c2FsdHNhbHRzYWx0c2E$aGFzaA",
		"wrong version":   "$argon2id$v=16$m=64,t=1,p=1$c2FsdHNhbHRzYWx0c2E$aGFzaA",
		"bad params":      "$argon2id$v=19$nonsense$c2FsdHNhbHRzYWx0c2E$aGFzaA",
		"bad salt base64": "$argon2id$v=19$m=64,t=1,p=1$!!!!$aGFzaA",
		"bad key base64":  "$argon2id$v=19$m=64,t=1,p=1$c2FsdHNhbHRzYWx0c2E$!!!!",
		"too few fields":  "$argon2id$v=19$m=64,t=1,p=1$c2FsdA",
	}
	for name, encoded := range cases {
		t.Run(name, func(t *testing.T) {
			ok, err := VerifyPassword(encoded, "whatever")
			if err == nil {
				t.Fatal("VerifyPassword accepted a malformed hash, want ErrInvalidHash")
			}
			if ok {
				t.Fatal("VerifyPassword returned true for a malformed hash")
			}
		})
	}
}

func TestHashPasswordUsesDefaultParams(t *testing.T) {
	encoded, err := HashPassword("production-strength")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Errorf("encoded = %q, want the default params encoded", encoded)
	}
	ok, err := VerifyPassword(encoded, "production-strength")
	if err != nil || !ok {
		t.Errorf("round trip with default params failed: ok=%v err=%v", ok, err)
	}
}

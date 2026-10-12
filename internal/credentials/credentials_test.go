package credentials

import (
	"testing"
	"time"
)

func TestTOTPAndEncryption(t *testing.T) {
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	code, err := Code(secret, 1)
	if err != nil || code != "287082" {
		t.Fatalf("RFC 6238 vector: %s %v", code, err)
	}
	now := time.Unix(59, 0)
	if step, ok := Verify(secret, code, now, -1); !ok || step != 1 {
		t.Fatal("valid code rejected")
	}
	if _, ok := Verify(secret, code, now, 1); ok {
		t.Fatal("replayed code accepted")
	}
	if _, ok := Verify(secret, "123456", now, -1); ok {
		t.Fatal("wrong code accepted")
	}
	key := make([]byte, 32)
	encrypted, err := Encrypt(key, secret)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Decrypt(key, encrypted)
	if err != nil || plain != secret {
		t.Fatal("encryption round trip", err)
	}
	key[0] = 1
	if _, err := Decrypt(key, encrypted); err == nil {
		t.Fatal("wrong key accepted")
	}
	codes, hashes, err := RecoveryCodes()
	if err != nil || len(codes) != 10 {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i, code := range codes {
		if hashes[i] != RecoveryHash(code) || seen[code] {
			t.Fatal("invalid recovery code")
		}
		seen[code] = true
	}
}

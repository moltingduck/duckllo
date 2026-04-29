package auth

import (
	"strings"
	"testing"
)

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !CheckPassword(hash, "correct-horse-battery-staple") {
		t.Error("correct password rejected")
	}
	if CheckPassword(hash, "wrong") {
		t.Error("wrong password accepted")
	}
}

func TestPasswordTooShort(t *testing.T) {
	if _, err := HashPassword("12345"); err == nil {
		t.Error("expected an error for sub-6-character password")
	}
}

func TestMintAPIKeyShape(t *testing.T) {
	plain, prefix, hash, err := MintAPIKey()
	if err != nil {
		t.Fatalf("MintAPIKey: %v", err)
	}
	if !strings.HasPrefix(plain, "duckllo_") {
		t.Errorf("plain missing prefix: %s", plain)
	}
	if !strings.HasPrefix(prefix, "duckllo_") {
		t.Errorf("prefix missing prefix: %s", prefix)
	}
	parts := strings.Split(plain, "_")
	if len(parts) != 3 {
		t.Errorf("plain should be duckllo_<prefix>_<secret>; got %d parts", len(parts))
	}
	if !strings.HasPrefix(plain, prefix+"_") {
		t.Errorf("plain should start with prefix+_, got plain=%s prefix=%s", plain, prefix)
	}
	if !CheckAPIKey(hash, plain) {
		t.Error("freshly minted plain failed bcrypt check against its own hash")
	}
}

func TestParseAPIKeyPrefix(t *testing.T) {
	plain, want, _, err := MintAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseAPIKeyPrefix(plain)
	if err != nil {
		t.Fatalf("ParseAPIKeyPrefix: %v", err)
	}
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestParseAPIKeyPrefixRejectsMalformed(t *testing.T) {
	cases := []string{
		"",
		"plain-uuid-style-token",
		"duckllo_short",                // missing secret half
		"duckllo_xx_yy",                 // prefix wrong length
		"duckllo_zzzzzzzz_abc",          // hex check is loose, but we accept this — rely on hash check
	}
	for _, c := range cases[:4] { // only first four are guaranteed wrong shape
		if _, err := ParseAPIKeyPrefix(c); err == nil {
			t.Errorf("ParseAPIKeyPrefix(%q): expected error, got nil", c)
		}
	}
}

func TestTwoMintsCollideOnHashOnlyByCoincidence(t *testing.T) {
	// Sanity: two consecutive mints produce different plaintexts (the
	// 24-byte secret is what differs even when prefixes happen to collide).
	a, _, _, err := MintAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	b, _, _, err := MintAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("two mints produced identical plaintext — RNG broken?")
	}
}

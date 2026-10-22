/*
Package auth tests the local account password hashing boundary.

The tests verify salted encoding, successful login checks, malformed verifier
rejection, and bounded input behavior without persisting account data.
*/
package auth

import (
	"strings"
	"testing"
)

func TestHashPasswordUsesUniqueArgon2idVerifier(t *testing.T) {
	first, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("password hashes reused the same salt")
	}
	if !strings.HasPrefix(first, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("unexpected password verifier format: %q", first)
	}
	if !VerifyPassword(first, "correct horse battery staple") {
		t.Fatal("matching password was rejected")
	}
	if VerifyPassword(first, "incorrect password") {
		t.Fatal("incorrect password was accepted")
	}
}

func TestPasswordVerifierRejectsMalformedOrUnboundedInputs(t *testing.T) {
	valid, err := HashPassword("password")
	if err != nil {
		t.Fatal(err)
	}
	for _, encoded := range []string{
		"",
		strings.Replace(valid, "m=19456", "m=4294967295", 1),
		strings.Replace(valid, "argon2id", "argon2i", 1),
		valid + "$extra",
		"$argon2id$v=19$m=19456,t=2,p=1$bad$bad",
	} {
		if VerifyPassword(encoded, "password") {
			t.Errorf("invalid verifier was accepted: %q", encoded)
		}
	}
	if _, err := HashPassword(""); err == nil {
		t.Fatal("empty password was accepted")
	}
	if _, err := HashPassword(strings.Repeat("x", maxPasswordBytes+1)); err == nil {
		t.Fatal("oversized password was accepted")
	}
	if VerifyPassword(valid, strings.Repeat("x", maxPasswordBytes+1)) {
		t.Fatal("oversized verification input was accepted")
	}
}

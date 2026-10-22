/*
Package auth provides password hashing primitives for local human accounts.

The encoded form is self-describing and bounded to the implementation's chosen
Argon2id parameters so an untrusted stored value cannot request arbitrary work.
*/
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

const (
	passwordSaltBytes = 16
	passwordKeyBytes  = 32
	passwordMemoryKiB = 19 * 1024
	passwordTimeCost  = 2
	passwordThreads   = 1
	maxPasswordBytes  = 1024
)

var rawBase64 = base64.RawStdEncoding

// HashPassword returns an Argon2id PHC string using a fresh cryptographic salt.
func HashPassword(password string) (string, error) {
	if password == "" || len(password) > maxPasswordBytes {
		return "", errors.New("password must contain between 1 and 1024 bytes")
	}
	salt := make([]byte, passwordSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	derived := argon2.IDKey([]byte(password), salt, passwordTimeCost, passwordMemoryKiB, passwordThreads, passwordKeyBytes)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, passwordMemoryKiB, passwordTimeCost, passwordThreads,
		rawBase64.EncodeToString(salt), rawBase64.EncodeToString(derived)), nil
}

// VerifyPassword checks a password against a supported bounded Argon2id PHC string.
func VerifyPassword(encoded, password string) bool {
	if password == "" || len(password) > maxPasswordBytes || len(encoded) > 256 {
		return false
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argon2.Version) || parts[3] != fmt.Sprintf("m=%d,t=%d,p=%d", passwordMemoryKiB, passwordTimeCost, passwordThreads) {
		return false
	}
	salt, err := rawBase64.DecodeString(parts[4])
	if err != nil || len(salt) != passwordSaltBytes {
		return false
	}
	want, err := rawBase64.DecodeString(parts[5])
	if err != nil || len(want) != passwordKeyBytes {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, passwordTimeCost, passwordMemoryKiB, passwordThreads, passwordKeyBytes)
	return subtle.ConstantTimeCompare(got, want) == 1
}

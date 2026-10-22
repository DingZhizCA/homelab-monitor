package plugins

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestVerifyPackageChecksCanonicalManifestSignatureAndArtifactDigest(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	artifact := []byte("plugin executable bytes")
	digest := sha256.Sum256(artifact)
	manifest := validManifest()
	manifest.ArtifactSHA256 = hex.EncodeToString(digest[:])
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(privateKey, manifestJSON)

	verified, err := VerifyPackage(publicKey, manifestJSON, signature, artifact)
	if err != nil || verified.PluginID != manifest.PluginID {
		t.Fatalf("VerifyPackage() = %+v, %v", verified, err)
	}
	if _, err := VerifyPackage(publicKey, manifestJSON, signature, []byte("changed artifact")); err == nil {
		t.Fatal("modified artifact was accepted")
	}
	badSignature := append([]byte(nil), signature...)
	badSignature[0] ^= 1
	if _, err := VerifyPackage(publicKey, manifestJSON, badSignature, artifact); err == nil {
		t.Fatal("modified signature was accepted")
	}
	if _, err := VerifyPackage(publicKey, append([]byte(" "), manifestJSON...), signature, artifact); err == nil {
		t.Fatal("non-canonical manifest encoding was accepted")
	}
}

func TestManifestDecodersRejectDuplicateIdentityFields(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifestJSON, err := json.Marshal(validManifest())
	if err != nil {
		t.Fatal(err)
	}
	duplicated := strings.Replace(string(manifestJSON), `"plugin_id":"storage.health"`, `"plugin_id":"storage.health","plugin_id":"other"`, 1)
	if duplicated == string(manifestJSON) {
		t.Fatal("test manifest identity field was not found")
	}
	if _, err := DecodeManifest([]byte(duplicated)); err == nil {
		t.Fatal("manifest decoder accepted a duplicate plugin_id")
	}
	if _, err := VerifyPackage(publicKey, []byte(duplicated), nil, nil); err == nil {
		t.Fatal("package verifier accepted a duplicate plugin_id")
	}
}

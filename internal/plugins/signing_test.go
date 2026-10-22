package plugins

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

func TestSignPackageProducesVerifiableCanonicalBundle(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		PluginID: "health", Version: "1.2.0", Publisher: "test.publisher",
		Platform: Platform{OS: "linux", Arch: "amd64"}, Protocol: ProtocolRange{Min: 1, Max: 1},
		Collectors: []CollectorSpec{{ID: "system"}},
	}
	bundle, key, err := SignPackage(privateKey, manifest, []byte("plugin executable"))
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyPackage(publicKey, bundle.ManifestJSON, bundle.Signature, bundle.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	if verified.PluginID != manifest.PluginID || key != (ArtifactKey{
		PluginID: manifest.PluginID, Version: manifest.Version, Platform: manifest.Platform, SHA256: verified.ArtifactSHA256,
	}) {
		t.Fatalf("verified manifest or artifact key mismatch: manifest=%+v key=%+v", verified, key)
	}
	bundle.Artifact[0] ^= 0xff
	if _, err := VerifyPackage(publicKey, bundle.ManifestJSON, bundle.Signature, bundle.Artifact); err == nil {
		t.Fatal("modified artifact passed signature package verification")
	}
}

func TestSignPackageRejectsInvalidKeyAndConflictingDigest(t *testing.T) {
	manifest := Manifest{
		PluginID: "health", Version: "1.2.0", Publisher: "test.publisher",
		Platform: Platform{OS: "linux", Arch: "amd64"}, Protocol: ProtocolRange{Min: 1, Max: 1},
		Collectors: []CollectorSpec{{ID: "system"}},
	}
	if _, _, err := SignPackage(ed25519.PrivateKey{1}, manifest, []byte("artifact")); err == nil {
		t.Fatal("invalid private key was accepted")
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	inconsistentKey := append(ed25519.PrivateKey(nil), privateKey...)
	inconsistentKey[len(inconsistentKey)-1] ^= 0xff
	if _, _, err := SignPackage(inconsistentKey, manifest, []byte("artifact")); err == nil {
		t.Fatal("internally inconsistent private key was accepted")
	}
	manifest.ArtifactSHA256 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if _, _, err := SignPackage(privateKey, manifest, []byte("artifact")); err == nil {
		t.Fatal("conflicting manifest digest was silently replaced")
	}
}

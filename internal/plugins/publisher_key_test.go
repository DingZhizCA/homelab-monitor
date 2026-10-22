package plugins

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPublisherPublicKeyReadsProtectedRawKey(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "publisher.pub")
	if err := os.WriteFile(path, publicKey, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPublisherPublicKey(path)
	if err != nil {
		t.Fatalf("load publisher key: %v", err)
	}
	if string(loaded) != string(publicKey) {
		t.Fatal("loaded publisher key differs from trusted bytes")
	}
}

func TestLoadPublisherPublicKeyRejectsUnsafeInputs(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "publisher.pub")
	if err := os.WriteFile(path, make([]byte, ed25519.PublicKeySize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPublisherPublicKey("relative.pub"); err == nil {
		t.Fatal("relative publisher key path was accepted")
	}
	if _, err := LoadPublisherPublicKey(path); err == nil {
		t.Fatal("publisher key with invalid length was accepted")
	}
	if err := os.WriteFile(path, make([]byte, ed25519.PublicKeySize), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPublisherPublicKey(path); err == nil {
		t.Fatal("group/other-writable publisher key was accepted")
	}
	link := filepath.Join(directory, "publisher-link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPublisherPublicKey(link); err == nil {
		t.Fatal("symlink publisher key was accepted")
	}
}

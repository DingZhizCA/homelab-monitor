/*
Package server tests device-token scoping, replacement, revocation, and parsing.
*/
package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/DingZhizCA/homelab-monitor/internal/testutil"
)

func TestDeviceCredentialsReplaceAndRevoke(t *testing.T) {
	credentials := NewDeviceCredentials()
	if err := credentials.Set("n1", "old-token"); err != nil {
		t.Fatal(err)
	}
	if !credentials.Authorize("n1", "old-token") {
		t.Fatal("configured credential was rejected")
	}
	if credentials.Authorize("n2", "old-token") {
		t.Fatal("credential was accepted for another device")
	}
	if err := credentials.Set("n1", "new-token"); err != nil {
		t.Fatal(err)
	}
	if credentials.Authorize("n1", "old-token") || !credentials.Authorize("n1", "new-token") {
		t.Fatal("credential replacement did not take effect")
	}
	if !credentials.Revoke("n1") || credentials.Revoke("n1") || credentials.Authorize("n1", "new-token") {
		t.Fatal("credential revocation did not take effect exactly once")
	}
}

func TestLoadDeviceCredentialsReadsPrivateDigestFile(t *testing.T) {
	root := testutil.TempDir(t)
	path := filepath.Join(root, "devices.json")
	digest := sha256.Sum256([]byte("long-random-device-token"))
	data := []byte(fmt.Sprintf("{\"devices\":[{\"device_id\":\"node-a\",\"token_sha256\":\"%s\"}]}", hex.EncodeToString(digest[:])))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	credentials, err := LoadDeviceCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	if !credentials.Authorize("node-a", "long-random-device-token") {
		t.Fatal("loaded verifier rejected matching token")
	}
	if credentials.Authorize("node-a", string(data)) {
		t.Fatal("credential file contents were accepted as token")
	}
}

func TestLoadDeviceCredentialsRejectsUnsafeAndInvalidFiles(t *testing.T) {
	root := testutil.TempDir(t)
	path := filepath.Join(root, "devices.json")
	if _, err := LoadDeviceCredentials("relative/devices.json"); err == nil {
		t.Fatal("relative credential path was accepted")
	}
	valid := []byte(`{"devices":[]}`)
	if err := os.WriteFile(path, valid, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDeviceCredentials(path); err == nil {
		t.Fatal("group-readable credential file was accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("another-token"))
	digestText := hex.EncodeToString(digest[:])
	for _, content := range []string{
		`{"devices":[{"device_id":"node-a","token_sha256":"bad"}]}`,
		`{"devices":[{"device_id":"node-a","token_sha256":"` + digestText + `"},{"device_id":"node-a","token_sha256":"` + digestText + `"}]}`,
		`{"devices":[{"device_id":" node-a ","token_sha256":"` + digestText + `"}]}`,
		`{"devices":[],"unexpected":true}`,
		`{"devices":[]} {}`,
		`{"devices":[],"devices":[]}`,
		`{"devices":[{"device_id":"node-a","device_id":"node-b","token_sha256":"` + digestText + `"}]}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadDeviceCredentials(path); err == nil {
			t.Errorf("invalid credential file was accepted: %q", content)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.json")
	if err := os.Symlink(filepath.Join(root, "missing"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDeviceCredentials(link); err == nil {
		t.Fatal("symbolic-link credential file was accepted")
	}
}

func TestBearerTokenParserRequiresSchemeAndCredential(t *testing.T) {
	for _, input := range []string{"", "token", "Basic token", "Bearer", "Bearer token extra"} {
		if _, ok := bearerToken(input); ok {
			t.Errorf("bearerToken(%q) unexpectedly succeeded", input)
		}
	}
	if token, ok := bearerToken("bearer token"); !ok || token != "token" {
		t.Fatalf("bearerToken() = %q, %v", token, ok)
	}
}

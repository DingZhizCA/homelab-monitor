package plugins

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DingZhizCA/homelab-monitor/internal/testutil"
)

func TestLoadGrantAuthorizerReadsStrictPrivateAllowList(t *testing.T) {
	directory := filepath.Join(testutil.TempDir(t), "policy")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "plugins.json")
	contents := `{"grants":[{"plugin_id":"network","publisher":"trusted.example","permissions":["read:proc"]}]}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	authorizer, err := LoadGrantAuthorizer(path)
	if err != nil {
		t.Fatal(err)
	}
	manifest := validManifest()
	manifest.PluginID = "network"
	manifest.Publisher = "trusted.example"
	manifest.Permissions = []string{"read:proc"}
	if err := authorizer.AuthorizePlugin(manifest); err != nil {
		t.Fatalf("loaded permission was not authorized: %v", err)
	}

	for _, invalid := range []string{
		`{"unknown":[]}`,
		`{"grants":[]} {"grants":[]}`,
		`{"grants":[],"grants":[]}`,
		`{"grants":[{"plugin_id":"network","plugin_id":"other","publisher":"trusted.example","permissions":[]}]}`,
		`{"grants":[{"plugin_id":"network","publisher":"trusted.example","permissions":["read:proc","read:proc"]}]}`,
	} {
		if err := os.WriteFile(path, []byte(invalid), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadGrantAuthorizer(path); err == nil {
			t.Fatalf("invalid permission file was accepted: %s", invalid)
		}
	}
}

func TestLoadGrantAuthorizerRejectsUnsafeFileAndPath(t *testing.T) {
	root := testutil.TempDir(t)
	private := filepath.Join(root, "private")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(private, "plugins.json")
	if err := os.WriteFile(path, []byte(`{"grants":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadGrantAuthorizer(path); err == nil {
		t.Fatal("group-readable grant file was accepted")
	}
	if _, err := LoadGrantAuthorizer("relative/plugins.json"); err == nil {
		t.Fatal("relative grant file path was accepted")
	}
	link := filepath.Join(private, "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadGrantAuthorizer(link); err == nil {
		t.Fatal("symbolic-link grant file was accepted")
	}
	if err := os.WriteFile(path, make([]byte, maxPermissionFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadGrantAuthorizer(path); err == nil {
		t.Fatal("oversized grant file was accepted")
	}
	permissive := filepath.Join(root, "permissive")
	if err := os.Mkdir(permissive, 0o775); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(permissive, 0o775); err != nil {
		t.Fatal(err)
	}
	permissivePath := filepath.Join(permissive, "plugins.json")
	if err := os.WriteFile(permissivePath, []byte(`{"grants":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadGrantAuthorizer(permissivePath); err == nil {
		t.Fatal("grant in a group-writable directory was accepted")
	}
}

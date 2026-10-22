/*
Package testutil contains test-only helpers shared across internal packages.

TempDir locates the repository root and creates isolated test data there, so
workspace-restricted test runs do not write fixtures to a system temp directory.
*/
package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

// TempDir creates a per-test directory beneath the repository root.
func TempDir(t testing.TB) string {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot := workingDirectory
	for {
		if _, err := os.Stat(filepath.Join(repositoryRoot, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(repositoryRoot)
		if parent == repositoryRoot {
			t.Fatal("could not locate repository root from test working directory")
		}
		repositoryRoot = parent
	}
	directory, err := os.MkdirTemp(repositoryRoot, ".homelab-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Errorf("remove test directory: %v", err)
		}
	})
	return directory
}

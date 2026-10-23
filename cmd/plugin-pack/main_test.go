package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/DingZhizCA/homelab-monitor/internal/plugins"
)

func TestPackWritesVerifiablePrivateBundle(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "manifest.json")
	artifactPath := filepath.Join(root, "plugin")
	privateKeyPath := filepath.Join(root, "publisher.key")
	outputPath := filepath.Join(root, "bundle")
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := plugins.Manifest{
		PluginID: "health", Version: "1.0.0", Publisher: "example.test",
		Platform: plugins.Platform{OS: "linux", Arch: "amd64"}, Protocol: plugins.ProtocolRange{Min: 1, Max: 1},
		Collectors: []plugins.CollectorSpec{{ID: "system"}},
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, manifestPath, manifestJSON, 0o600)
	writeTestFile(t, artifactPath, []byte("signed executable"), 0o600)
	writeTestFile(t, privateKeyPath, privateKey, 0o600)
	if err := pack(manifestPath, artifactPath, privateKeyPath, outputPath, 1024); err != nil {
		t.Fatal(err)
	}
	bundle := plugins.PackageArtifact{
		ManifestJSON: readTestFile(t, filepath.Join(outputPath, "manifest.json")),
		Signature:    readTestFile(t, filepath.Join(outputPath, "manifest.sig")),
		Artifact:     readTestFile(t, filepath.Join(outputPath, "artifact")),
	}
	if _, err := plugins.VerifyPackage(publicKey, bundle.ManifestJSON, bundle.Signature, bundle.Artifact); err != nil {
		t.Fatalf("created bundle did not verify: %v", err)
	}
	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("output directory mode=%v", info.Mode().Perm())
	}
	if err := pack(manifestPath, artifactPath, privateKeyPath, outputPath, 1024); err == nil {
		t.Fatal("pack overwrote an existing output directory")
	}
}

func TestPackEnforcesArtifactLimitAndPrivateKeyPermissions(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "manifest.json")
	artifactPath := filepath.Join(root, "plugin")
	privateKeyPath := filepath.Join(root, "publisher.key")
	outputPath := filepath.Join(root, "bundle")
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := plugins.Manifest{
		PluginID: "health", Version: "1.0.0", Publisher: "example.test",
		Platform: plugins.Platform{OS: "linux", Arch: "amd64"}, Protocol: plugins.ProtocolRange{Min: 1, Max: 1},
		Collectors: []plugins.CollectorSpec{{ID: "system"}},
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, manifestPath, manifestJSON, 0o600)
	writeTestFile(t, artifactPath, []byte("oversized"), 0o600)
	writeTestFile(t, privateKeyPath, privateKey, 0o600)
	if err := pack(manifestPath, artifactPath, privateKeyPath, outputPath, 4); err == nil {
		t.Fatal("oversized artifact was accepted")
	}
	if _, err := os.Lstat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("failed package left output directory behind: %v", err)
	}
	if err := os.Chmod(privateKeyPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := pack(manifestPath, artifactPath, privateKeyPath, outputPath, 1024); err == nil {
		t.Fatal("group/world-readable private key was accepted")
	}
	if err := os.Chmod(privateKeyPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o770); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateKey(privateKeyPath); err == nil {
		t.Fatal("group-writable private key directory was accepted")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	keyLink := filepath.Join(root, "publisher-link.key")
	if err := os.Symlink(privateKeyPath, keyLink); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateKey(keyLink); err == nil {
		t.Fatal("symbolic-link private key was accepted")
	}
	directoryLink := filepath.Join(t.TempDir(), "key-directory-link")
	if err := os.Symlink(root, directoryLink); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateKey(filepath.Join(directoryLink, "publisher.key")); err == nil {
		t.Fatal("private key reached through a symbolic-link directory was accepted")
	}
}

func TestPublishBundleImportsImmutablePackageRepository(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	bundleDirectory := filepath.Join(root, "bundle")
	repositoryDirectory := filepath.Join(root, "repository")
	if err := os.Mkdir(bundleDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(repositoryDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	artifact := []byte("signed executable")
	manifest := plugins.Manifest{
		PluginID: "health", Version: "1.0.0", Publisher: "example.test",
		Platform: plugins.Platform{OS: "linux", Arch: "amd64"}, Protocol: plugins.ProtocolRange{Min: 1, Max: 1},
		Collectors: []plugins.CollectorSpec{{ID: "system"}},
	}
	bundle, key, err := plugins.SignPackage(privateKey, manifest, artifact)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(bundleDirectory, "manifest.json"), bundle.ManifestJSON, 0o600)
	writeTestFile(t, filepath.Join(bundleDirectory, "manifest.sig"), bundle.Signature, 0o600)
	writeTestFile(t, filepath.Join(bundleDirectory, "artifact"), bundle.Artifact, 0o600)
	limits := plugins.ArtifactRepositoryLimits{MaxArtifactBytes: 1024, MaxRepositoryBytes: 1 << 20, MaxPackages: 16, MaxScanEntries: 256}
	if err := publishBundle(bundleDirectory, repositoryDirectory, limits); err != nil {
		t.Fatal(err)
	}
	repository, err := plugins.NewFileArtifactRepository(repositoryDirectory, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	stored, err := repository.Get(key)
	if err != nil || !bytes.Equal(stored.Artifact, artifact) {
		t.Fatalf("stored artifact=%q err=%v", stored.Artifact, err)
	}
	if err := publishBundle(bundleDirectory, repositoryDirectory, limits); err != nil {
		t.Fatalf("idempotent publish: %v", err)
	}
}

func writeTestFile(t *testing.T, path string, contents []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, contents, mode); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

package plugins

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/testutil"
)

func TestStagerWritesVerifiedPackageImmutably(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	artifact := []byte("safe mock plugin artifact")
	digest := sha256.Sum256(artifact)
	manifest := validManifest()
	manifest.ArtifactSHA256 = hex.EncodeToString(digest[:])
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(privateKey, manifestJSON)
	rootPath := filepath.Join(testutil.TempDir(t), "staging")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	stager, err := NewStager(rootPath, publicKey, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stager.Close() })

	staged, err := stager.Stage(manifestJSON, signature, artifact)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := stager.VerifyStaged(staged.RelativePath)
	if err != nil || verified.Manifest.PluginID != manifest.PluginID {
		t.Fatalf("verify staged package = %+v, %v", verified, err)
	}
	if _, err := stager.VerifyStaged("../../outside"); err == nil {
		t.Fatal("staged path escaping the root was accepted")
	}
	artifactPath := filepath.Join(rootPath, filepath.FromSlash(staged.RelativePath), "artifact")
	got, err := os.ReadFile(artifactPath)
	if err != nil || string(got) != string(artifact) {
		t.Fatalf("staged artifact = %q, %v", got, err)
	}
	info, err := os.Stat(artifactPath)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("staged artifact permissions = %v, %v", info, err)
	}
	if err := os.Chmod(artifactPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := stager.VerifyStaged(staged.RelativePath); err == nil {
		t.Fatal("staged artifact with executable permission removed passed verification")
	}
	if err := os.Chmod(artifactPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := stager.Stage(manifestJSON, signature, artifact); err != nil {
		t.Fatalf("identical retry should succeed: %v", err)
	}
	if err := os.WriteFile(artifactPath, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := stager.VerifyStaged(staged.RelativePath); err == nil {
		t.Fatal("modified staged artifact passed independent verification")
	}
	if _, err := stager.Stage(manifestJSON, signature, artifact); err == nil {
		t.Fatal("modified existing package contents were accepted")
	}
}

func TestBoundedStagerRejectsCapacityAndPrunesOnlyOldUnprotectedPackages(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(testutil.TempDir(t), "staging")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	stager, err := NewBoundedStager(rootPath, publicKey, StagingLimits{
		MaxArtifactBytes: 1024, MaxStagingBytes: 4096, MaxPackages: 2, MaxScanEntries: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stager.Close()
	stageVersion := func(version string) StagedPackage {
		manifest := validManifest()
		manifest.Version = version
		manifest.ArtifactSHA256 = ""
		bundle, _, err := SignPackage(privateKey, manifest, []byte("artifact-"+version))
		if err != nil {
			t.Fatal(err)
		}
		staged, err := stager.Stage(bundle.ManifestJSON, bundle.Signature, bundle.Artifact)
		if err != nil {
			t.Fatal(err)
		}
		return staged
	}
	current := stageVersion("1.0.0")
	orphan := stageVersion("2.0.0")
	retry := mustSignedBundle(t, privateKey, "1.0.0")
	if _, err := stager.Stage(retry.ManifestJSON, retry.Signature, retry.Artifact); err != nil {
		t.Fatalf("identical package retry failed at capacity: %v", err)
	}
	overflow := mustSignedBundle(t, privateKey, "3.0.0")
	if _, err := stager.Stage(overflow.ManifestJSON, overflow.Signature, overflow.Artifact); err == nil {
		t.Fatal("staging accepted a package beyond its count limit")
	}
	old := time.Now().Add(-2 * time.Hour)
	for _, staged := range []StagedPackage{current, orphan} {
		if err := os.Chtimes(filepath.Join(rootPath, filepath.FromSlash(staged.RelativePath)), old, old); err != nil {
			t.Fatal(err)
		}
	}
	result, err := stager.Prune(context.Background(), map[string]struct{}{current.RelativePath: {}}, time.Now().Add(-time.Hour), 1)
	if err != nil || result.DeletedPackages != 1 {
		t.Fatalf("prune result=%+v err=%v", result, err)
	}
	if _, err := stager.VerifyStaged(current.RelativePath); err != nil {
		t.Fatalf("protected current package removed: %v", err)
	}
	if _, err := stager.VerifyStaged(orphan.RelativePath); err == nil {
		t.Fatal("old orphan package remains after cleanup")
	}
}

func TestBoundedStagerLocksAndBoundsStartupScan(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(testutil.TempDir(t), "staging")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	limits := StagingLimits{MaxArtifactBytes: 1024, MaxStagingBytes: 4096, MaxPackages: 1, MaxScanEntries: 9}
	first, err := NewBoundedStager(rootPath, publicKey, limits)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := NewBoundedStager(rootPath, publicKey, limits); err == nil {
		_ = second.Close()
		t.Fatal("staging directory accepted a second writer")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"} {
		if err := os.Mkdir(filepath.Join(rootPath, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if stager, err := NewBoundedStager(rootPath, publicKey, limits); err == nil {
		_ = stager.Close()
		t.Fatal("staging tree beyond its scan limit was accepted")
	}
}

func mustSignedBundle(t *testing.T, privateKey ed25519.PrivateKey, version string) PackageArtifact {
	t.Helper()
	manifest := validManifest()
	manifest.Version = version
	manifest.ArtifactSHA256 = ""
	bundle, _, err := SignPackage(privateKey, manifest, []byte("artifact-"+version))
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func TestStagerRejectsVerificationAfterClose(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(testutil.TempDir(t), "staging")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	stager, err := NewStager(rootPath, publicKey, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := stager.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := stager.VerifyStaged("package"); err == nil {
		t.Fatal("verification succeeded after the stager was closed")
	}
}

func TestStagerEnforcesConfiguredArtifactLimitOnWriteAndRead(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(testutil.TempDir(t), "staging")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	const maxArtifactSize = 16
	stager, err := NewStager(rootPath, publicKey, maxArtifactSize)
	if err != nil {
		t.Fatal(err)
	}
	defer stager.Close()

	manifest := validManifest()
	manifest.ArtifactSHA256 = ""
	bundle, _, err := SignPackage(privateKey, manifest, []byte("small"))
	if err != nil {
		t.Fatal(err)
	}
	staged, err := stager.Stage(bundle.ManifestJSON, bundle.Signature, bundle.Artifact)
	if err != nil {
		t.Fatalf("stage in-limit artifact: %v", err)
	}
	artifactPath := filepath.Join(rootPath, filepath.FromSlash(staged.RelativePath), "artifact")
	if err := os.WriteFile(artifactPath, make([]byte, maxArtifactSize+1), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := stager.VerifyStaged(staged.RelativePath); err == nil {
		t.Fatal("oversized staged artifact was read and accepted")
	}

	manifest.ArtifactSHA256 = ""
	oversized, _, err := SignPackage(privateKey, manifest, make([]byte, maxArtifactSize+1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stager.Stage(oversized.ManifestJSON, oversized.Signature, oversized.Artifact); err == nil {
		t.Fatal("oversized artifact was staged")
	}
}

func TestStagerRejectsUnsafeRootAndInvalidPackageBeforeWriting(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	permissiveRoot := filepath.Join(testutil.TempDir(t), "permissive")
	if err := os.Mkdir(permissiveRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(permissiveRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStager(permissiveRoot, publicKey, 1<<20); err == nil {
		t.Fatal("staging root accessible to group or other users was accepted")
	}
	privateTarget := filepath.Join(testutil.TempDir(t), "private-target")
	if err := os.Mkdir(privateTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStager(privateTarget, publicKey, 0); err == nil {
		t.Fatal("stager without an explicit artifact size limit was accepted")
	}
	symlinkRoot := filepath.Join(testutil.TempDir(t), "staging-link")
	if err := os.Symlink(privateTarget, symlinkRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStager(symlinkRoot, publicKey, 1<<20); err == nil {
		t.Fatal("symbolic-link staging root was accepted")
	}

	rootPath := filepath.Join(testutil.TempDir(t), "staging")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	stager, err := NewStager(rootPath, publicKey, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer stager.Close()
	manifestJSON, err := json.Marshal(validManifest())
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(privateKey, manifestJSON)
	if _, err := stager.Stage(manifestJSON, signature, []byte("wrong artifact")); err == nil {
		t.Fatal("artifact with a mismatched digest was staged")
	}
	entries, err := os.ReadDir(rootPath)
	if err != nil || len(entries) != 0 {
		t.Fatalf("invalid package left staging entries: %v, %v", entries, err)
	}
}

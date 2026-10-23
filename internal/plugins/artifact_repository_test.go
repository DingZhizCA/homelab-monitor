package plugins

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestFileArtifactRepositoryStoresImmutablePackage(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Chmod(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	artifact := []byte("signed plugin bytes")
	digest := sha256.Sum256(artifact)
	manifest := validManifest()
	manifest.ArtifactSHA256 = hex.EncodeToString(digest[:])
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	bundle := PackageArtifact{ManifestJSON: manifestJSON, Signature: make([]byte, 64), Artifact: artifact}
	key := ArtifactKey{PluginID: manifest.PluginID, Version: manifest.Version, Platform: manifest.Platform, SHA256: manifest.ArtifactSHA256}
	repository, err := NewFileArtifactRepository(rootPath, artifactTestLimits(1024))
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}
	defer repository.Close()
	if err := repository.Put(key, bundle); err != nil {
		t.Fatalf("put package: %v", err)
	}
	if err := repository.Put(key, bundle); err != nil {
		t.Fatalf("identical package retry: %v", err)
	}
	got, err := repository.Get(key)
	if err != nil {
		t.Fatalf("get package: %v", err)
	}
	if !packageArtifactsEqual(got, bundle) {
		t.Fatal("stored package bytes differ from the imported bundle")
	}
	conflicting := bundle
	conflicting.Signature = make([]byte, 64)
	conflicting.Signature[0] = 1
	if err := repository.Put(key, conflicting); err == nil {
		t.Fatal("conflicting immutable package replacement was accepted")
	}
}

func TestFileArtifactRepositoryConcurrentIdenticalWritersAreIdempotent(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Chmod(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	artifact := []byte("concurrent signed plugin bytes")
	digest := sha256.Sum256(artifact)
	manifest := validManifest()
	manifest.ArtifactSHA256 = hex.EncodeToString(digest[:])
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	bundle := PackageArtifact{ManifestJSON: manifestJSON, Signature: make([]byte, 64), Artifact: artifact}
	key := ArtifactKey{PluginID: manifest.PluginID, Version: manifest.Version, Platform: manifest.Platform, SHA256: manifest.ArtifactSHA256}
	first, err := NewFileArtifactRepository(rootPath, artifactTestLimits(1024))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := NewFileArtifactRepository(rootPath, artifactTestLimits(1024))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	start := make(chan struct{})
	errorsFound := make(chan error, 2)
	var group sync.WaitGroup
	for _, repository := range []*FileArtifactRepository{first, second} {
		group.Add(1)
		go func(repository *FileArtifactRepository) {
			defer group.Done()
			<-start
			errorsFound <- repository.Put(key, bundle)
		}(repository)
	}
	close(start)
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatalf("concurrent identical Put() failed: %v", err)
		}
	}
	got, err := first.Get(key)
	if err != nil || !packageArtifactsEqual(got, bundle) {
		t.Fatalf("concurrent package readback=%+v err=%v", got, err)
	}
}

func TestFileArtifactRepositoryConcurrentConflictingWritersRemainImmutable(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Chmod(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	artifact := []byte("conflicting concurrent plugin bytes")
	digest := sha256.Sum256(artifact)
	manifest := validManifest()
	manifest.ArtifactSHA256 = hex.EncodeToString(digest[:])
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	key := ArtifactKey{PluginID: manifest.PluginID, Version: manifest.Version, Platform: manifest.Platform, SHA256: manifest.ArtifactSHA256}
	winners := []PackageArtifact{
		{ManifestJSON: manifestJSON, Signature: bytes.Repeat([]byte{1}, 64), Artifact: artifact},
		{ManifestJSON: manifestJSON, Signature: bytes.Repeat([]byte{2}, 64), Artifact: artifact},
	}
	repositories := make([]*FileArtifactRepository, 2)
	for index := range repositories {
		repositories[index], err = NewFileArtifactRepository(rootPath, artifactTestLimits(1024))
		if err != nil {
			t.Fatal(err)
		}
		defer repositories[index].Close()
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	for index, repository := range repositories {
		group.Add(1)
		go func(index int, repository *FileArtifactRepository) {
			defer group.Done()
			<-start
			results <- repository.Put(key, winners[index])
		}(index, repository)
	}
	close(start)
	group.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("conflicting concurrent successful writes=%d, want exactly one", successes)
	}
	stored, err := repositories[0].Get(key)
	if err != nil {
		t.Fatalf("read winning immutable package: %v", err)
	}
	if !packageArtifactsEqual(stored, winners[0]) && !packageArtifactsEqual(stored, winners[1]) {
		t.Fatal("stored package is not either complete concurrent input")
	}
}

func TestFileArtifactRepositoryRejectsUnsafeOrOversizedPackages(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Chmod(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileArtifactRepository(rootPath, artifactTestLimits(0)); err == nil {
		t.Fatal("missing artifact size limit was accepted")
	}
	repository, err := NewFileArtifactRepository(rootPath, artifactTestLimits(2))
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}
	defer repository.Close()
	artifact := []byte("too large")
	digest := sha256.Sum256(artifact)
	manifest := validManifest()
	manifest.ArtifactSHA256 = hex.EncodeToString(digest[:])
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	bundle := PackageArtifact{ManifestJSON: manifestJSON, Signature: make([]byte, 64), Artifact: artifact}
	key := ArtifactKey{PluginID: manifest.PluginID, Version: manifest.Version, Platform: manifest.Platform, SHA256: manifest.ArtifactSHA256}
	if err := repository.Put(key, bundle); err == nil {
		t.Fatal("oversized plugin artifact was accepted")
	}
	key.PluginID = "../escape"
	if _, err := repository.Get(key); err == nil {
		t.Fatal("unsafe artifact key was accepted")
	}
}

func TestFileArtifactRepositoryEnforcesTotalBytesAndPackageCount(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Chmod(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	firstKey, firstBundle := artifactRepositoryTestBundle(t, "1.0.0", []byte("first"))
	secondKey, secondBundle := artifactRepositoryTestBundle(t, "2.0.0", []byte("second"))
	firstBytes := int64(len(firstBundle.ManifestJSON) + len(firstBundle.Signature) + len(firstBundle.Artifact))
	repository, err := NewFileArtifactRepository(rootPath, ArtifactRepositoryLimits{
		MaxArtifactBytes: 64, MaxRepositoryBytes: firstBytes, MaxPackages: 1, MaxScanEntries: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	if err := repository.Put(firstKey, firstBundle); err != nil {
		t.Fatal(err)
	}
	if err := repository.Put(firstKey, firstBundle); err != nil {
		t.Fatalf("idempotent import was blocked at capacity: %v", err)
	}
	if err := repository.Put(secondKey, secondBundle); err == nil {
		t.Fatal("package beyond repository capacity was accepted")
	}
}

func TestFileArtifactRepositoryPrunesOnlyOldUnprotectedPackages(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Chmod(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	repository, err := NewFileArtifactRepository(rootPath, artifactTestLimits(1024))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	protectedKey, protectedBundle := artifactRepositoryTestBundle(t, "1.0.0", []byte("protected"))
	removedKey, removedBundle := artifactRepositoryTestBundle(t, "2.0.0", []byte("removed"))
	recentKey, recentBundle := artifactRepositoryTestBundle(t, "3.0.0", []byte("recent"))
	for key, bundle := range map[ArtifactKey]PackageArtifact{protectedKey: protectedBundle, removedKey: removedBundle, recentKey: recentBundle} {
		if err := repository.Put(key, bundle); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	old := now.Add(-2 * time.Hour)
	for _, key := range []ArtifactKey{protectedKey, removedKey} {
		if err := os.Chtimes(filepath.Join(rootPath, artifactRelativePath(key)), old, old); err != nil {
			t.Fatal(err)
		}
	}
	result, err := repository.Prune(context.Background(), map[ArtifactKey]struct{}{protectedKey: {}}, now.Add(-time.Hour), 1)
	if err != nil || result.DeletedPackages != 1 || result.DeletedBytes == 0 {
		t.Fatalf("prune result=%+v err=%v", result, err)
	}
	if _, err := repository.Get(protectedKey); err != nil {
		t.Fatalf("protected artifact was removed: %v", err)
	}
	if _, err := repository.Get(recentKey); err != nil {
		t.Fatalf("recent artifact was removed: %v", err)
	}
	if _, err := repository.Get(removedKey); err == nil {
		t.Fatal("old unprotected artifact was retained")
	}
}

func TestFileArtifactRepositorySerializesQuotaAcrossInstances(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Chmod(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	limits := artifactTestLimits(1024)
	limits.MaxPackages = 1
	first, err := NewFileArtifactRepository(rootPath, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := NewFileArtifactRepository(rootPath, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	firstKey, firstBundle := artifactRepositoryTestBundle(t, "1.0.0", []byte("first"))
	secondKey, secondBundle := artifactRepositoryTestBundle(t, "2.0.0", []byte("second"))
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() { <-start; results <- first.Put(firstKey, firstBundle) }()
	go func() { <-start; results <- second.Put(secondKey, secondBundle) }()
	close(start)
	successes := 0
	for range 2 {
		if err := <-results; err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful quota-constrained imports=%d, want one", successes)
	}
}

func TestFileArtifactRepositoryPruneHonorsContextWhileMutationLocked(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Chmod(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := NewFileArtifactRepository(rootPath, artifactTestLimits(1024))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := NewFileArtifactRepository(rootPath, artifactTestLimits(1024))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := first.lockMutation(); err != nil {
		t.Fatal(err)
	}
	defer first.unlockMutation()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := second.Prune(ctx, nil, time.Now(), 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("prune lock wait error=%v", err)
	}
}

func artifactRepositoryTestBundle(t *testing.T, version string, artifact []byte) (ArtifactKey, PackageArtifact) {
	t.Helper()
	digest := sha256.Sum256(artifact)
	manifest := validManifest()
	manifest.Version = version
	manifest.ArtifactSHA256 = hex.EncodeToString(digest[:])
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return ArtifactKey{PluginID: manifest.PluginID, Version: manifest.Version, Platform: manifest.Platform, SHA256: manifest.ArtifactSHA256}, PackageArtifact{
		ManifestJSON: manifestJSON, Signature: make([]byte, 64), Artifact: artifact,
	}
}

func artifactTestLimits(maxArtifactBytes int64) ArtifactRepositoryLimits {
	return ArtifactRepositoryLimits{MaxArtifactBytes: maxArtifactBytes, MaxRepositoryBytes: 1 << 20, MaxPackages: 16, MaxScanEntries: 256}
}

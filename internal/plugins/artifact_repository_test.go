package plugins

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"sync"
	"testing"
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
	repository, err := NewFileArtifactRepository(rootPath, 1024)
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
	first, err := NewFileArtifactRepository(rootPath, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := NewFileArtifactRepository(rootPath, 1024)
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
		repositories[index], err = NewFileArtifactRepository(rootPath, 1024)
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
	if _, err := NewFileArtifactRepository(rootPath, 0); err == nil {
		t.Fatal("missing artifact size limit was accepted")
	}
	repository, err := NewFileArtifactRepository(rootPath, 2)
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

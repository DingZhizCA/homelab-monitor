/*
Package plugins tests bounded artifact cleanup against an explicit Desired State
reference source without running a Server scheduler.
*/
package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type artifactReferenceStub struct {
	keys []ArtifactKey
	err  error
}

func (s artifactReferenceStub) DesiredArtifactKeysContext(context.Context) ([]ArtifactKey, error) {
	return s.keys, s.err
}

func TestArtifactMaintenanceProtectsDesiredPackagesAndReportsCleanup(t *testing.T) {
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
	orphanKey, orphanBundle := artifactRepositoryTestBundle(t, "2.0.0", []byte("orphan"))
	for key, bundle := range map[ArtifactKey]PackageArtifact{protectedKey: protectedBundle, orphanKey: orphanBundle} {
		if err := repository.Put(key, bundle); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 10, 23, 5, 0, 0, 0, time.UTC)
	old := now.Add(-2 * time.Hour)
	for _, key := range []ArtifactKey{protectedKey, orphanKey} {
		if err := os.Chtimes(filepath.Join(rootPath, artifactRelativePath(key)), old, old); err != nil {
			t.Fatal(err)
		}
	}
	var observed ArtifactMaintenanceObservation
	manager, err := NewArtifactMaintenance(repository, artifactReferenceStub{keys: []ArtifactKey{protectedKey}}, &sync.Mutex{}, ArtifactMaintenanceConfig{
		Interval: time.Hour, OrphanGrace: time.Hour, DeleteLimit: 1, Timeout: time.Second,
	}, func(value ArtifactMaintenanceObservation) { observed = value })
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return now }
	result := manager.Cleanup(context.Background())
	if result.Err != nil || result.DeletedPackages != 1 || observed.DeletedPackages != 1 {
		t.Fatalf("cleanup result=%+v observed=%+v", result, observed)
	}
	if _, err := repository.Get(protectedKey); err != nil {
		t.Fatalf("Desired package was removed: %v", err)
	}
	if _, err := repository.Get(orphanKey); err == nil {
		t.Fatal("expired orphan package was retained")
	}
}

func TestArtifactMaintenanceFailsClosedWhenReferencesUnavailable(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Chmod(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	repository, err := NewFileArtifactRepository(rootPath, artifactTestLimits(1024))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	key, bundle := artifactRepositoryTestBundle(t, "1.0.0", []byte("retained"))
	if err := repository.Put(key, bundle); err != nil {
		t.Fatal(err)
	}
	manager, err := NewArtifactMaintenance(repository, artifactReferenceStub{err: errors.New("database unavailable")}, &sync.Mutex{}, ArtifactMaintenanceConfig{
		Interval: time.Hour, OrphanGrace: time.Hour, DeleteLimit: 1, Timeout: time.Second,
	}, func(ArtifactMaintenanceObservation) {})
	if err != nil {
		t.Fatal(err)
	}
	if result := manager.Cleanup(context.Background()); result.Err == nil {
		t.Fatal("cleanup succeeded without a protected reference snapshot")
	}
	if _, err := repository.Get(key); err != nil {
		t.Fatalf("package was removed after reference failure: %v", err)
	}
}

/*
Package plugins stores and retrieves immutable signed plugin package artifacts.

The repository owns the local package layout and byte-integrity checks. Publisher
trust remains an Agent decision, and the repository does not install or execute
the returned artifact.
*/
package plugins

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	maxArtifactManifestBytes = 1 << 20
	artifactRepositoryLock   = ".repository.lock"
)

// ArtifactRepositoryLimits bounds individual packages and the complete on-disk tree.
type ArtifactRepositoryLimits struct {
	MaxArtifactBytes   int64
	MaxRepositoryBytes int64
	MaxPackages        int
	MaxScanEntries     int
}

// PackageArtifact contains the exact signed metadata and executable bytes for one package.
type PackageArtifact struct {
	ManifestJSON []byte
	Signature    []byte
	Artifact     []byte
}

// ArtifactRepository reads immutable packages by their content-addressed identity.
type ArtifactRepository interface {
	Get(ArtifactKey) (PackageArtifact, error)
}

// FileArtifactRepository stores immutable plugin packages below a private directory.
type FileArtifactRepository struct {
	mu       sync.RWMutex
	root     *os.Root
	rootPath string
	lock     *os.File
	limits   ArtifactRepositoryLimits
	closed   bool
}

// ArtifactPruneResult records one bounded repository cleanup pass.
type ArtifactPruneResult struct {
	ScannedEntries  int
	Packages        int
	UsedBytes       int64
	DeletedPackages int
	DeletedBytes    int64
}

type artifactRepositoryScan struct {
	ArtifactPruneResult
	candidates []artifactCleanupCandidate
}

type artifactCleanupCandidate struct {
	key      ArtifactKey
	path     string
	modified time.Time
}

// NewFileArtifactRepository requires a private directory and explicit capacity bounds.
func NewFileArtifactRepository(rootPath string, limits ArtifactRepositoryLimits) (*FileArtifactRepository, error) {
	if !filepath.IsAbs(rootPath) {
		return nil, errors.New("plugin artifact repository path must be absolute")
	}
	if limits.MaxArtifactBytes < 1 || limits.MaxRepositoryBytes < limits.MaxArtifactBytes || limits.MaxPackages < 1 || limits.MaxScanEntries < limits.MaxPackages {
		return nil, errors.New("plugin artifact repository limits must be positive and ordered")
	}
	info, err := os.Lstat(rootPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("plugin artifact repository must be an existing private non-symlink directory")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open plugin artifact repository: %w", err)
	}
	lock, err := openArtifactRepositoryLock(root)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	repository := &FileArtifactRepository{root: root, rootPath: rootPath, lock: lock, limits: limits}
	if err := repository.withMutationLock(func() error {
		if err := repository.cleanupPendingLocked(context.Background()); err != nil {
			return err
		}
		_, err := repository.scanLocked(context.Background())
		return err
	}); err != nil {
		_ = repository.Close()
		return nil, err
	}
	return repository, nil
}

// Put validates package identity and stores it without replacing an existing artifact.
func (r *FileArtifactRepository) Put(key ArtifactKey, bundle PackageArtifact) error {
	if err := key.Validate(); err != nil {
		return err
	}
	if int64(len(bundle.Artifact)) > r.limits.MaxArtifactBytes {
		return errors.New("plugin artifact exceeds repository size limit")
	}
	if len(bundle.ManifestJSON) > maxArtifactManifestBytes {
		return errors.New("plugin manifest exceeds repository size limit")
	}
	manifest, err := decodeCanonicalManifest(bundle.ManifestJSON)
	if err != nil {
		return err
	}
	if len(bundle.Signature) != ed25519.SignatureSize {
		return errors.New("plugin manifest signature has an invalid length")
	}
	if manifest.PluginID != key.PluginID || manifest.Version != key.Version || manifest.Platform != key.Platform || manifest.ArtifactSHA256 != key.SHA256 {
		return errors.New("artifact key does not match its signed manifest identity")
	}
	digest := sha256.Sum256(bundle.Artifact)
	if hex.EncodeToString(digest[:]) != key.SHA256 {
		return errors.New("plugin artifact digest does not match artifact key")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("plugin artifact repository is closed")
	}
	if err := r.lockMutation(); err != nil {
		return err
	}
	defer r.unlockMutation()
	if err := r.cleanupPendingLocked(context.Background()); err != nil {
		return err
	}
	path := artifactRelativePath(key)
	if info, err := r.root.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
			return errors.New("existing artifact path is not a private directory")
		}
		existing, err := r.getLocked(key)
		if err != nil || !packageArtifactsEqual(existing, bundle) {
			return errors.New("artifact path already exists with incomplete or conflicting contents")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect plugin artifact: %w", err)
	}
	usage, err := r.scanLocked(context.Background())
	if err != nil {
		return err
	}
	incomingBytes := int64(len(bundle.ManifestJSON) + len(bundle.Signature) + len(bundle.Artifact))
	if usage.Packages >= r.limits.MaxPackages || incomingBytes > r.limits.MaxRepositoryBytes-usage.UsedBytes {
		return errors.New("plugin artifact repository capacity reached")
	}
	if err := r.ensurePrivateDirectories(filepath.Dir(path)); err != nil {
		return err
	}
	temporaryPath, err := createPrivateArtifactDirectory(r.root)
	if err != nil {
		return fmt.Errorf("create temporary plugin artifact directory: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = r.root.RemoveAll(temporaryPath)
		}
	}()
	for name, contents := range map[string][]byte{
		"manifest.json": bundle.ManifestJSON,
		"manifest.sig":  bundle.Signature,
		"artifact":      bundle.Artifact,
	} {
		if err := writeExclusive(r.root, filepath.Join(temporaryPath, name), contents, 0o600); err != nil {
			return err
		}
	}
	directory, err := r.root.Open(temporaryPath)
	if err != nil {
		return fmt.Errorf("open plugin artifact directory: %w", err)
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return fmt.Errorf("sync plugin artifact directory: %w", err)
	}
	if err := directory.Close(); err != nil {
		return fmt.Errorf("close plugin artifact directory: %w", err)
	}
	if err := r.root.Rename(temporaryPath, path); err != nil {
		if existing, getErr := r.getLocked(key); getErr == nil && packageArtifactsEqual(existing, bundle) {
			return nil
		}
		return fmt.Errorf("publish immutable plugin artifact: %w", err)
	}
	parent, err := r.root.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open plugin artifact parent directory: %w", err)
	}
	if err := parent.Sync(); err != nil {
		_ = parent.Close()
		return fmt.Errorf("sync plugin artifact parent directory: %w", err)
	}
	if err := parent.Close(); err != nil {
		return fmt.Errorf("close plugin artifact parent directory: %w", err)
	}
	cleanup = false
	return nil
}

// Prune removes only old complete packages that are absent from the protected set.
func (r *FileArtifactRepository) Prune(ctx context.Context, protected map[ArtifactKey]struct{}, olderThan time.Time, maxDeletes int) (ArtifactPruneResult, error) {
	if ctx == nil || olderThan.IsZero() || maxDeletes < 1 {
		return ArtifactPruneResult{}, errors.New("plugin artifact cleanup requires context, cutoff, and delete limit")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ArtifactPruneResult{}, errors.New("plugin artifact repository is closed")
	}
	if err := r.lockMutationContext(ctx); err != nil {
		return ArtifactPruneResult{}, err
	}
	defer r.unlockMutation()
	if err := r.cleanupPendingLocked(ctx); err != nil {
		return ArtifactPruneResult{}, err
	}
	usage, err := r.scanLocked(ctx)
	if err != nil {
		return ArtifactPruneResult{}, err
	}
	result := usage.ArtifactPruneResult
	slices.SortFunc(usage.candidates, func(left, right artifactCleanupCandidate) int {
		if compared := left.modified.Compare(right.modified); compared != 0 {
			return compared
		}
		return strings.Compare(left.path, right.path)
	})
	for _, candidate := range usage.candidates {
		if result.DeletedPackages >= maxDeletes {
			break
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if !candidate.modified.Before(olderThan) {
			continue
		}
		if _, keep := protected[candidate.key]; keep {
			continue
		}
		bundle, err := r.getLocked(candidate.key)
		if err != nil {
			return result, fmt.Errorf("verify cleanup candidate %q: %w", candidate.path, err)
		}
		bytesRemoved := int64(len(bundle.ManifestJSON) + len(bundle.Signature) + len(bundle.Artifact))
		if err := r.root.RemoveAll(candidate.path); err != nil {
			return result, fmt.Errorf("remove plugin artifact %q: %w", candidate.path, err)
		}
		if err := r.syncAndRemoveEmptyParents(candidate.path); err != nil {
			return result, err
		}
		result.DeletedPackages++
		result.DeletedBytes += bytesRemoved
	}
	return result, nil
}

func createPrivateArtifactDirectory(root *os.Root) (string, error) {
	for range 8 {
		var randomName [16]byte
		if _, err := rand.Read(randomName[:]); err != nil {
			return "", fmt.Errorf("generate temporary artifact directory name: %w", err)
		}
		path := ".pending-" + hex.EncodeToString(randomName[:])
		if err := root.Mkdir(path, 0o700); err == nil {
			return path, nil
		} else if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	return "", errors.New("could not allocate a unique temporary artifact directory")
}

// Get returns a size-bounded copy of a package after checking its stored identity and digest.
func (r *FileArtifactRepository) Get(key ArtifactKey) (PackageArtifact, error) {
	if err := key.Validate(); err != nil {
		return PackageArtifact{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return PackageArtifact{}, errors.New("plugin artifact repository is closed")
	}
	return r.getLocked(key)
}

// Close releases the repository root and prevents further operations.
func (r *FileArtifactRepository) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return errors.Join(r.lock.Close(), r.root.Close())
}

func (r *FileArtifactRepository) getLocked(key ArtifactKey) (PackageArtifact, error) {
	path := artifactRelativePath(key)
	info, err := r.root.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return PackageArtifact{}, errors.New("plugin artifact package directory is missing or unsafe")
	}
	limits := map[string]int64{
		"manifest.json": maxArtifactManifestBytes,
		"manifest.sig":  ed25519.SignatureSize,
		"artifact":      r.limits.MaxArtifactBytes,
	}
	contents := make(map[string][]byte, len(limits))
	for name, limit := range limits {
		filePath := filepath.Join(path, name)
		fileInfo, err := r.root.Lstat(filePath)
		if err != nil || fileInfo.Mode()&os.ModeSymlink != 0 || !fileInfo.Mode().IsRegular() || fileInfo.Mode().Perm() != 0o600 || fileInfo.Size() > limit {
			return PackageArtifact{}, fmt.Errorf("plugin artifact file %q is missing or unsafe", name)
		}
		file, err := r.root.Open(filePath)
		if err != nil {
			return PackageArtifact{}, fmt.Errorf("open plugin artifact file %q: %w", name, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(file, limit+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || int64(len(data)) > limit {
			return PackageArtifact{}, fmt.Errorf("read plugin artifact file %q failed or exceeded its size limit", name)
		}
		contents[name] = data
	}
	manifest, err := decodeCanonicalManifest(contents["manifest.json"])
	if err != nil {
		return PackageArtifact{}, fmt.Errorf("decode stored plugin manifest: %w", err)
	}
	digest := sha256.Sum256(contents["artifact"])
	if manifest.PluginID != key.PluginID || manifest.Version != key.Version || manifest.Platform != key.Platform ||
		manifest.ArtifactSHA256 != key.SHA256 || hex.EncodeToString(digest[:]) != key.SHA256 || len(contents["manifest.sig"]) != ed25519.SignatureSize {
		return PackageArtifact{}, errors.New("stored plugin artifact does not match requested identity")
	}
	return PackageArtifact{
		ManifestJSON: contents["manifest.json"], Signature: contents["manifest.sig"], Artifact: contents["artifact"],
	}, nil
}

func (r *FileArtifactRepository) ensurePrivateDirectories(relativePath string) error {
	current := ""
	for _, component := range splitPath(relativePath) {
		current = filepath.Join(current, component)
		info, err := r.root.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			err = r.root.Mkdir(current, 0o700)
			if err != nil && !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("create plugin artifact directory: %w", err)
			}
			info, err = r.root.Lstat(current)
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("plugin artifact path component %q is unsafe", current)
		}
	}
	return nil
}

func openArtifactRepositoryLock(root *os.Root) (*os.File, error) {
	if info, err := root.Lstat(artifactRepositoryLock); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return nil, errors.New("plugin artifact repository lock must be a private regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect plugin artifact repository lock: %w", err)
	}
	lock, err := root.OpenFile(artifactRepositoryLock, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open plugin artifact repository lock: %w", err)
	}
	return lock, nil
}

func (r *FileArtifactRepository) lockMutation() error {
	if err := syscall.Flock(int(r.lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock plugin artifact repository: %w", err)
	}
	return nil
}

func (r *FileArtifactRepository) lockMutationContext(ctx context.Context) error {
	for {
		err := syscall.Flock(int(r.lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return fmt.Errorf("lock plugin artifact repository: %w", err)
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (r *FileArtifactRepository) unlockMutation() {
	_ = syscall.Flock(int(r.lock.Fd()), syscall.LOCK_UN)
}

func (r *FileArtifactRepository) withMutationLock(operation func() error) error {
	if err := r.lockMutation(); err != nil {
		return err
	}
	defer r.unlockMutation()
	return operation()
}

func (r *FileArtifactRepository) cleanupPendingLocked(ctx context.Context) error {
	directory, err := r.root.Open(".")
	if err != nil {
		return fmt.Errorf("open plugin artifact repository root: %w", err)
	}
	defer directory.Close()
	seen := 0
	for {
		entries, readErr := directory.ReadDir(64)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			seen++
			if seen > r.limits.MaxScanEntries {
				return errors.New("plugin artifact repository exceeds its scan entry limit")
			}
			name := entry.Name()
			if !strings.HasPrefix(name, ".pending-") || len(name) != len(".pending-")+32 {
				continue
			}
			if _, err := hex.DecodeString(strings.TrimPrefix(name, ".pending-")); err != nil {
				continue
			}
			if err := r.root.RemoveAll(name); err != nil {
				return fmt.Errorf("remove abandoned plugin artifact import: %w", err)
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func (r *FileArtifactRepository) scanLocked(ctx context.Context) (artifactRepositoryScan, error) {
	result := artifactRepositoryScan{}
	err := filepath.WalkDir(r.rootPath, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if path == r.rootPath {
			return nil
		}
		result.ScannedEntries++
		if result.ScannedEntries > r.limits.MaxScanEntries {
			return errors.New("plugin artifact repository exceeds its scan entry limit")
		}
		relative, err := filepath.Rel(r.rootPath, path)
		if err != nil || relative == "." || filepath.IsAbs(relative) {
			return errors.New("plugin artifact repository contains an invalid path")
		}
		info, err := entry.Info()
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("plugin artifact repository contains an unsafe entry")
		}
		depth := len(splitPath(relative))
		if info.IsDir() {
			if info.Mode().Perm()&0o077 != 0 || depth > 4 {
				return errors.New("plugin artifact repository contains an unsafe directory")
			}
			if depth == 4 {
				result.Packages++
				if result.Packages > r.limits.MaxPackages {
					return errors.New("plugin artifact repository exceeds its package limit")
				}
				result.candidates = append(result.candidates, artifactCleanupCandidate{path: relative, modified: info.ModTime()})
			}
			return nil
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return errors.New("plugin artifact repository contains an unsafe file")
		}
		if depth == 1 && relative == artifactRepositoryLock {
			return nil
		}
		parts := splitPath(relative)
		if depth != 5 || (parts[4] != "manifest.json" && parts[4] != "manifest.sig" && parts[4] != "artifact") {
			return errors.New("plugin artifact repository contains an unexpected file")
		}
		result.UsedBytes += info.Size()
		if result.UsedBytes > r.limits.MaxRepositoryBytes {
			return errors.New("plugin artifact repository exceeds its byte limit")
		}
		return nil
	})
	if err != nil {
		return artifactRepositoryScan{}, fmt.Errorf("scan plugin artifact repository: %w", err)
	}
	for index := range result.candidates {
		if err := ctx.Err(); err != nil {
			return artifactRepositoryScan{}, err
		}
		candidate := &result.candidates[index]
		key, err := r.readArtifactKeyLocked(candidate.path)
		if err != nil {
			return artifactRepositoryScan{}, fmt.Errorf("read plugin artifact repository entry %q: %w", candidate.path, err)
		}
		candidate.key = key
		if _, err := r.getLocked(candidate.key); err != nil {
			return artifactRepositoryScan{}, fmt.Errorf("validate plugin artifact repository entry %q: %w", candidate.path, err)
		}
	}
	return result, nil
}

func (r *FileArtifactRepository) readArtifactKeyLocked(path string) (ArtifactKey, error) {
	manifestPath := filepath.Join(path, "manifest.json")
	file, err := r.root.Open(manifestPath)
	if err != nil {
		return ArtifactKey{}, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxArtifactManifestBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(data) > maxArtifactManifestBytes {
		return ArtifactKey{}, errors.New("plugin artifact manifest could not be read within its limit")
	}
	manifest, err := decodeCanonicalManifest(data)
	if err != nil {
		return ArtifactKey{}, err
	}
	key := ArtifactKey{PluginID: manifest.PluginID, Version: manifest.Version, Platform: manifest.Platform, SHA256: manifest.ArtifactSHA256}
	if err := key.Validate(); err != nil {
		return ArtifactKey{}, err
	}
	if artifactRelativePath(key) != path {
		return ArtifactKey{}, errors.New("plugin artifact manifest does not match its repository path")
	}
	return key, nil
}

func (r *FileArtifactRepository) syncAndRemoveEmptyParents(packagePath string) error {
	parent := filepath.Dir(packagePath)
	directory, err := r.root.Open(parent)
	if err != nil {
		return fmt.Errorf("open pruned artifact parent: %w", err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil || closeErr != nil {
		return errors.Join(syncErr, closeErr)
	}
	for current := parent; current != "." && current != ""; current = filepath.Dir(current) {
		if err := r.root.Remove(current); err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTEMPTY) {
				continue
			}
			return fmt.Errorf("remove empty plugin artifact directory: %w", err)
		}
	}
	return nil
}

func splitPath(path string) []string {
	parts := []string{}
	for path != "." && path != "" {
		parent, base := filepath.Split(path)
		parts = append([]string{base}, parts...)
		path = filepath.Clean(parent)
	}
	return parts
}

func artifactRelativePath(key ArtifactKey) string {
	return filepath.Join(key.PluginID, key.Version, key.Platform.OS+"-"+key.Platform.Arch, key.SHA256)
}

func packageArtifactsEqual(left, right PackageArtifact) bool {
	return bytes.Equal(left.ManifestJSON, right.ManifestJSON) && bytes.Equal(left.Signature, right.Signature) && bytes.Equal(left.Artifact, right.Artifact)
}

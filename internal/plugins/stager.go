/*
Package plugins stages verified artifacts under a private local directory.

The stager validates a package before writing immutable manifest, signature,
and artifact files. It never installs, activates, launches, downloads, or removes
an earlier version; those operations belong to later lifecycle components.
*/
package plugins

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
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

const stagingLockFile = ".staging.lock"

// StagedPackage identifies one verified package below the configured staging root.
type StagedPackage struct {
	Manifest     Manifest
	RelativePath string
	ArtifactPath string
}

// Stager verifies and writes immutable package versions below a private directory.
type Stager struct {
	mu              sync.Mutex
	root            *os.Root
	rootPath        string
	publicKey       ed25519.PublicKey
	maxArtifactSize int64
	limits          *StagingLimits
	lock            *os.File
	closed          bool
}

// StagingLimits bounds the complete Agent staging tree.
type StagingLimits struct {
	MaxArtifactBytes int64
	MaxStagingBytes  int64
	MaxPackages      int
	MaxScanEntries   int
}

// StagingPruneResult reports bounded staging usage and cleanup work.
type StagingPruneResult struct {
	ScannedEntries  int
	Packages        int
	UsedBytes       int64
	DeletedPackages int
	DeletedBytes    int64
}

type stagingCandidate struct {
	path     string
	modified time.Time
	bytes    int64
}

type stagingScan struct {
	StagingPruneResult
	candidates []stagingCandidate
}

// NewStager opens an existing absolute staging directory owned by this user.
// The directory must have no group or other permissions, and every package
// operation is bounded by the caller's explicit artifact size limit.
func NewStager(rootPath string, publicKey ed25519.PublicKey, maxArtifactSize int64) (*Stager, error) {
	return newStager(rootPath, publicKey, maxArtifactSize, nil)
}

// NewBoundedStager requires explicit single-package and total staging limits.
func NewBoundedStager(rootPath string, publicKey ed25519.PublicKey, limits StagingLimits) (*Stager, error) {
	if limits.MaxArtifactBytes < 1 || limits.MaxStagingBytes < limits.MaxArtifactBytes || limits.MaxPackages < 1 || limits.MaxScanEntries < 1 || limits.MaxPackages > (limits.MaxScanEntries-1)/8 {
		return nil, errors.New("plugin staging limits must be positive and ordered")
	}
	copyOfLimits := limits
	stager, err := newStager(rootPath, publicKey, limits.MaxArtifactBytes, &copyOfLimits)
	if err != nil {
		return nil, err
	}
	stager.lock, err = openStagingLock(stager.root)
	if err != nil {
		_ = stager.Close()
		return nil, err
	}
	stager.mu.Lock()
	_, scanErr := stager.scanLocked(context.Background())
	stager.mu.Unlock()
	if scanErr != nil {
		_ = stager.Close()
		return nil, scanErr
	}
	return stager, nil
}

func newStager(rootPath string, publicKey ed25519.PublicKey, maxArtifactSize int64, limits *StagingLimits) (*Stager, error) {
	if !filepath.IsAbs(rootPath) {
		return nil, errors.New("plugin staging path must be absolute")
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return nil, errors.New("publisher public key has an invalid length")
	}
	if maxArtifactSize < 1 || maxArtifactSize == int64(^uint64(0)>>1) {
		return nil, errors.New("plugin artifact size limit must be positive and representable")
	}
	info, err := os.Lstat(rootPath)
	if err != nil {
		return nil, fmt.Errorf("inspect plugin staging directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("plugin staging directory must be a private, non-symlink directory")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open plugin staging directory: %w", err)
	}
	return &Stager{root: root, rootPath: rootPath, publicKey: append(ed25519.PublicKey(nil), publicKey...), maxArtifactSize: maxArtifactSize, limits: limits}, nil
}

// Stage verifies package bytes and writes them without replacing an existing version.
func (s *Stager) Stage(manifestJSON, signature, artifact []byte) (StagedPackage, error) {
	if int64(len(artifact)) > s.maxArtifactSize {
		return StagedPackage{}, errors.New("plugin artifact exceeds staging size limit")
	}
	if len(manifestJSON) > maxArtifactManifestBytes {
		return StagedPackage{}, errors.New("plugin manifest exceeds staging size limit")
	}
	manifest, err := VerifyPackage(s.publicKey, manifestJSON, signature, artifact)
	if err != nil {
		return StagedPackage{}, err
	}
	platform := manifest.Platform.OS + "-" + manifest.Platform.Arch
	relativePath := filepath.Join(manifest.PluginID, manifest.Version, platform, manifest.ArtifactSHA256)
	parent := filepath.Dir(relativePath)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return StagedPackage{}, errors.New("plugin stager is closed")
	}
	if info, err := s.root.Lstat(relativePath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
			return StagedPackage{}, errors.New("existing plugin package path is not a private directory")
		}
		if err := s.verifyExisting(relativePath, manifestJSON, signature, artifact, manifest.ArtifactSHA256); err != nil {
			return StagedPackage{}, err
		}
		return StagedPackage{Manifest: manifest, RelativePath: filepath.ToSlash(relativePath), ArtifactPath: filepath.Join(s.rootPath, relativePath, "artifact")}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return StagedPackage{}, fmt.Errorf("inspect staged plugin package: %w", err)
	}
	if s.limits != nil {
		usage, err := s.scanLocked(context.Background())
		if err != nil {
			return StagedPackage{}, err
		}
		incomingBytes := int64(len(manifestJSON)) + int64(len(signature)) + int64(len(artifact)) + sha256.Size*2 + 1
		if usage.Packages >= s.limits.MaxPackages || incomingBytes > s.limits.MaxStagingBytes-usage.UsedBytes {
			return StagedPackage{}, errors.New("plugin staging capacity reached")
		}
	}
	if err := s.ensurePrivateDirectories(parent); err != nil {
		return StagedPackage{}, err
	}
	if err := s.root.Mkdir(relativePath, 0o700); err != nil {
		return StagedPackage{}, fmt.Errorf("create staged plugin package: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = s.root.RemoveAll(relativePath)
		}
	}()
	files := []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{name: "manifest.json", data: manifestJSON, mode: 0o600},
		{name: "manifest.sig", data: signature, mode: 0o600},
		// Supervisor receives this path as the signed executable entry point.
		// Restrict execution to the staging owner; staging itself never executes it.
		{name: "artifact", data: artifact, mode: 0o700},
		{name: "verified", data: []byte(manifest.ArtifactSHA256 + "\n"), mode: 0o600},
	}
	for _, file := range files {
		if err := writeExclusive(s.root, filepath.Join(relativePath, file.name), file.data, file.mode); err != nil {
			return StagedPackage{}, err
		}
	}
	directory, err := s.root.Open(relativePath)
	if err != nil {
		return StagedPackage{}, fmt.Errorf("open staged package directory: %w", err)
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return StagedPackage{}, fmt.Errorf("sync staged package directory: %w", err)
	}
	if err := directory.Close(); err != nil {
		return StagedPackage{}, fmt.Errorf("close staged package directory: %w", err)
	}
	cleanup = false
	return StagedPackage{Manifest: manifest, RelativePath: filepath.ToSlash(relativePath), ArtifactPath: filepath.Join(s.rootPath, relativePath, "artifact")}, nil
}

// Prune removes complete old packages not referenced by current or rollback state.
func (s *Stager) Prune(ctx context.Context, protected map[string]struct{}, olderThan time.Time, maxDeletes int) (StagingPruneResult, error) {
	if ctx == nil || olderThan.IsZero() || maxDeletes < 1 || s.limits == nil {
		return StagingPruneResult{}, errors.New("bounded plugin staging cleanup requires context, cutoff, and delete limit")
	}
	for path := range protected {
		clean := filepath.Clean(filepath.FromSlash(path))
		if path == "" || filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.ToSlash(clean) != path {
			return StagingPruneResult{}, errors.New("protected staged package path is invalid")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return StagingPruneResult{}, errors.New("plugin stager is closed")
	}
	scan, err := s.scanLocked(ctx)
	if err != nil {
		return StagingPruneResult{}, err
	}
	result := scan.StagingPruneResult
	slices.SortFunc(scan.candidates, func(left, right stagingCandidate) int {
		if compared := left.modified.Compare(right.modified); compared != 0 {
			return compared
		}
		return strings.Compare(left.path, right.path)
	})
	for _, candidate := range scan.candidates {
		if result.DeletedPackages >= maxDeletes || !candidate.modified.Before(olderThan) {
			continue
		}
		canonical := filepath.ToSlash(candidate.path)
		if _, keep := protected[canonical]; keep {
			continue
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := s.root.RemoveAll(candidate.path); err != nil {
			return result, fmt.Errorf("remove staged plugin package: %w", err)
		}
		s.removeEmptyParents(filepath.Dir(candidate.path))
		result.DeletedPackages++
		result.DeletedBytes += candidate.bytes
	}
	return result, nil
}

func (s *Stager) scanLocked(ctx context.Context) (stagingScan, error) {
	var result stagingScan
	if s.limits == nil {
		return result, errors.New("staging scan requires total limits")
	}
	var walk func(string, int) error
	walk = func(relative string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		directory, err := s.root.Open(relative)
		if err != nil {
			return fmt.Errorf("open staging directory: %w", err)
		}
		defer directory.Close()
		for {
			entries, err := directory.ReadDir(32)
			if err != nil && !errors.Is(err, io.EOF) {
				return fmt.Errorf("scan staging directory: %w", err)
			}
			for _, entry := range entries {
				result.ScannedEntries++
				if result.ScannedEntries > s.limits.MaxScanEntries {
					return errors.New("plugin staging scan entry limit exceeded")
				}
				path := filepath.Join(relative, entry.Name())
				if depth == 0 && entry.Name() == stagingLockFile {
					info, statErr := s.root.Lstat(path)
					if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
						return errors.New("plugin staging lock is unsafe")
					}
					continue
				}
				info, statErr := s.root.Lstat(path)
				if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
					return fmt.Errorf("staging path %q is not a private directory", path)
				}
				if depth == 3 {
					candidate, scanErr := s.scanPackageLocked(path, info.ModTime(), &result.ScannedEntries)
					if scanErr != nil {
						return scanErr
					}
					result.Packages++
					result.UsedBytes += candidate.bytes
					result.candidates = append(result.candidates, candidate)
					if result.Packages > s.limits.MaxPackages || result.UsedBytes > s.limits.MaxStagingBytes {
						return errors.New("plugin staging exceeds configured capacity")
					}
				} else if err := walk(path, depth+1); err != nil {
					return err
				}
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
		}
	}
	if err := walk(".", 0); err != nil {
		return stagingScan{}, err
	}
	return result, nil
}

func (s *Stager) scanPackageLocked(path string, modified time.Time, scanned *int) (stagingCandidate, error) {
	directory, err := s.root.Open(path)
	if err != nil {
		return stagingCandidate{}, err
	}
	defer directory.Close()
	expected := map[string]struct {
		mode os.FileMode
		max  int64
	}{
		"manifest.json": {0o600, maxArtifactManifestBytes},
		"manifest.sig":  {0o600, ed25519.SignatureSize},
		"artifact":      {0o700, s.maxArtifactSize},
		"verified":      {0o600, sha256.Size*2 + 1},
	}
	seen := make(map[string]struct{}, len(expected))
	var used int64
	for {
		entries, readErr := directory.ReadDir(8)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return stagingCandidate{}, readErr
		}
		for _, entry := range entries {
			*scanned++
			if *scanned > s.limits.MaxScanEntries {
				return stagingCandidate{}, errors.New("plugin staging scan entry limit exceeded")
			}
			limits, ok := expected[entry.Name()]
			if !ok {
				return stagingCandidate{}, fmt.Errorf("staged package %q contains an unexpected entry", path)
			}
			info, err := s.root.Lstat(filepath.Join(path, entry.Name()))
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != limits.mode || info.Size() > limits.max || info.Size() > s.limits.MaxStagingBytes-used {
				return stagingCandidate{}, fmt.Errorf("staged package %q contains an unsafe file", path)
			}
			seen[entry.Name()] = struct{}{}
			used += info.Size()
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	if len(seen) != len(expected) {
		return stagingCandidate{}, fmt.Errorf("staged package %q is incomplete", path)
	}
	return stagingCandidate{path: path, modified: modified, bytes: used}, nil
}

func (s *Stager) removeEmptyParents(path string) {
	for path != "." && path != "" {
		directory, err := s.root.Open(path)
		if err != nil {
			return
		}
		entries, readErr := directory.ReadDir(1)
		_ = directory.Close()
		if readErr == nil || len(entries) != 0 || !errors.Is(readErr, io.EOF) {
			return
		}
		if err := s.root.Remove(path); err != nil {
			return
		}
		path = filepath.Dir(path)
	}
}

// VerifyStaged rechecks stored bytes before a later lifecycle component uses them.
// It confirms integrity and publisher authenticity without executing the artifact.
func (s *Stager) VerifyStaged(relativePath string) (StagedPackage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return StagedPackage{}, errors.New("plugin stager is closed")
	}
	cleanPath := filepath.Clean(filepath.FromSlash(relativePath))
	if filepath.IsAbs(cleanPath) || cleanPath == "." || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
		return StagedPackage{}, errors.New("staged plugin path must be relative to the staging root")
	}
	info, err := s.root.Lstat(cleanPath)
	if err != nil {
		return StagedPackage{}, fmt.Errorf("inspect staged plugin package: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return StagedPackage{}, errors.New("staged plugin package must be a private, non-symlink directory")
	}
	limits := map[string]struct {
		mode os.FileMode
		size int64
	}{
		"manifest.json": {mode: 0o600, size: maxArtifactManifestBytes},
		"manifest.sig":  {mode: 0o600, size: ed25519.SignatureSize},
		"artifact":      {mode: 0o700, size: s.maxArtifactSize},
		"verified":      {mode: 0o600, size: sha256.Size*2 + 1},
	}
	for name, fileLimits := range limits {
		if err := verifyStagedFile(s.root, filepath.Join(cleanPath, name), fileLimits.mode, fileLimits.size); err != nil {
			return StagedPackage{}, err
		}
	}
	manifestJSON, err := readStagedFile(s.root, filepath.Join(cleanPath, "manifest.json"), maxArtifactManifestBytes)
	if err != nil {
		return StagedPackage{}, fmt.Errorf("read staged plugin manifest: %w", err)
	}
	signature, err := readStagedFile(s.root, filepath.Join(cleanPath, "manifest.sig"), ed25519.SignatureSize)
	if err != nil {
		return StagedPackage{}, fmt.Errorf("read staged plugin signature: %w", err)
	}
	artifact, err := readStagedFile(s.root, filepath.Join(cleanPath, "artifact"), s.maxArtifactSize)
	if err != nil {
		return StagedPackage{}, fmt.Errorf("read staged plugin artifact: %w", err)
	}
	manifest, err := VerifyPackage(s.publicKey, manifestJSON, signature, artifact)
	if err != nil {
		return StagedPackage{}, fmt.Errorf("verify staged plugin package: %w", err)
	}
	expectedPath := filepath.Join(manifest.PluginID, manifest.Version, manifest.Platform.OS+"-"+manifest.Platform.Arch, manifest.ArtifactSHA256)
	if cleanPath != expectedPath {
		return StagedPackage{}, errors.New("staged plugin path does not match its signed manifest")
	}
	verified, err := readStagedFile(s.root, filepath.Join(cleanPath, "verified"), sha256.Size*2+1)
	if err != nil || !bytes.Equal(verified, []byte(manifest.ArtifactSHA256+"\n")) {
		return StagedPackage{}, errors.New("staged plugin verification marker is missing or inconsistent")
	}
	return StagedPackage{Manifest: manifest, RelativePath: filepath.ToSlash(cleanPath), ArtifactPath: filepath.Join(s.rootPath, cleanPath, "artifact")}, nil
}

// Close releases the staging root and makes further Stage calls fail.
func (s *Stager) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var lockErr error
	if s.lock != nil {
		lockErr = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		lockErr = errors.Join(lockErr, s.lock.Close())
		s.lock = nil
	}
	return errors.Join(lockErr, s.root.Close())
}

func openStagingLock(root *os.Root) (*os.File, error) {
	file, err := root.OpenFile(stagingLockFile, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open plugin staging lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock plugin staging directory: %w", err)
	}
	return file, nil
}

func (s *Stager) ensurePrivateDirectories(relativePath string) error {
	current := ""
	for _, component := range strings.Split(filepath.Clean(relativePath), string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := s.root.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			err = s.root.Mkdir(current, 0o700)
			if err != nil && !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("create staged package directory: %w", err)
			}
			info, err = s.root.Lstat(current)
		}
		if err != nil {
			return fmt.Errorf("inspect staged package directory: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("staging path component %q must be a private directory", current)
		}
	}
	return nil
}

func (s *Stager) verifyExisting(relativePath string, manifestJSON, signature, artifact []byte, digest string) error {
	limits := map[string]struct {
		mode os.FileMode
		size int64
	}{
		"manifest.json": {mode: 0o600, size: maxArtifactManifestBytes},
		"manifest.sig":  {mode: 0o600, size: ed25519.SignatureSize},
		"artifact":      {mode: 0o700, size: s.maxArtifactSize},
		"verified":      {mode: 0o600, size: sha256.Size*2 + 1},
	}
	for name, fileLimits := range limits {
		if err := verifyStagedFile(s.root, filepath.Join(relativePath, name), fileLimits.mode, fileLimits.size); err != nil {
			return err
		}
	}
	verified, err := readStagedFile(s.root, filepath.Join(relativePath, "verified"), sha256.Size*2+1)
	if err != nil || !bytes.Equal(verified, []byte(digest+"\n")) {
		return errors.New("plugin version path already exists with incomplete or conflicting contents")
	}
	for name, expected := range map[string][]byte{
		"manifest.json": manifestJSON,
		"manifest.sig":  signature,
		"artifact":      artifact,
	} {
		actual, err := readStagedFile(s.root, filepath.Join(relativePath, name), limits[name].size)
		if err != nil || !bytes.Equal(actual, expected) {
			return errors.New("plugin version path already exists with conflicting contents")
		}
	}
	return nil
}

func verifyStagedFile(root *os.Root, path string, mode os.FileMode, maxSize int64) error {
	info, err := root.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != mode {
		return fmt.Errorf("staged plugin file %q has an unsafe type or permission", filepath.Base(path))
	}
	if info.Size() > maxSize {
		return fmt.Errorf("staged plugin file %q exceeds its size limit", filepath.Base(path))
	}
	return nil
}

func readStagedFile(root *os.Root, path string, limit int64) ([]byte, error) {
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, limit+1))
	statErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if statErr != nil {
		return nil, statErr
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("staged plugin file %q exceeds its size limit", filepath.Base(path))
	}
	return data, nil
}

func writeExclusive(root *os.Root, path string, contents []byte, mode os.FileMode) error {
	file, err := root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create staged package file %q: %w", filepath.Base(path), err)
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		return fmt.Errorf("write staged package file %q: %w", filepath.Base(path), err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync staged package file %q: %w", filepath.Base(path), err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close staged package file %q: %w", filepath.Base(path), err)
	}
	return nil
}

/*
Package plugins stages verified artifacts under a private local directory.

The stager validates a package before writing immutable manifest, signature,
and artifact files. It never installs, activates, launches, downloads, or removes
an earlier version; those operations belong to later lifecycle components.
*/
package plugins

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

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
	closed          bool
}

// NewStager opens an existing absolute staging directory owned by this user.
// The directory must have no group or other permissions, and every package
// operation is bounded by the caller's explicit artifact size limit.
func NewStager(rootPath string, publicKey ed25519.PublicKey, maxArtifactSize int64) (*Stager, error) {
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
	return &Stager{root: root, rootPath: rootPath, publicKey: append(ed25519.PublicKey(nil), publicKey...), maxArtifactSize: maxArtifactSize}, nil
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
	if err := s.ensurePrivateDirectories(parent); err != nil {
		return StagedPackage{}, err
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
	return s.root.Close()
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

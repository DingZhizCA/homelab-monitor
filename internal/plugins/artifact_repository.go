/*
Package plugins stores and retrieves immutable signed plugin package artifacts.

The repository owns the local package layout and byte-integrity checks. Publisher
trust remains an Agent decision, and the repository does not install or execute
the returned artifact.
*/
package plugins

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const maxArtifactManifestBytes = 1 << 20

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
	mu              sync.RWMutex
	root            *os.Root
	maxArtifactSize int64
	closed          bool
}

// NewFileArtifactRepository requires an existing private directory and explicit artifact size limit.
func NewFileArtifactRepository(rootPath string, maxArtifactSize int64) (*FileArtifactRepository, error) {
	if !filepath.IsAbs(rootPath) {
		return nil, errors.New("plugin artifact repository path must be absolute")
	}
	if maxArtifactSize < 1 {
		return nil, errors.New("plugin artifact size limit must be positive")
	}
	info, err := os.Lstat(rootPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("plugin artifact repository must be an existing private non-symlink directory")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open plugin artifact repository: %w", err)
	}
	return &FileArtifactRepository{root: root, maxArtifactSize: maxArtifactSize}, nil
}

// Put validates package identity and stores it without replacing an existing artifact.
func (r *FileArtifactRepository) Put(key ArtifactKey, bundle PackageArtifact) error {
	if err := key.Validate(); err != nil {
		return err
	}
	if int64(len(bundle.Artifact)) > r.maxArtifactSize {
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
	return r.root.Close()
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
		"artifact":      r.maxArtifactSize,
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

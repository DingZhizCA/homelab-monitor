/*
Package plugins loads the local publisher trust root for signed plugin packages.

The key file contains raw Ed25519 public-key bytes. This loader does not fetch
or rotate trust roots; their provisioning remains an out-of-band host decision.
*/
package plugins

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// LoadPublisherPublicKey reads one raw Ed25519 key from a protected local file.
func LoadPublisherPublicKey(path string) (ed25519.PublicKey, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("plugin publisher key path must be absolute")
	}
	cleanPath := filepath.Clean(path)
	parentPath := filepath.Dir(cleanPath)
	resolvedParent, err := filepath.EvalSymlinks(parentPath)
	if err != nil || resolvedParent != parentPath {
		return nil, errors.New("plugin publisher key directory must exist without symbolic links")
	}
	parentInfo, err := os.Lstat(parentPath)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 || parentInfo.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("plugin publisher key directory must not be writable by group or other users")
	}
	root, err := os.OpenRoot(parentPath)
	if err != nil {
		return nil, fmt.Errorf("open plugin publisher key directory: %w", err)
	}
	defer root.Close()
	name := filepath.Base(cleanPath)
	info, err := root.Lstat(name)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || info.Mode().Perm()&0o400 == 0 {
		return nil, errors.New("plugin publisher key must be a readable regular file not writable by group or other users")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open plugin publisher key: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, ed25519.PublicKeySize+1))
	if err != nil {
		return nil, fmt.Errorf("read plugin publisher key: %w", err)
	}
	if len(data) != ed25519.PublicKeySize {
		return nil, errors.New("plugin publisher key must contain exactly one raw Ed25519 public key")
	}
	return append(ed25519.PublicKey(nil), data...), nil
}

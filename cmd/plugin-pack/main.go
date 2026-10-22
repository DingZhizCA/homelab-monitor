/*
Command plugin-pack creates a locally signed plugin package bundle.

The command never executes or publishes the artifact. Publisher key custody,
manifest approval, and the caller-selected maximum artifact size remain release
pipeline responsibilities.
*/
package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/DingZhizCA/homelab-monitor/internal/plugins"
)

const maxManifestBytes = 1 << 20

func main() {
	manifestPath := flag.String("manifest", "", "JSON plugin manifest template")
	artifactPath := flag.String("artifact", "", "plugin executable artifact")
	privateKeyPath := flag.String("private-key", "", "protected raw 64-byte Ed25519 private key")
	outputDirectory := flag.String("output", "", "new private output directory")
	maxArtifactBytes := flag.Int64("max-artifact-bytes", 0, "required maximum input artifact size")
	flag.Parse()
	if err := pack(*manifestPath, *artifactPath, *privateKeyPath, *outputDirectory, *maxArtifactBytes); err != nil {
		fmt.Fprintln(os.Stderr, "plugin-pack:", err)
		os.Exit(1)
	}
}

func pack(manifestPath, artifactPath, privateKeyPath, outputDirectory string, maxArtifactBytes int64) error {
	if manifestPath == "" || artifactPath == "" || privateKeyPath == "" || outputDirectory == "" || maxArtifactBytes < 1 {
		return errors.New("manifest, artifact, private-key, output, and positive max-artifact-bytes are required")
	}
	manifestBytes, err := readBoundedFile(manifestPath, maxManifestBytes)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	var manifest plugins.Manifest
	decoder := json.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return fmt.Errorf("decode manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("manifest must contain exactly one JSON value")
	}
	privateKey, err := readPrivateKey(privateKeyPath)
	if err != nil {
		return err
	}
	artifact, err := readBoundedFile(artifactPath, maxArtifactBytes)
	if err != nil {
		return fmt.Errorf("read artifact: %w", err)
	}
	bundle, key, err := plugins.SignPackage(privateKey, manifest, artifact)
	if err != nil {
		return fmt.Errorf("sign package: %w", err)
	}
	if err := writeBundle(outputDirectory, bundle); err != nil {
		return err
	}
	fmt.Printf("plugin=%s version=%s platform=%s-%s sha256=%s output=%s\n", key.PluginID, key.Version, key.Platform.OS, key.Platform.Arch, key.SHA256, filepath.Clean(outputDirectory))
	return nil
}

func readPrivateKey(path string) (ed25519.PrivateKey, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve publisher private key path: %w", err)
	}
	parentPath := filepath.Dir(absolutePath)
	resolvedParent, err := filepath.EvalSymlinks(parentPath)
	if err != nil || resolvedParent != parentPath {
		return nil, errors.New("publisher private key directory must exist without symbolic links")
	}
	parentInfo, err := os.Lstat(parentPath)
	if err != nil {
		return nil, fmt.Errorf("inspect publisher private key directory: %w", err)
	}
	if !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 || parentInfo.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("publisher private key directory must not be writable by group or other users (mode %04o)", parentInfo.Mode().Perm())
	}
	root, err := os.OpenRoot(parentPath)
	if err != nil {
		return nil, fmt.Errorf("open publisher private key directory: %w", err)
	}
	defer root.Close()
	name := filepath.Base(absolutePath)
	info, err := root.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("inspect publisher private key: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Mode().Perm()&0o400 == 0 {
		return nil, errors.New("publisher private key must be a private readable regular non-symlink file")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open publisher private key: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened publisher private key: %w", err)
	}
	if !os.SameFile(info, openedInfo) {
		return nil, errors.New("publisher private key changed while being opened")
	}
	data, err := io.ReadAll(io.LimitReader(file, ed25519.PrivateKeySize+1))
	if err != nil {
		return nil, fmt.Errorf("read publisher private key: %w", err)
	}
	if len(data) != ed25519.PrivateKeySize {
		return nil, errors.New("publisher private key must contain exactly 64 raw bytes")
	}
	return ed25519.PrivateKey(data), nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds configured limit of %d bytes", limit)
	}
	return data, nil
}

func writeBundle(outputDirectory string, bundle plugins.PackageArtifact) error {
	if err := os.Mkdir(outputDirectory, 0o700); err != nil {
		return fmt.Errorf("create new private output directory: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(outputDirectory)
		}
	}()
	for name, contents := range map[string][]byte{
		"manifest.json": bundle.ManifestJSON,
		"manifest.sig":  bundle.Signature,
		"artifact":      bundle.Artifact,
	} {
		file, err := os.OpenFile(filepath.Join(outputDirectory, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("create package file %s: %w", name, err)
		}
		_, writeErr := file.Write(contents)
		closeErr := file.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			return fmt.Errorf("write package file %s: %w", name, err)
		}
	}
	directory, err := os.Open(outputDirectory)
	if err != nil {
		return fmt.Errorf("open package output directory: %w", err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("sync package output directory: %w", err)
	}
	complete = true
	return nil
}

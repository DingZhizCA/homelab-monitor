/*
Package plugins verifies the integrity and signature of downloaded plugin packages.

The caller supplies a public key obtained from its trust store. This file
validates exact canonical Manifest bytes and the artifact digest, but does not
choose trusted publishers, manage keys, download files, or stage executables.
*/
package plugins

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// VerifyPackage validates a canonical signed Manifest and its referenced artifact.
// The signature covers the exact canonical JSON bytes supplied in manifestJSON.
func VerifyPackage(publicKey ed25519.PublicKey, manifestJSON, signature, artifact []byte) (Manifest, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return Manifest{}, errors.New("publisher public key has an invalid length")
	}
	manifest, err := decodeCanonicalManifest(manifestJSON)
	if err != nil {
		return Manifest{}, err
	}
	if !ed25519.Verify(publicKey, manifestJSON, signature) {
		return Manifest{}, errors.New("plugin manifest signature verification failed")
	}
	digest := sha256.Sum256(artifact)
	if hex.EncodeToString(digest[:]) != manifest.ArtifactSHA256 {
		return Manifest{}, errors.New("plugin artifact digest does not match manifest")
	}
	return manifest, nil
}

// DecodeManifest validates canonical manifest bytes without asserting publisher trust.
func DecodeManifest(manifestJSON []byte) (Manifest, error) {
	return decodeCanonicalManifest(manifestJSON)
}

func decodeCanonicalManifest(raw []byte) (Manifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode plugin manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Manifest{}, errors.New("plugin manifest must contain exactly one JSON object")
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("validate plugin manifest: %w", err)
	}
	canonical, err := json.Marshal(manifest)
	if err != nil {
		return Manifest{}, fmt.Errorf("canonicalize plugin manifest: %w", err)
	}
	if !bytes.Equal(canonical, raw) {
		return Manifest{}, errors.New("plugin manifest is not in canonical JSON form")
	}
	return manifest, nil
}

/*
Package plugins creates canonical signed plugin package bundles.

Signing happens in an explicitly invoked build or release step. Callers own
private-key custody, package-size limits, and approval of manifest claims.
*/
package plugins

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// SignPackage binds an artifact digest to a validated canonical Manifest.
func SignPackage(privateKey ed25519.PrivateKey, manifest Manifest, artifact []byte) (PackageArtifact, ArtifactKey, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return PackageArtifact{}, ArtifactKey{}, errors.New("publisher private key has an invalid length")
	}
	if !bytes.Equal(ed25519.NewKeyFromSeed(privateKey.Seed()), privateKey) {
		return PackageArtifact{}, ArtifactKey{}, errors.New("publisher private key is internally inconsistent")
	}
	digest := sha256.Sum256(artifact)
	artifactDigest := hex.EncodeToString(digest[:])
	if manifest.ArtifactSHA256 != "" && manifest.ArtifactSHA256 != artifactDigest {
		return PackageArtifact{}, ArtifactKey{}, errors.New("manifest artifact digest conflicts with artifact bytes")
	}
	manifest.ArtifactSHA256 = artifactDigest
	if err := manifest.Validate(); err != nil {
		return PackageArtifact{}, ArtifactKey{}, err
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return PackageArtifact{}, ArtifactKey{}, err
	}
	bundle := PackageArtifact{
		ManifestJSON: manifestJSON,
		Signature:    ed25519.Sign(privateKey, manifestJSON),
		Artifact:     append([]byte(nil), artifact...),
	}
	key := ArtifactKey{PluginID: manifest.PluginID, Version: manifest.Version, Platform: manifest.Platform, SHA256: artifactDigest}
	return bundle, key, nil
}

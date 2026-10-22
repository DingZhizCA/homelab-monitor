/*
Package server provides device-scoped credential verification for Agent APIs.

Credentials are kept as one-way digests in memory; this component does not
persist tokens, issue them, or define enrollment and rotation policy.
*/
package server

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/DingZhizCA/homelab-monitor/internal/jsonutil"
)

// DeviceAuthorizer verifies that a credential belongs to the claimed device.
type DeviceAuthorizer interface {
	Authorize(deviceID, token string) bool
}

// DeviceCredentials stores device-scoped token digests for an in-memory runtime.
type DeviceCredentials struct {
	mu   sync.RWMutex
	byID map[string][sha256.Size]byte
}

type deviceCredentialFile struct {
	Devices []deviceCredentialEntry `json:"devices"`
}

type deviceCredentialEntry struct {
	DeviceID    string `json:"device_id"`
	TokenSHA256 string `json:"token_sha256"`
}

// NewDeviceCredentials creates an empty credential set.
func NewDeviceCredentials() *DeviceCredentials {
	return &DeviceCredentials{byID: make(map[string][sha256.Size]byte)}
}

// LoadDeviceCredentials reads per-device token verifier digests from a private JSON file.
// The file format stores SHA-256 digests only; token issuance and rotation stay external.
func LoadDeviceCredentials(path string) (*DeviceCredentials, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("device credential file path must be absolute")
	}
	cleanPath := filepath.Clean(path)
	resolvedParent, err := filepath.EvalSymlinks(filepath.Dir(cleanPath))
	if err != nil || resolvedParent != filepath.Dir(cleanPath) {
		return nil, errors.New("device credential directory must exist without symbolic links")
	}
	parentInfo, err := os.Lstat(filepath.Dir(cleanPath))
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 || parentInfo.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("device credential directory must not be writable by group or other users")
	}
	root, err := os.OpenRoot(filepath.Dir(cleanPath))
	if err != nil {
		return nil, fmt.Errorf("open device credential directory: %w", err)
	}
	defer root.Close()
	name := filepath.Base(cleanPath)
	info, err := root.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("inspect device credential file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Mode().Perm()&0o400 == 0 {
		return nil, errors.New("device credential file must be a private readable regular file")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open device credential file: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return nil, fmt.Errorf("read device credential file: %w", err)
	}
	if len(data) > 1<<20 {
		return nil, errors.New("device credential file exceeds size limit")
	}
	if err := jsonutil.RejectDuplicateObjectKeys(data); err != nil {
		return nil, fmt.Errorf("validate device credential JSON: %w", err)
	}
	var contents deviceCredentialFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&contents); err != nil {
		return nil, fmt.Errorf("decode device credential file: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("device credential file must contain exactly one JSON object")
	}
	credentials := NewDeviceCredentials()
	for _, entry := range contents.Devices {
		if strings.TrimSpace(entry.DeviceID) == "" || strings.TrimSpace(entry.DeviceID) != entry.DeviceID || len(entry.TokenSHA256) != sha256.Size*2 {
			return nil, errors.New("device credential entries require a device ID and SHA-256 token digest")
		}
		decoded, err := hex.DecodeString(entry.TokenSHA256)
		if err != nil || strings.ToLower(entry.TokenSHA256) != entry.TokenSHA256 || len(decoded) != sha256.Size {
			return nil, errors.New("device token digest must be lowercase hexadecimal SHA-256")
		}
		if _, exists := credentials.byID[entry.DeviceID]; exists {
			return nil, fmt.Errorf("duplicate device credential for %q", entry.DeviceID)
		}
		var digest [sha256.Size]byte
		copy(digest[:], decoded)
		credentials.byID[entry.DeviceID] = digest
	}
	return credentials, nil
}

// Set replaces the token for one device without retaining the plaintext token.
func (c *DeviceCredentials) Set(deviceID, token string) error {
	if strings.TrimSpace(deviceID) == "" || strings.TrimSpace(deviceID) != deviceID || token == "" {
		return errors.New("device ID and non-empty token are required")
	}
	digest := sha256.Sum256([]byte(token))
	c.mu.Lock()
	if c.byID == nil {
		c.byID = make(map[string][sha256.Size]byte)
	}
	c.byID[deviceID] = digest
	c.mu.Unlock()
	return nil
}

// Revoke removes a device credential and reports whether one existed.
func (c *DeviceCredentials) Revoke(deviceID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.byID[deviceID]; !ok {
		return false
	}
	delete(c.byID, deviceID)
	return true
}

// Authorize compares token digests in constant time after binding by device ID.
func (c *DeviceCredentials) Authorize(deviceID, token string) bool {
	if deviceID == "" || token == "" {
		return false
	}
	c.mu.RLock()
	stored, ok := c.byID[deviceID]
	c.mu.RUnlock()
	if !ok {
		return false
	}
	candidate := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(stored[:], candidate[:]) == 1
}

func bearerToken(header string) (string, bool) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

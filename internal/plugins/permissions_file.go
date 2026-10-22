/*
Package plugins loads an owner-managed plugin permission allow-list.

The file is accepted only from a private regular file in a protected directory.
Loading policy does not install plugins or enforce OS-level capabilities.
*/
package plugins

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/DingZhizCA/homelab-monitor/internal/jsonutil"
)

const maxPermissionFileBytes = 1 << 20

type permissionGrantDocument struct {
	Grants []PermissionGrant `json:"grants"`
}

// LoadGrantAuthorizer reads explicit plugin grants from a private JSON file.
func LoadGrantAuthorizer(path string) (*GrantAuthorizer, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("plugin permission file path must be absolute")
	}
	cleanPath := filepath.Clean(path)
	parentPath := filepath.Dir(cleanPath)
	resolvedParent, err := filepath.EvalSymlinks(parentPath)
	if err != nil || resolvedParent != parentPath {
		return nil, errors.New("plugin permission directory must exist without symbolic links")
	}
	parentInfo, err := os.Lstat(parentPath)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 || parentInfo.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("plugin permission directory must not be writable by group or other users")
	}
	root, err := os.OpenRoot(parentPath)
	if err != nil {
		return nil, fmt.Errorf("open plugin permission directory: %w", err)
	}
	defer root.Close()
	name := filepath.Base(cleanPath)
	info, err := root.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("inspect plugin permission file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Mode().Perm()&0o400 == 0 {
		return nil, errors.New("plugin permission file must be a private readable regular file")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open plugin permission file: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxPermissionFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read plugin permission file: %w", err)
	}
	if len(data) > maxPermissionFileBytes {
		return nil, errors.New("plugin permission file exceeds size limit")
	}
	if err := jsonutil.RejectDuplicateObjectKeys(data); err != nil {
		return nil, fmt.Errorf("validate plugin permission file JSON: %w", err)
	}
	var document permissionGrantDocument
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode plugin permission file: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("plugin permission file must contain exactly one JSON object")
	}
	return NewGrantAuthorizer(document.Grants)
}

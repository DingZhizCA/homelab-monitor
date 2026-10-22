/*
Package agent persists bounded plugin reconciliation state in a private file.

The store uses checksum-verified atomic replacement and an advisory directory
lock. It contains no process handles and never starts or stops a plugin.
*/
package agent

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/DingZhizCA/homelab-monitor/internal/jsonutil"
	"github.com/DingZhizCA/homelab-monitor/internal/plugins"
)

const (
	pluginStateVersion = 1
	pluginStateFile    = "plugin-reconciler.json"
	pluginStateLock    = ".plugin-reconciler.lock"
	pluginStatePrefix  = ".plugin-reconciler-"
)

type pluginStateEnvelope struct {
	Version      int                          `json:"version"`
	DeviceIDHash string                       `json:"device_id_sha256"`
	State        plugins.ReconcilerCheckpoint `json:"state"`
	Checksum     string                       `json:"checksum"`
}

type pluginReconcilerFileStore struct {
	root          *os.Root
	lock          *os.File
	deviceIDHash  string
	maxStateBytes int64
}

func openPluginReconcilerStore(directory, deviceID string, maxStateBytes int64) (*pluginReconcilerFileStore, error) {
	if !filepath.IsAbs(directory) || strings.TrimSpace(deviceID) == "" || strings.TrimSpace(deviceID) != deviceID || maxStateBytes < 1 {
		return nil, errors.New("absolute plugin state directory, device ID, and positive state byte limit are required")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create plugin state directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("plugin state directory must be a private non-symlink directory")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, fmt.Errorf("open plugin state directory: %w", err)
	}
	lock, err := openPluginStateLock(root)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	store := &pluginReconcilerFileStore{root: root, lock: lock, deviceIDHash: stateIdentityHash(deviceID), maxStateBytes: maxStateBytes}
	if err := store.cleanupTemporary(); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

func (s *pluginReconcilerFileStore) Load() (plugins.ReconcilerCheckpoint, error) {
	info, err := s.root.Lstat(pluginStateFile)
	if errors.Is(err, os.ErrNotExist) {
		return plugins.ReconcilerCheckpoint{}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o177 != 0 || info.Size() > s.maxStateBytes {
		return plugins.ReconcilerCheckpoint{}, errors.New("plugin state must be a private bounded regular file")
	}
	file, err := s.root.Open(pluginStateFile)
	if err != nil {
		return plugins.ReconcilerCheckpoint{}, fmt.Errorf("open plugin state: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, s.maxStateBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || int64(len(data)) > s.maxStateBytes {
		return plugins.ReconcilerCheckpoint{}, errors.New("read bounded plugin state")
	}
	if err := jsonutil.RejectDuplicateObjectKeys(data); err != nil {
		return plugins.ReconcilerCheckpoint{}, fmt.Errorf("validate plugin state JSON: %w", err)
	}
	var envelope pluginStateEnvelope
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return plugins.ReconcilerCheckpoint{}, fmt.Errorf("decode plugin state: %w", err)
	}
	checksum := envelope.Checksum
	envelope.Checksum = ""
	payload, err := json.Marshal(envelope)
	if err != nil || envelope.Version != pluginStateVersion || envelope.DeviceIDHash != s.deviceIDHash || checksum == "" || checksum != pluginStateChecksum(payload) {
		return plugins.ReconcilerCheckpoint{}, errors.New("plugin state version, identity, or checksum is invalid")
	}
	return envelope.State, nil
}

func (s *pluginReconcilerFileStore) Save(state plugins.ReconcilerCheckpoint) (bool, error) {
	envelope := pluginStateEnvelope{Version: pluginStateVersion, DeviceIDHash: s.deviceIDHash, State: state}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return false, fmt.Errorf("encode plugin state checksum payload: %w", err)
	}
	envelope.Checksum = pluginStateChecksum(payload)
	data, err := json.Marshal(envelope)
	if err != nil || int64(len(data)) > s.maxStateBytes {
		return false, errors.New("plugin state exceeds its configured byte limit")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return false, fmt.Errorf("generate plugin state temporary name: %w", err)
	}
	temporary := pluginStatePrefix + hex.EncodeToString(random[:])
	file, err := s.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, fmt.Errorf("create plugin state temporary file: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = s.root.Remove(temporary)
		}
	}()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return false, fmt.Errorf("write plugin state: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return false, fmt.Errorf("sync plugin state: %w", err)
	}
	if err := file.Close(); err != nil {
		return false, fmt.Errorf("close plugin state: %w", err)
	}
	if err := s.root.Rename(temporary, pluginStateFile); err != nil {
		return false, fmt.Errorf("publish plugin state: %w", err)
	}
	cleanup = false
	directory, err := s.root.Open(".")
	if err != nil {
		return true, fmt.Errorf("open plugin state directory: %w", err)
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return true, fmt.Errorf("sync plugin state directory: %w", err)
	}
	if err := directory.Close(); err != nil {
		return true, fmt.Errorf("close plugin state directory: %w", err)
	}
	return true, nil
}

func (s *pluginReconcilerFileStore) Close() error {
	unlockErr := syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
	return errors.Join(unlockErr, s.lock.Close(), s.root.Close())
}

func openPluginStateLock(root *os.Root) (*os.File, error) {
	if info, err := root.Lstat(pluginStateLock); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o177 != 0 {
			return nil, errors.New("plugin state lock must be a private regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect plugin state lock: %w", err)
	}
	file, err := root.OpenFile(pluginStateLock, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open plugin state lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock plugin state directory: %w", err)
	}
	return file, nil
}

func (s *pluginReconcilerFileStore) cleanupTemporary() error {
	directory, err := s.root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	for {
		entries, err := directory.ReadDir(64)
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasPrefix(name, pluginStatePrefix) || len(name) != len(pluginStatePrefix)+32 {
				continue
			}
			if _, decodeErr := hex.DecodeString(strings.TrimPrefix(name, pluginStatePrefix)); decodeErr == nil {
				if removeErr := s.root.Remove(name); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
					return removeErr
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func stateIdentityHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func pluginStateChecksum(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

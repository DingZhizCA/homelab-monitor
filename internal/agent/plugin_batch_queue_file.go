/*
Package agent persists the bounded plugin observation queue as one atomic snapshot.

The private directory is locked by one process; committed state uses file and
directory sync around an atomic rename, with a checksum for restart validation.
*/
package agent

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/DingZhizCA/homelab-monitor/internal/jsonutil"
	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

const (
	pluginBatchQueueStateVersion = 1
	pluginBatchQueueStateFile    = "plugin-metrics-queue.json"
	pluginBatchQueuePrefix       = ".plugin-metrics-queue-"
)

type pluginBatchQueueDiskState struct {
	Version             int                         `json:"version"`
	DeviceIDHash        string                      `json:"device_id_sha256"`
	Observations        []metrics.PluginObservation `json:"observations"`
	PendingBatchID      string                      `json:"pending_batch_id,omitempty"`
	PendingObservations int                         `json:"pending_observations,omitempty"`
	Checksum            string                      `json:"checksum"`
}

type pluginBatchQueueFileStore struct {
	root          *os.Root
	lock          *os.File
	maxStateBytes int64
}

// OpenPluginBatchQueue restores or creates one durable device queue.
func OpenPluginBatchQueue(directory, deviceID string, limits PluginBatchQueueLimits) (*PluginBatchQueue, error) {
	if !filepath.IsAbs(directory) {
		return nil, errors.New("plugin batch state directory must be absolute")
	}
	if _, err := newPluginBatchQueue(deviceID, limits, nil, pluginBatchQueueDiskState{}); err != nil {
		return nil, err
	}
	if limits.MaxQueuedBytes > math.MaxInt64-int64(limits.MaxQueuedObservations)-2048 {
		return nil, errors.New("plugin batch state size limit overflows")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create plugin batch state directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, fmt.Errorf("inspect plugin batch state directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("plugin batch state directory must be a private non-symlink directory")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, fmt.Errorf("open plugin batch state directory: %w", err)
	}
	store := &pluginBatchQueueFileStore{root: root, maxStateBytes: limits.MaxQueuedBytes + int64(limits.MaxQueuedObservations) + 2048}
	store.lock, err = store.acquireLock()
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	if err := store.cleanupTemporary(); err != nil {
		_ = store.Close()
		return nil, err
	}
	state, err := store.Load()
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	queue, err := newPluginBatchQueue(deviceID, limits, store, state)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	return queue, nil
}

func (s *pluginBatchQueueFileStore) Load() (pluginBatchQueueDiskState, error) {
	info, err := s.root.Lstat(pluginBatchQueueStateFile)
	if errors.Is(err, os.ErrNotExist) {
		return pluginBatchQueueDiskState{}, nil
	}
	if err != nil {
		return pluginBatchQueueDiskState{}, fmt.Errorf("inspect persisted plugin queue: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o177 != 0 || info.Size() > s.maxStateBytes {
		return pluginBatchQueueDiskState{}, errors.New("persisted plugin queue must be a private bounded regular file")
	}
	file, err := s.root.Open(pluginBatchQueueStateFile)
	if err != nil {
		return pluginBatchQueueDiskState{}, fmt.Errorf("open persisted plugin queue: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, s.maxStateBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return pluginBatchQueueDiskState{}, errors.Join(readErr, closeErr)
	}
	if int64(len(data)) > s.maxStateBytes {
		return pluginBatchQueueDiskState{}, errors.New("persisted plugin queue exceeds its byte limit")
	}
	if err := jsonutil.RejectDuplicateObjectKeys(data); err != nil {
		return pluginBatchQueueDiskState{}, fmt.Errorf("validate persisted plugin queue JSON: %w", err)
	}
	var state pluginBatchQueueDiskState
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return pluginBatchQueueDiskState{}, fmt.Errorf("decode persisted plugin queue: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return pluginBatchQueueDiskState{}, errors.New("decode persisted plugin queue: expected exactly one JSON object")
	}
	checksum := state.Checksum
	state.Checksum = ""
	payload, err := json.Marshal(state)
	if err != nil || checksum == "" || checksum != batchQueueChecksum(payload) {
		return pluginBatchQueueDiskState{}, errors.New("persisted plugin queue checksum is invalid")
	}
	state.Checksum = checksum
	return state, nil
}

func (s *pluginBatchQueueFileStore) Save(state pluginBatchQueueDiskState) (bool, error) {
	state.Checksum = ""
	payload, err := json.Marshal(state)
	if err != nil {
		return false, fmt.Errorf("encode plugin queue checksum payload: %w", err)
	}
	state.Checksum = batchQueueChecksum(payload)
	data, err := json.Marshal(state)
	if err != nil {
		return false, fmt.Errorf("encode plugin queue state: %w", err)
	}
	if int64(len(data)) > s.maxStateBytes {
		return false, errors.New("plugin queue state exceeds its byte limit")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return false, fmt.Errorf("generate plugin queue temporary name: %w", err)
	}
	temporary := pluginBatchQueuePrefix + hex.EncodeToString(random[:])
	file, err := s.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, fmt.Errorf("create plugin queue temporary state: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = s.root.Remove(temporary)
		}
	}()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return false, fmt.Errorf("write plugin queue temporary state: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return false, fmt.Errorf("sync plugin queue temporary state: %w", err)
	}
	if err := file.Close(); err != nil {
		return false, fmt.Errorf("close plugin queue temporary state: %w", err)
	}
	if err := s.root.Rename(temporary, pluginBatchQueueStateFile); err != nil {
		return false, fmt.Errorf("publish plugin queue state: %w", err)
	}
	cleanup = false
	directory, err := s.root.Open(".")
	if err != nil {
		return true, fmt.Errorf("open plugin queue state directory: %w", err)
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return true, fmt.Errorf("sync plugin queue state directory: %w", err)
	}
	return true, directory.Close()
}

func (s *pluginBatchQueueFileStore) acquireLock() (*os.File, error) {
	const name = ".plugin-metrics-queue.lock"
	if info, err := s.root.Lstat(name); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o177 != 0 {
			return nil, errors.New("plugin queue lock must be a private regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect plugin queue lock: %w", err)
	}
	file, err := s.root.OpenFile(name, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open plugin queue lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock plugin queue state directory: %w", err)
	}
	return file, nil
}

func (s *pluginBatchQueueFileStore) cleanupTemporary() error {
	directory, err := s.root.Open(".")
	if err != nil {
		return fmt.Errorf("open plugin queue state directory for recovery: %w", err)
	}
	defer directory.Close()
	for {
		entries, readErr := directory.ReadDir(64)
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasPrefix(name, pluginBatchQueuePrefix) || len(name) != len(pluginBatchQueuePrefix)+32 {
				continue
			}
			if _, err := hex.DecodeString(strings.TrimPrefix(name, pluginBatchQueuePrefix)); err != nil {
				continue
			}
			if err := s.root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove incomplete plugin queue state: %w", err)
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("scan plugin queue state directory: %w", readErr)
		}
	}
}

func (s *pluginBatchQueueFileStore) Close() error {
	var unlockErr, lockErr error
	if s.lock != nil {
		unlockErr = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		lockErr = s.lock.Close()
		s.lock = nil
	}
	return errors.Join(unlockErr, lockErr, s.root.Close())
}

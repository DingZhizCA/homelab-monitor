/*
Package agent persists one bounded metric queue as an atomic private snapshot.

Each mutation writes and syncs a temporary file, renames it over the prior
state, and syncs the directory. The format does not use an append-only journal.
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
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/DingZhizCA/homelab-monitor/internal/jsonutil"
	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

const (
	batchQueueStateVersion = 1
	batchQueueStateFile    = "metrics-queue.json"
)

type batchQueueDiskState struct {
	Version          int                      `json:"version"`
	DeviceIDHash     string                   `json:"device_id_sha256"`
	Snapshots        []metrics.MetricEnvelope `json:"snapshots"`
	PendingBatchID   string                   `json:"pending_batch_id,omitempty"`
	PendingSnapshots int                      `json:"pending_snapshots,omitempty"`
	Checksum         string                   `json:"checksum"`
}

type batchQueueFileStore struct {
	root          *os.Root
	lock          *os.File
	maxStateBytes int64
}

// OpenBatchQueue restores or creates one queue in an absolute private directory.
func OpenBatchQueue(directory, deviceID string, maxQueuedSnapshots int, maxQueuedBytes int64, maxBatchSnapshots int, maxBatchBodyBytes int64) (*BatchQueue, error) {
	if !filepath.IsAbs(directory) {
		return nil, errors.New("metrics batch state directory must be absolute")
	}
	if _, err := newBatchQueue(deviceID, maxQueuedSnapshots, maxQueuedBytes, maxBatchSnapshots, maxBatchBodyBytes, nil, batchQueueDiskState{}); err != nil {
		return nil, err
	}
	if maxQueuedBytes > math.MaxInt64-int64(maxQueuedSnapshots)-1024 {
		return nil, errors.New("metrics batch state size limit overflows")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create metrics batch state directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, fmt.Errorf("inspect metrics batch state directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("metrics batch state directory must be a private non-symlink directory")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, fmt.Errorf("open metrics batch state directory: %w", err)
	}
	lock, err := acquireBatchQueueLock(root)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	store := &batchQueueFileStore{root: root, lock: lock, maxStateBytes: maxQueuedBytes + int64(maxQueuedSnapshots) + 1024}
	if err := store.cleanupTemporary(); err != nil {
		_ = store.Close()
		return nil, err
	}
	state, err := store.Load()
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	queue, err := newBatchQueue(deviceID, maxQueuedSnapshots, maxQueuedBytes, maxBatchSnapshots, maxBatchBodyBytes, store, state)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	return queue, nil
}

func (s *batchQueueFileStore) Load() (batchQueueDiskState, error) {
	info, err := s.root.Lstat(batchQueueStateFile)
	if errors.Is(err, os.ErrNotExist) {
		return batchQueueDiskState{}, nil
	}
	if err != nil {
		return batchQueueDiskState{}, fmt.Errorf("inspect persisted metrics queue: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o177 != 0 || info.Size() > s.maxStateBytes {
		return batchQueueDiskState{}, errors.New("persisted metrics queue must be a private bounded regular file")
	}
	file, err := s.root.Open(batchQueueStateFile)
	if err != nil {
		return batchQueueDiskState{}, fmt.Errorf("open persisted metrics queue: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, s.maxStateBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return batchQueueDiskState{}, fmt.Errorf("read persisted metrics queue: %w", readErr)
	}
	if closeErr != nil {
		return batchQueueDiskState{}, fmt.Errorf("close persisted metrics queue: %w", closeErr)
	}
	if int64(len(data)) > s.maxStateBytes {
		return batchQueueDiskState{}, errors.New("persisted metrics queue exceeds its byte limit")
	}
	if err := jsonutil.RejectDuplicateObjectKeys(data); err != nil {
		return batchQueueDiskState{}, fmt.Errorf("validate persisted metrics queue JSON: %w", err)
	}
	var state batchQueueDiskState
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return batchQueueDiskState{}, fmt.Errorf("decode persisted metrics queue: %w", err)
	}
	checksum := state.Checksum
	state.Checksum = ""
	payload, err := json.Marshal(state)
	if err != nil || checksum == "" || checksum != batchQueueChecksum(payload) {
		return batchQueueDiskState{}, errors.New("persisted metrics queue checksum is invalid")
	}
	state.Checksum = checksum
	return state, nil
}

func (s *batchQueueFileStore) Save(state batchQueueDiskState) (bool, error) {
	state.Checksum = ""
	payload, err := json.Marshal(state)
	if err != nil {
		return false, fmt.Errorf("encode metrics queue checksum payload: %w", err)
	}
	state.Checksum = batchQueueChecksum(payload)
	data, err := json.Marshal(state)
	if err != nil {
		return false, fmt.Errorf("encode metrics queue state: %w", err)
	}
	if int64(len(data)) > s.maxStateBytes {
		return false, errors.New("metrics queue state exceeds its byte limit")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return false, fmt.Errorf("generate metrics queue temporary name: %w", err)
	}
	temporary := ".metrics-queue-" + hex.EncodeToString(random[:])
	file, err := s.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, fmt.Errorf("create metrics queue temporary state: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = s.root.Remove(temporary)
		}
	}()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return false, fmt.Errorf("write metrics queue temporary state: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return false, fmt.Errorf("sync metrics queue temporary state: %w", err)
	}
	if err := file.Close(); err != nil {
		return false, fmt.Errorf("close metrics queue temporary state: %w", err)
	}
	if err := s.root.Rename(temporary, batchQueueStateFile); err != nil {
		return false, fmt.Errorf("publish metrics queue state: %w", err)
	}
	cleanup = false
	directory, err := s.root.Open(".")
	if err != nil {
		return true, fmt.Errorf("open metrics queue state directory: %w", err)
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return true, fmt.Errorf("sync metrics queue state directory: %w", err)
	}
	if err := directory.Close(); err != nil {
		return true, fmt.Errorf("close metrics queue state directory: %w", err)
	}
	return true, nil
}

func (s *batchQueueFileStore) Close() error {
	var unlockErr, lockErr error
	if s.lock != nil {
		unlockErr = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		lockErr = s.lock.Close()
		s.lock = nil
	}
	return errors.Join(unlockErr, lockErr, s.root.Close())
}

func acquireBatchQueueLock(root *os.Root) (*os.File, error) {
	const name = ".metrics-queue.lock"
	if info, err := root.Lstat(name); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o177 != 0 {
			return nil, errors.New("metrics queue lock must be a private regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect metrics queue lock: %w", err)
	}
	file, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open metrics queue lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock metrics queue state directory: %w", err)
	}
	return file, nil
}

func (s *batchQueueFileStore) cleanupTemporary() error {
	directory, err := s.root.Open(".")
	if err != nil {
		return fmt.Errorf("open metrics queue state directory for recovery: %w", err)
	}
	defer directory.Close()
	for {
		entries, err := directory.ReadDir(64)
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasPrefix(name, ".metrics-queue-") || len(name) != len(".metrics-queue-")+32 {
				continue
			}
			if _, decodeErr := hex.DecodeString(strings.TrimPrefix(name, ".metrics-queue-")); decodeErr != nil {
				continue
			}
			if removeErr := s.root.Remove(name); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				return fmt.Errorf("remove incomplete metrics queue state: %w", removeErr)
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("scan metrics queue state directory: %w", err)
		}
	}
}

func batchQueueDeviceHash(deviceID string) string {
	digest := sha256.Sum256([]byte(deviceID))
	return hex.EncodeToString(digest[:])
}

func batchQueueChecksum(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

/*
Package storage coordinates explicitly configured passive WAL checkpoints.

The manager never chooses scheduling or size defaults. Callers start its run
loop, provide an observer, and decide when to request a final checkpoint.
*/
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// CheckpointConfig bounds checkpoint scheduling and individual operations.
type CheckpointConfig struct {
	Interval          time.Duration
	WALThresholdBytes int64
	Timeout           time.Duration
}

// CheckpointObservation describes one attempted checkpoint or inspection error.
type CheckpointObservation struct {
	Database           string
	Reason             string
	WALBytes           int64
	Busy               int
	LogFrames          int
	CheckpointedFrames int
	Duration           time.Duration
	Err                error
}

// CheckpointObserver receives bounded operational checkpoint results.
type CheckpointObserver func(CheckpointObservation)

type checkpointTarget struct {
	name     string
	database *sql.DB
	walPath  string
}

type walIdentity struct {
	size    int64
	modTime time.Time
}

// CheckpointManager runs passive checkpoints without owning a goroutine.
type CheckpointManager struct {
	config   CheckpointConfig
	targets  []checkpointTarget
	observe  CheckpointObserver
	mu       sync.Mutex
	previous map[string]walIdentity
}

// NewCheckpointManager binds explicit policy to both Server databases.
func NewCheckpointManager(databases *Databases, config CheckpointConfig, observer CheckpointObserver) (*CheckpointManager, error) {
	if databases == nil || databases.Core == nil || databases.Metrics == nil || observer == nil {
		return nil, errors.New("databases and checkpoint observer are required")
	}
	if config.Interval <= 0 || config.WALThresholdBytes < 1 || config.Timeout <= 0 {
		return nil, errors.New("positive checkpoint interval, WAL byte threshold, and timeout are required")
	}
	return &CheckpointManager{
		config: config,
		targets: []checkpointTarget{
			{name: "core", database: databases.Core, walPath: databases.CorePath + "-wal"},
			{name: "metrics", database: databases.Metrics, walPath: databases.MetricsPath + "-wal"},
		},
		observe: observer, previous: make(map[string]walIdentity),
	}, nil
}

// Run checks WAL growth at the configured interval until the context ends.
func (m *CheckpointManager) Run(ctx context.Context) {
	ticker := time.NewTicker(m.config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.CheckIfNeeded(ctx)
		}
	}
}

// CheckIfNeeded checkpoints WAL files that crossed the byte threshold and changed.
func (m *CheckpointManager) CheckIfNeeded(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, target := range m.targets {
		info, err := os.Stat(target.walPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			m.observe(CheckpointObservation{Database: target.name, Reason: "threshold", Err: fmt.Errorf("inspect WAL: %w", err)})
			continue
		}
		identity := walIdentity{size: info.Size(), modTime: info.ModTime()}
		if identity.size < m.config.WALThresholdBytes || identity == m.previous[target.name] {
			continue
		}
		if observation := m.checkpoint(ctx, target, "threshold", identity.size); observation.Err == nil {
			if current, err := os.Stat(target.walPath); err == nil {
				identity = walIdentity{size: current.Size(), modTime: current.ModTime()}
			}
			m.previous[target.name] = identity
		}
	}
}

// CheckpointAll attempts one final passive checkpoint for both databases.
func (m *CheckpointManager) CheckpointAll(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, target := range m.targets {
		walBytes := int64(0)
		if info, err := os.Stat(target.walPath); err == nil {
			walBytes = info.Size()
		} else if !errors.Is(err, os.ErrNotExist) {
			m.observe(CheckpointObservation{Database: target.name, Reason: "shutdown", Err: fmt.Errorf("inspect WAL: %w", err)})
			continue
		}
		m.checkpoint(ctx, target, "shutdown", walBytes)
	}
}

// CheckpointMetricsForCapacity passively checkpoints metrics.db after emergency cleanup.
func (m *CheckpointManager) CheckpointMetricsForCapacity(ctx context.Context) CheckpointObservation {
	m.mu.Lock()
	defer m.mu.Unlock()
	target := m.targets[1]
	walBytes := int64(0)
	if info, err := os.Stat(target.walPath); err == nil {
		walBytes = info.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		observation := CheckpointObservation{Database: target.name, Reason: "capacity", Err: fmt.Errorf("inspect WAL: %w", err)}
		m.observe(observation)
		return observation
	}
	return m.checkpoint(ctx, target, "capacity", walBytes)
}

func (m *CheckpointManager) checkpoint(ctx context.Context, target checkpointTarget, reason string, walBytes int64) CheckpointObservation {
	started := time.Now()
	operationContext, cancel := context.WithTimeout(ctx, m.config.Timeout)
	defer cancel()
	observation := CheckpointObservation{Database: target.name, Reason: reason, WALBytes: walBytes}
	observation.Err = target.database.QueryRowContext(operationContext, `PRAGMA wal_checkpoint(PASSIVE)`).Scan(
		&observation.Busy, &observation.LogFrames, &observation.CheckpointedFrames,
	)
	observation.Duration = time.Since(started)
	if observation.Err != nil {
		observation.Err = fmt.Errorf("passive WAL checkpoint: %w", observation.Err)
	}
	m.observe(observation)
	return observation
}

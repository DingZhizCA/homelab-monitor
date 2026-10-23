/*
Package storage performs bounded SQLite WAL truncation and incremental vacuum.

It never runs a periodic full VACUUM. Legacy format conversion is a separate,
explicit startup operation that must finish before the HTTP listener starts.
*/
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"
)

// SpaceRecoveryConfig bounds scheduling, triggers, page work, and lock time.
type SpaceRecoveryConfig struct {
	Interval             time.Duration
	Timeout              time.Duration
	WALTruncateBytes     int64
	VacuumMinFreePages   int64
	VacuumMaxPagesPerRun int64
}

// SpaceRecoveryObservation reports one database inspection and recovery pass.
type SpaceRecoveryObservation struct {
	Database             string
	AllocatedBytesBefore int64
	AllocatedBytesAfter  int64
	FreePagesBefore      int64
	FreePagesAfter       int64
	WALBytesBefore       int64
	WALBytesAfter        int64
	VacuumAttempted      bool
	TruncateAttempted    bool
	Busy                 int
	LogFrames            int
	CheckpointedFrames   int
	Duration             time.Duration
	Err                  error
}

// SpaceRecoveryObserver receives each per-database maintenance result.
type SpaceRecoveryObserver func(SpaceRecoveryObservation)

type spaceRecoveryTarget struct {
	name     string
	database *sql.DB
	path     string
}

// SpaceRecoveryManager inspects both databases without owning a goroutine.
type SpaceRecoveryManager struct {
	config  SpaceRecoveryConfig
	targets []spaceRecoveryTarget
	observe SpaceRecoveryObserver
	mu      sync.Mutex
}

// NewSpaceRecoveryManager requires incremental vacuum mode and explicit bounds.
func NewSpaceRecoveryManager(ctx context.Context, databases *Databases, config SpaceRecoveryConfig, observer SpaceRecoveryObserver) (*SpaceRecoveryManager, error) {
	if databases == nil || databases.Core == nil || databases.Metrics == nil || observer == nil {
		return nil, errors.New("databases and space recovery observer are required")
	}
	if config.Interval <= 0 || config.Timeout <= 0 || config.WALTruncateBytes < 1 || config.VacuumMinFreePages < 1 || config.VacuumMaxPagesPerRun < 1 {
		return nil, errors.New("positive space recovery schedule, thresholds, page limit, and timeout are required")
	}
	targets := []spaceRecoveryTarget{{"core", databases.Core, databases.CorePath}, {"metrics", databases.Metrics, databases.MetricsPath}}
	for _, target := range targets {
		var mode int
		if err := target.database.QueryRowContext(ctx, `PRAGMA auto_vacuum`).Scan(&mode); err != nil {
			return nil, fmt.Errorf("read %s auto_vacuum mode: %w", target.name, err)
		}
		if mode != 2 {
			return nil, fmt.Errorf("%s database requires explicit one-time migration to incremental auto_vacuum", target.name)
		}
	}
	return &SpaceRecoveryManager{config: config, targets: targets, observe: observer}, nil
}

// Run performs one recovery inspection per interval until cancellation.
func (m *SpaceRecoveryManager) Run(ctx context.Context) {
	ticker := time.NewTicker(m.config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.Recover(ctx)
		}
	}
}

// Recover performs one bounded pass for each database.
func (m *SpaceRecoveryManager) Recover(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, target := range m.targets {
		m.recoverTarget(ctx, target)
	}
}

func (m *SpaceRecoveryManager) recoverTarget(ctx context.Context, target spaceRecoveryTarget) {
	started := time.Now()
	operationContext, cancel := context.WithTimeout(ctx, m.config.Timeout)
	defer cancel()
	before, err := readDatabaseUsage(operationContext, target.database, target.path)
	observation := SpaceRecoveryObservation{Database: target.name}
	if err != nil {
		observation.Err = fmt.Errorf("measure space before recovery: %w", err)
		m.finish(started, observation)
		return
	}
	observation.AllocatedBytesBefore = before.allocatedBytes
	observation.FreePagesBefore = before.pageCount - before.usedBytes/before.pageSize
	observation.WALBytesBefore = before.walBytes
	if observation.FreePagesBefore >= m.config.VacuumMinFreePages {
		observation.VacuumAttempted = true
		statement := fmt.Sprintf("PRAGMA incremental_vacuum(%d)", m.config.VacuumMaxPagesPerRun)
		if _, err := target.database.ExecContext(operationContext, statement); err != nil {
			observation.Err = fmt.Errorf("incremental vacuum: %w", err)
			m.finish(started, observation)
			return
		}
	}
	if before.walBytes >= m.config.WALTruncateBytes || observation.VacuumAttempted {
		observation.TruncateAttempted = true
		if err := target.database.QueryRowContext(operationContext, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&observation.Busy, &observation.LogFrames, &observation.CheckpointedFrames); err != nil {
			observation.Err = fmt.Errorf("truncate WAL checkpoint: %w", err)
			m.finish(started, observation)
			return
		}
		if observation.Busy != 0 {
			observation.Err = errors.New("truncate WAL checkpoint remained busy")
		}
	}
	after, err := readDatabaseUsage(operationContext, target.database, target.path)
	if err != nil && observation.Err == nil {
		observation.Err = fmt.Errorf("measure space after recovery: %w", err)
	} else if err == nil {
		observation.AllocatedBytesAfter = after.allocatedBytes
		observation.FreePagesAfter = after.pageCount - after.usedBytes/after.pageSize
		observation.WALBytesAfter = after.walBytes
	}
	m.finish(started, observation)
}

func (m *SpaceRecoveryManager) finish(started time.Time, observation SpaceRecoveryObservation) {
	observation.Duration = time.Since(started)
	m.observe(observation)
}

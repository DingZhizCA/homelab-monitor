/*
Package storage enforces explicit SQLite page limits and metric-ingestion
capacity checks. It may request bounded retention and passive checkpoint work,
but it never chooses limits or deletes unexpired data.
*/
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

// CapacityConfig defines hard database limits and shared-filesystem protection.
type CapacityConfig struct {
	CoreMaxBytes       int64
	MetricsSoftBytes   int64
	MetricsHardBytes   int64
	FilesystemMinBytes int64
}

// CapacityObservation describes a threshold transition or emergency cleanup.
type CapacityObservation struct {
	Level                 string
	MetricsUsedBytes      int64
	MetricsAllocatedBytes int64
	MetricsWALBytes       int64
	FilesystemFreeBytes   int64
	IncomingBytes         int64
	CleanupAttempted      bool
	RecoveryDeferred      bool
	DeletedSnapshots      int64
	DeletedReceipts       int64
	Err                   error
}

// CapacityObserver receives threshold transitions and hard-limit attempts.
type CapacityObserver func(CapacityObservation)

type databaseUsage struct {
	usedBytes      int64
	allocatedBytes int64
	walBytes       int64
	pageSize       int64
	pageCount      int64
}

// CapacityManager protects core.db, metrics.db, and their shared filesystem.
type CapacityManager struct {
	databases    *Databases
	config       CapacityConfig
	retention    *MetricRetentionManager
	checkpoint   *CheckpointManager
	observe      CapacityObserver
	mu           sync.Mutex
	lastLevel    string
	nextRecovery time.Time
	now          func() time.Time
}

// NewCapacityManager installs SQLite hard limits and verifies current files fit.
func NewCapacityManager(ctx context.Context, databases *Databases, config CapacityConfig, retention *MetricRetentionManager, checkpoint *CheckpointManager, observer CapacityObserver) (*CapacityManager, error) {
	if databases == nil || databases.Core == nil || databases.Metrics == nil || retention == nil || checkpoint == nil || observer == nil {
		return nil, errors.New("databases, retention, checkpoint, and capacity observer are required")
	}
	if config.CoreMaxBytes < 1 || config.MetricsSoftBytes < 1 || config.MetricsHardBytes <= config.MetricsSoftBytes || config.FilesystemMinBytes < 1 {
		return nil, errors.New("positive capacity limits with metrics soft bytes below hard bytes are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	coreLimit, err := calculateDatabaseLimit(ctx, databases.Core, "core", config.CoreMaxBytes)
	if err != nil {
		return nil, err
	}
	metricsLimit, err := calculateDatabaseLimit(ctx, databases.Metrics, "metrics", config.MetricsHardBytes)
	if err != nil {
		return nil, err
	}
	config.CoreMaxBytes = coreLimit.bytes
	config.MetricsHardBytes = metricsLimit.bytes
	if config.MetricsSoftBytes >= config.MetricsHardBytes {
		return nil, errors.New("metrics soft bytes must be below the page-aligned hard limit")
	}
	if coreLimit.previousPages != coreLimit.pages || metricsLimit.previousPages != metricsLimit.pages {
		return nil, errors.New("SQLite page limits must be installed on every connection before capacity management starts")
	}
	return &CapacityManager{
		databases: databases, config: config, retention: retention, checkpoint: checkpoint, observe: observer, now: time.Now,
	}, nil
}

// PrepareMetricWrite attempts bounded recovery at hard limits and rejects unsafe writes.
func (m *CapacityManager) PrepareMetricWrite(ctx context.Context, incomingBytes int64) error {
	return m.prepareMetricWrite(ctx, incomingBytes, false)
}

// RecoverMetricWrite forces one bounded recovery after SQLite reports FULL.
func (m *CapacityManager) RecoverMetricWrite(ctx context.Context, incomingBytes int64) error {
	return m.prepareMetricWrite(ctx, incomingBytes, true)
}

func (m *CapacityManager) prepareMetricWrite(ctx context.Context, incomingBytes int64, forceRecovery bool) error {
	if incomingBytes < 0 {
		return errors.New("incoming metric bytes must not be negative")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	observation, err := m.measure(ctx, incomingBytes)
	if err != nil {
		m.observe(CapacityObservation{Level: "hard", IncomingBytes: incomingBytes, Err: err})
		return fmt.Errorf("%w: %v", metrics.ErrMetricStorageCapacity, err)
	}
	level := m.level(observation)
	if level == "hard" || forceRecovery {
		level = "hard"
		now := m.now()
		if !m.nextRecovery.IsZero() && now.Before(m.nextRecovery) {
			observation.Level = level
			observation.RecoveryDeferred = true
			m.observe(observation)
			m.lastLevel = level
			return capacityError(observation, m.config)
		}
		m.nextRecovery = now.Add(m.retention.config.Interval)
		observation.CleanupAttempted = true
		cleanup := m.retention.Cleanup(ctx)
		observation.DeletedSnapshots = cleanup.DeletedSnapshots
		observation.DeletedReceipts = cleanup.DeletedReceipts
		if cleanup.Err == nil {
			checkpoint := m.checkpoint.CheckpointMetricsForCapacity(ctx)
			if checkpoint.Err != nil {
				cleanup.Err = checkpoint.Err
			}
		}
		after, measureErr := m.measure(ctx, incomingBytes)
		if measureErr == nil {
			after.CleanupAttempted = true
			after.DeletedSnapshots = observation.DeletedSnapshots
			after.DeletedReceipts = observation.DeletedReceipts
			observation = after
		}
		if cleanup.Err != nil {
			observation.Err = cleanup.Err
		} else if measureErr != nil {
			observation.Err = measureErr
		}
		level = m.level(observation)
		observation.Level = level
		m.observe(observation)
		m.lastLevel = level
		if level == "hard" || observation.Err != nil {
			return capacityError(observation, m.config)
		}
		m.nextRecovery = time.Time{}
		return nil
	}
	m.nextRecovery = time.Time{}
	if level != m.lastLevel {
		observation.Level = level
		m.observe(observation)
		m.lastLevel = level
	}
	return nil
}

func (m *CapacityManager) measure(ctx context.Context, incomingBytes int64) (CapacityObservation, error) {
	usage, err := readDatabaseUsage(ctx, m.databases.Metrics, m.databases.MetricsPath)
	if err != nil {
		return CapacityObservation{}, fmt.Errorf("measure metrics database: %w", err)
	}
	freeBytes, err := filesystemFreeBytes(filepath.Dir(m.databases.MetricsPath))
	if err != nil {
		return CapacityObservation{}, fmt.Errorf("measure storage filesystem: %w", err)
	}
	return CapacityObservation{
		MetricsUsedBytes: usage.usedBytes, MetricsAllocatedBytes: usage.allocatedBytes,
		MetricsWALBytes: usage.walBytes, FilesystemFreeBytes: freeBytes, IncomingBytes: incomingBytes,
	}, nil
}

func (m *CapacityManager) level(observation CapacityObservation) string {
	projectedUsed := saturatingAdd(observation.MetricsUsedBytes, observation.IncomingBytes)
	neededFree := saturatingAdd(m.config.FilesystemMinBytes, observation.IncomingBytes)
	if projectedUsed >= m.config.MetricsHardBytes || observation.FilesystemFreeBytes < neededFree {
		return "hard"
	}
	if projectedUsed >= m.config.MetricsSoftBytes {
		return "soft"
	}
	return "normal"
}

type databaseLimit struct {
	pages         int64
	bytes         int64
	previousPages int64
}

func calculateDatabaseLimit(ctx context.Context, database *sql.DB, name string, maxBytes int64) (databaseLimit, error) {
	usage, err := readDatabaseUsage(ctx, database, "")
	if err != nil {
		return databaseLimit{}, fmt.Errorf("measure %s database pages: %w", name, err)
	}
	maxPages := maxBytes / usage.pageSize
	if maxPages < 1 {
		return databaseLimit{}, fmt.Errorf("%s database byte limit is smaller than one %d-byte page", name, usage.pageSize)
	}
	if usage.pageCount > maxPages {
		return databaseLimit{}, fmt.Errorf("%s database uses %d pages but configured limit allows %d", name, usage.pageCount, maxPages)
	}
	var previousPages int64
	if err := database.QueryRowContext(ctx, `PRAGMA max_page_count`).Scan(&previousPages); err != nil {
		return databaseLimit{}, fmt.Errorf("read %s database page limit: %w", name, err)
	}
	return databaseLimit{pages: maxPages, bytes: maxPages * usage.pageSize, previousPages: previousPages}, nil
}

func capacityError(observation CapacityObservation, config CapacityConfig) error {
	message := fmt.Sprintf("used=%d hard=%d free=%d reserve=%d incoming=%d", observation.MetricsUsedBytes, config.MetricsHardBytes, observation.FilesystemFreeBytes, config.FilesystemMinBytes, observation.IncomingBytes)
	if observation.Err != nil {
		return fmt.Errorf("%w: %s: %v", metrics.ErrMetricStorageCapacity, message, observation.Err)
	}
	return fmt.Errorf("%w: %s", metrics.ErrMetricStorageCapacity, message)
}

func readDatabaseUsage(ctx context.Context, database *sql.DB, path string) (databaseUsage, error) {
	var usage databaseUsage
	if err := database.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&usage.pageSize); err != nil {
		return usage, err
	}
	if err := database.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&usage.pageCount); err != nil {
		return usage, err
	}
	var freePages int64
	if err := database.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&freePages); err != nil {
		return usage, err
	}
	if usage.pageSize < 1 || usage.pageCount < 0 || freePages < 0 || freePages > usage.pageCount || usage.pageCount > math.MaxInt64/usage.pageSize {
		return usage, errors.New("invalid SQLite page accounting")
	}
	usage.allocatedBytes = usage.pageCount * usage.pageSize
	usage.usedBytes = (usage.pageCount - freePages) * usage.pageSize
	if path != "" {
		if info, err := os.Stat(path + "-wal"); err == nil {
			usage.walBytes = info.Size()
		} else if !errors.Is(err, os.ErrNotExist) {
			return usage, err
		}
	}
	return usage, nil
}

func filesystemFreeBytes(path string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	if stat.Bsize <= 0 {
		return 0, errors.New("filesystem block size is not positive")
	}
	blockSize := uint64(stat.Bsize)
	if uint64(stat.Bavail) > math.MaxUint64/blockSize {
		return math.MaxInt64, nil
	}
	available := uint64(stat.Bavail) * blockSize
	if available > math.MaxInt64 {
		return math.MaxInt64, nil
	}
	return int64(available), nil
}

func saturatingAdd(left, right int64) int64 {
	if right > 0 && left > math.MaxInt64-right {
		return math.MaxInt64
	}
	return left + right
}

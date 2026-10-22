/*
Package storage persists retryable Agent metric batches in metrics.db.

Each batch identity, digest, and complete set of protocol envelopes commits in
one transaction. A separate bounded retention manager may delete envelopes and
later expire their retry identities without changing this writer's protocol.
*/
package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
	"github.com/mattn/go-sqlite3"
)

var metricMigrations = []Migration{
	{
		Version: 1,
		Name:    "metric_batches_and_snapshots",
		SQL: `CREATE TABLE metric_batches (
		batch_id TEXT PRIMARY KEY,
		device_id TEXT NOT NULL,
		content_sha256 BLOB NOT NULL CHECK (length(content_sha256) = 32),
		snapshot_count INTEGER NOT NULL CHECK (snapshot_count > 0)
	);
	CREATE TABLE metric_snapshots (
		batch_id TEXT NOT NULL REFERENCES metric_batches(batch_id) ON DELETE RESTRICT,
		sequence INTEGER NOT NULL CHECK (sequence >= 0),
		device_id TEXT NOT NULL,
		sampled_at TEXT NOT NULL,
		envelope_json BLOB NOT NULL,
		PRIMARY KEY (batch_id, sequence)
	);
	CREATE INDEX metric_snapshots_device_time
		ON metric_snapshots (device_id, sampled_at, batch_id, sequence);`,
	},
	{
		Version: 2,
		Name:    "metric_batch_received_time",
		SQL: `ALTER TABLE metric_batches ADD COLUMN received_at TEXT NOT NULL DEFAULT '';
	UPDATE metric_batches
	SET received_at = COALESCE(
		(SELECT MAX(sampled_at) FROM metric_snapshots WHERE metric_snapshots.batch_id = metric_batches.batch_id),
		'1970-01-01T00:00:00.000000000Z'
	)
		WHERE received_at = '';`,
	},
	{
		Version: 3,
		Name:    "metric_retention_indexes",
		SQL: `CREATE INDEX metric_snapshots_retention
		ON metric_snapshots (sampled_at, batch_id, sequence);
	CREATE INDEX metric_batches_retention
		ON metric_batches (received_at, batch_id);`,
	},
}

// MetricBatchWriter atomically persists and deduplicates bounded metric batches.
type MetricBatchWriter struct {
	database     *sql.DB
	maxSnapshots int
	guard        MetricWriteGuard
	writeToken   chan struct{}
}

// MetricWriteGuard checks storage capacity before a new batch transaction.
type MetricWriteGuard interface {
	PrepareMetricWrite(context.Context, int64) error
	RecoverMetricWrite(context.Context, int64) error
}

// NewMetricBatchWriter binds the adapter to metrics.db with an explicit batch limit.
func NewMetricBatchWriter(database *sql.DB, maxSnapshots int) (*MetricBatchWriter, error) {
	return NewMetricBatchWriterWithGuard(database, maxSnapshots, nil)
}

// NewMetricBatchWriterWithGuard adds an explicit production capacity guard.
func NewMetricBatchWriterWithGuard(database *sql.DB, maxSnapshots int, guard MetricWriteGuard) (*MetricBatchWriter, error) {
	if database == nil || maxSnapshots < 1 {
		return nil, errors.New("metrics database and positive snapshot limit are required")
	}
	token := make(chan struct{}, 1)
	token <- struct{}{}
	return &MetricBatchWriter{database: database, maxSnapshots: maxSnapshots, guard: guard, writeToken: token}, nil
}

// AppendBatch stores one whole batch, recognizes identical retries, and rejects ID reuse.
func (w *MetricBatchWriter) AppendBatch(ctx context.Context, batch metrics.Batch) (bool, error) {
	if err := batch.Validate(w.maxSnapshots); err != nil {
		return false, fmt.Errorf("validate metric batch: %w", err)
	}
	encodedBatch, err := json.Marshal(batch)
	if err != nil {
		return false, fmt.Errorf("encode metric batch: %w", err)
	}
	digest := sha256.Sum256(encodedBatch)
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-w.writeToken:
	}
	defer func() { w.writeToken <- struct{}{} }()
	var previousDigest []byte
	err = w.database.QueryRowContext(ctx, `SELECT content_sha256 FROM metric_batches WHERE batch_id = ?`, batch.ID).Scan(&previousDigest)
	if err == nil {
		if !bytes.Equal(previousDigest, digest[:]) {
			return false, metrics.ErrBatchConflict
		}
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("read existing metric batch: %w", err)
	}
	if w.guard != nil {
		if err := w.guard.PrepareMetricWrite(ctx, int64(len(encodedBatch))); err != nil {
			return false, err
		}
	}
	inserted, err := w.persistNewBatch(ctx, batch, digest)
	if !errors.Is(err, metrics.ErrMetricStorageCapacity) || w.guard == nil {
		return inserted, err
	}
	if err := w.guard.RecoverMetricWrite(ctx, int64(len(encodedBatch))); err != nil {
		return false, err
	}
	return w.persistNewBatch(ctx, batch, digest)
}

func (w *MetricBatchWriter) persistNewBatch(ctx context.Context, batch metrics.Batch, digest [sha256.Size]byte) (bool, error) {
	transaction, err := w.database.BeginTx(ctx, nil)
	if err != nil {
		return false, metricWriteError("begin metric batch", err)
	}
	defer transaction.Rollback()
	result, err := transaction.ExecContext(ctx, `INSERT INTO metric_batches
		(batch_id, device_id, content_sha256, snapshot_count, received_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(batch_id) DO NOTHING`, batch.ID, batch.DeviceID, digest[:], len(batch.Snapshots), storageTimestamp(time.Now()))
	if err != nil {
		return false, metricWriteError("insert metric batch identity", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("inspect metric batch insert: %w", err)
	}
	if inserted == 0 {
		var previousDigest []byte
		if err := transaction.QueryRowContext(ctx, `SELECT content_sha256 FROM metric_batches WHERE batch_id = ?`, batch.ID).Scan(&previousDigest); err != nil {
			return false, fmt.Errorf("read existing metric batch: %w", err)
		}
		if !bytes.Equal(previousDigest, digest[:]) {
			return false, metrics.ErrBatchConflict
		}
		return false, nil
	}
	for sequence, snapshot := range batch.Snapshots {
		encodedSnapshot, err := json.Marshal(snapshot)
		if err != nil {
			return false, fmt.Errorf("encode metric snapshot %d: %w", sequence, err)
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO metric_snapshots
			(batch_id, sequence, device_id, sampled_at, envelope_json)
			VALUES (?, ?, ?, ?, ?)`, batch.ID, sequence, batch.DeviceID, storageTimestamp(snapshot.Timestamp), encodedSnapshot); err != nil {
			return false, metricWriteError(fmt.Sprintf("insert metric snapshot %d", sequence), err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return false, metricWriteError("commit metric batch", err)
	}
	return true, nil
}

func metricWriteError(operation string, err error) error {
	var sqliteError sqlite3.Error
	if errors.As(err, &sqliteError) && sqliteError.Code == sqlite3.ErrFull {
		return fmt.Errorf("%w: %s: %v", metrics.ErrMetricStorageCapacity, operation, err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func storageTimestamp(value time.Time) string {
	return value.UTC().Format(storageTimeLayout)
}

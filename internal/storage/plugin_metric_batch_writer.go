/*
Package storage persists bounded plugin observation batches in metrics.db.

Batch identity and observations commit atomically, and retries are compared by
content digest before capacity recovery is attempted.
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
)

// PluginMetricBatchWriter atomically persists plugin observations.
type PluginMetricBatchWriter struct {
	database        *sql.DB
	maxObservations int
	maxSamples      int
	guard           MetricWriteGuard
	writeToken      chan struct{}
}

// NewPluginMetricBatchWriterWithGuard binds explicit protocol limits and storage protection.
func NewPluginMetricBatchWriterWithGuard(database *sql.DB, maxObservations, maxSamples int, guard MetricWriteGuard) (*PluginMetricBatchWriter, error) {
	if database == nil || maxObservations < 1 || maxSamples < 1 {
		return nil, errors.New("metrics database and positive plugin batch limits are required")
	}
	token := make(chan struct{}, 1)
	token <- struct{}{}
	return &PluginMetricBatchWriter{database: database, maxObservations: maxObservations, maxSamples: maxSamples, guard: guard, writeToken: token}, nil
}

// AppendPluginBatch stores a new batch or recognizes an identical retry.
func (w *PluginMetricBatchWriter) AppendPluginBatch(ctx context.Context, batch metrics.PluginBatch) (bool, error) {
	if err := batch.Validate(w.maxObservations, w.maxSamples); err != nil {
		return false, fmt.Errorf("validate plugin metric batch: %w", err)
	}
	encoded, err := json.Marshal(batch)
	if err != nil {
		return false, fmt.Errorf("encode plugin metric batch: %w", err)
	}
	digest := sha256.Sum256(encoded)
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-w.writeToken:
	}
	defer func() { w.writeToken <- struct{}{} }()
	var previous []byte
	err = w.database.QueryRowContext(ctx, `SELECT content_sha256 FROM plugin_metric_batches WHERE batch_id = ?`, batch.ID).Scan(&previous)
	if err == nil {
		if !bytes.Equal(previous, digest[:]) {
			return false, metrics.ErrBatchConflict
		}
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("read existing plugin metric batch: %w", err)
	}
	if w.guard != nil {
		if err := w.guard.PrepareMetricWrite(ctx, int64(len(encoded))); err != nil {
			return false, err
		}
	}
	inserted, err := w.persist(ctx, batch, digest)
	if !errors.Is(err, metrics.ErrMetricStorageCapacity) || w.guard == nil {
		return inserted, err
	}
	if err := w.guard.RecoverMetricWrite(ctx, int64(len(encoded))); err != nil {
		return false, err
	}
	return w.persist(ctx, batch, digest)
}

func (w *PluginMetricBatchWriter) persist(ctx context.Context, batch metrics.PluginBatch, digest [sha256.Size]byte) (bool, error) {
	tx, err := w.database.BeginTx(ctx, nil)
	if err != nil {
		return false, metricWriteError("begin plugin metric batch", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO plugin_metric_batches
		(batch_id, device_id, content_sha256, observation_count, received_at)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT(batch_id) DO NOTHING`, batch.ID, batch.DeviceID, digest[:], len(batch.Observations), storageTimestamp(time.Now()))
	if err != nil {
		return false, metricWriteError("insert plugin metric batch identity", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("inspect plugin metric batch insert: %w", err)
	}
	if rows == 0 {
		var previous []byte
		if err := tx.QueryRowContext(ctx, `SELECT content_sha256 FROM plugin_metric_batches WHERE batch_id = ?`, batch.ID).Scan(&previous); err != nil {
			return false, fmt.Errorf("read existing plugin metric batch: %w", err)
		}
		if !bytes.Equal(previous, digest[:]) {
			return false, metrics.ErrBatchConflict
		}
		return false, nil
	}
	for sequence, observation := range batch.Observations {
		encoded, err := json.Marshal(observation)
		if err != nil {
			return false, fmt.Errorf("encode plugin observation %d: %w", sequence, err)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO plugin_metric_observations
			(batch_id, sequence, device_id, plugin_id, plugin_version, collector_id, observed_at, observation_json)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, batch.ID, sequence, batch.DeviceID, observation.PluginID, observation.PluginVersion,
			observation.CollectorID, storageTimestamp(observation.ObservedAt), encoded)
		if err != nil {
			return false, metricWriteError(fmt.Sprintf("insert plugin observation %d", sequence), err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, metricWriteError("commit plugin metric batch", err)
	}
	return true, nil
}

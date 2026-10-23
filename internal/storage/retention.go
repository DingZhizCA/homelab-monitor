/*
Package storage applies explicitly configured, bounded retention to raw and
aggregated metric history plus retry receipts. It does not choose product
retention values or perform aggregation, checkpointing, or file compaction.
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

// RetentionConfig bounds the schedule, age windows, transaction work, and time.
type RetentionConfig struct {
	Interval         time.Duration
	RawRetention     time.Duration
	ReceiptRetention time.Duration
	MinuteRetention  time.Duration
	HourRetention    time.Duration
	DayRetention     time.Duration
	DeleteLimit      int
	Timeout          time.Duration
}

// RetentionObservation reports one bounded cleanup transaction.
type RetentionObservation struct {
	RawCutoff                 time.Time
	ReceiptCutoff             time.Time
	DeletedSnapshots          int64
	DeletedReceipts           int64
	DeletedPluginObservations int64
	DeletedPluginReceipts     int64
	DeletedMinute             int64
	DeletedHour               int64
	DeletedDay                int64
	Duration                  time.Duration
	Err                       error
}

// RetentionObserver receives every scheduled or directly requested result.
type RetentionObserver func(RetentionObservation)

// MetricRetentionManager deletes expired metric data without owning a goroutine.
type MetricRetentionManager struct {
	database *sql.DB
	config   RetentionConfig
	observe  RetentionObserver
	now      func() time.Time
	mu       sync.Mutex
}

// NewMetricRetentionManager binds explicit cleanup policy to metrics.db.
func NewMetricRetentionManager(database *sql.DB, config RetentionConfig, observer RetentionObserver) (*MetricRetentionManager, error) {
	if database == nil || observer == nil {
		return nil, errors.New("metrics database and retention observer are required")
	}
	if config.Interval <= 0 || config.RawRetention <= 0 || config.ReceiptRetention <= config.RawRetention || config.DeleteLimit < 1 || config.Timeout <= 0 {
		return nil, errors.New("positive retention settings, a receipt window longer than the raw window, and a positive delete limit are required")
	}
	if !validAggregateRetention(config) {
		return nil, errors.New("aggregate retention windows must all be disabled or increase from raw through day")
	}
	return &MetricRetentionManager{database: database, config: config, observe: observer, now: time.Now}, nil
}

// Run performs one bounded cleanup per interval until the context ends.
func (m *MetricRetentionManager) Run(ctx context.Context) {
	ticker := time.NewTicker(m.config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.Cleanup(ctx)
		}
	}
}

// Cleanup removes at most DeleteLimit snapshots and receipts in one transaction.
func (m *MetricRetentionManager) Cleanup(ctx context.Context) RetentionObservation {
	m.mu.Lock()
	defer m.mu.Unlock()
	started := time.Now()
	now := m.now().UTC()
	observation := RetentionObservation{
		RawCutoff: now.Add(-m.config.RawRetention), ReceiptCutoff: now.Add(-m.config.ReceiptRetention),
	}
	operationContext, cancel := context.WithTimeout(ctx, m.config.Timeout)
	defer cancel()
	transaction, err := m.database.BeginTx(operationContext, nil)
	if err != nil {
		observation.Err = fmt.Errorf("begin metric retention: %w", err)
		return m.finish(started, observation)
	}
	defer transaction.Rollback()
	result, err := transaction.ExecContext(operationContext, `DELETE FROM metric_snapshots
		WHERE rowid IN (
			SELECT snapshots.rowid FROM metric_snapshots AS snapshots
			WHERE snapshots.sampled_at < ?
				AND NOT EXISTS (
					SELECT 1 FROM metric_aggregation_pending AS pending
					WHERE pending.batch_id = snapshots.batch_id AND pending.sequence = snapshots.sequence
				)
			ORDER BY sampled_at, batch_id, sequence
			LIMIT ?
		)`, storageTimestamp(observation.RawCutoff), m.config.DeleteLimit)
	if err != nil {
		observation.Err = fmt.Errorf("delete expired metric snapshots: %w", err)
		return m.finish(started, observation)
	}
	observation.DeletedSnapshots, err = result.RowsAffected()
	if err != nil {
		observation.Err = fmt.Errorf("count deleted metric snapshots: %w", err)
		return m.finish(started, observation)
	}
	result, err = transaction.ExecContext(operationContext, `DELETE FROM plugin_metric_observations
		WHERE rowid IN (
			SELECT rowid FROM plugin_metric_observations
			WHERE observed_at < ?
			ORDER BY observed_at, batch_id, sequence
			LIMIT ?
		)`, storageTimestamp(observation.RawCutoff), m.config.DeleteLimit)
	if err != nil {
		observation.Err = fmt.Errorf("delete expired plugin metric observations: %w", err)
		return m.finish(started, observation)
	}
	observation.DeletedPluginObservations, err = result.RowsAffected()
	if err != nil {
		observation.Err = fmt.Errorf("count deleted plugin metric observations: %w", err)
		return m.finish(started, observation)
	}
	if configHasAggregateRetention(m.config) {
		for _, tier := range []struct {
			resolution int
			retention  time.Duration
			deleted    *int64
		}{{minuteResolution, m.config.MinuteRetention, &observation.DeletedMinute}, {hourResolution, m.config.HourRetention, &observation.DeletedHour}, {dayResolution, m.config.DayRetention, &observation.DeletedDay}} {
			result, err = transaction.ExecContext(operationContext, `DELETE FROM metric_aggregates WHERE rowid IN (
				SELECT rowid FROM metric_aggregates
				WHERE resolution_seconds = ? AND bucket_start < ?
				ORDER BY bucket_start, device_id, descriptor_id, dimensions_json
				LIMIT ?
			)`, tier.resolution, storageTimestamp(now.Add(-tier.retention)), m.config.DeleteLimit)
			if err != nil {
				observation.Err = fmt.Errorf("delete expired %d-second metric aggregates: %w", tier.resolution, err)
				return m.finish(started, observation)
			}
			*tier.deleted, err = result.RowsAffected()
			if err != nil {
				observation.Err = fmt.Errorf("count deleted %d-second metric aggregates: %w", tier.resolution, err)
				return m.finish(started, observation)
			}
		}
	}
	result, err = transaction.ExecContext(operationContext, `DELETE FROM metric_batches
		WHERE batch_id IN (
			SELECT batches.batch_id FROM metric_batches AS batches
			WHERE batches.received_at < ?
				AND NOT EXISTS (
					SELECT 1 FROM metric_snapshots AS snapshots
					WHERE snapshots.batch_id = batches.batch_id
				)
			ORDER BY batches.received_at, batches.batch_id
			LIMIT ?
		)`, storageTimestamp(observation.ReceiptCutoff), m.config.DeleteLimit)
	if err != nil {
		observation.Err = fmt.Errorf("delete expired metric receipts: %w", err)
		return m.finish(started, observation)
	}
	observation.DeletedReceipts, err = result.RowsAffected()
	if err != nil {
		observation.Err = fmt.Errorf("count deleted metric receipts: %w", err)
		return m.finish(started, observation)
	}
	result, err = transaction.ExecContext(operationContext, `DELETE FROM plugin_metric_batches
		WHERE batch_id IN (
			SELECT batches.batch_id FROM plugin_metric_batches AS batches
			WHERE batches.received_at < ?
				AND NOT EXISTS (
					SELECT 1 FROM plugin_metric_observations AS observations
					WHERE observations.batch_id = batches.batch_id
				)
			ORDER BY batches.received_at, batches.batch_id
			LIMIT ?
		)`, storageTimestamp(observation.ReceiptCutoff), m.config.DeleteLimit)
	if err != nil {
		observation.Err = fmt.Errorf("delete expired plugin metric receipts: %w", err)
		return m.finish(started, observation)
	}
	observation.DeletedPluginReceipts, err = result.RowsAffected()
	if err != nil {
		observation.Err = fmt.Errorf("count deleted plugin metric receipts: %w", err)
		return m.finish(started, observation)
	}
	if err := transaction.Commit(); err != nil {
		observation.DeletedSnapshots = 0
		observation.DeletedReceipts = 0
		observation.DeletedPluginObservations = 0
		observation.DeletedPluginReceipts = 0
		observation.DeletedMinute = 0
		observation.DeletedHour = 0
		observation.DeletedDay = 0
		observation.Err = fmt.Errorf("commit metric retention: %w", err)
	}
	return m.finish(started, observation)
}

func configHasAggregateRetention(config RetentionConfig) bool {
	return config.MinuteRetention != 0 || config.HourRetention != 0 || config.DayRetention != 0
}

func validAggregateRetention(config RetentionConfig) bool {
	if !configHasAggregateRetention(config) {
		return config.MinuteRetention == 0 && config.HourRetention == 0 && config.DayRetention == 0
	}
	return config.MinuteRetention > config.RawRetention && config.HourRetention > config.MinuteRetention && config.DayRetention > config.HourRetention
}

func (m *MetricRetentionManager) finish(started time.Time, observation RetentionObservation) RetentionObservation {
	observation.Duration = time.Since(started)
	m.observe(observation)
	return observation
}

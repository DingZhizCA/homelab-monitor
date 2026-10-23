/*
Package storage incrementally builds bounded metric history aggregates.

The pending queue and aggregate updates commit together, so retention cannot
remove a raw snapshot until every configured resolution has accepted it.
*/
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/dashboard"
	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

const (
	minuteResolution = 60
	hourResolution   = 3600
	dayResolution    = 86400
)

type AggregationConfig struct {
	Interval     time.Duration
	MaxSnapshots int
	Timeout      time.Duration
}

type AggregationObservation struct {
	ProcessedSnapshots int
	UpdatedBuckets     int
	PendingSnapshots   int64
	Duration           time.Duration
	Err                error
}

type AggregationObserver func(AggregationObservation)

type MetricAggregationManager struct {
	database *sql.DB
	registry *dashboard.MetricRegistry
	config   AggregationConfig
	observe  AggregationObserver
	mu       sync.Mutex
}

func NewMetricAggregationManager(database *sql.DB, registry *dashboard.MetricRegistry, config AggregationConfig, observer AggregationObserver) (*MetricAggregationManager, error) {
	if database == nil || registry == nil || observer == nil || config.Interval <= 0 || config.MaxSnapshots < 1 || config.Timeout <= 0 {
		return nil, errors.New("metrics database, descriptor registry, observer, and positive aggregation bounds are required")
	}
	transaction, err := database.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin metric aggregation activation: %w", err)
	}
	defer transaction.Rollback()
	var enabled int
	if err := transaction.QueryRow(`SELECT enabled FROM metric_aggregation_control WHERE id = 1`).Scan(&enabled); err != nil {
		return nil, fmt.Errorf("read metric aggregation activation: %w", err)
	}
	if _, err := transaction.Exec(`UPDATE metric_aggregation_control SET enabled = 1 WHERE id = 1`); err != nil {
		return nil, fmt.Errorf("enable metric aggregation queue: %w", err)
	}
	if enabled == 0 {
		if _, err := transaction.Exec(`INSERT OR IGNORE INTO metric_aggregation_pending (batch_id, sequence)
			SELECT batch_id, sequence FROM metric_snapshots ORDER BY rowid`); err != nil {
			return nil, fmt.Errorf("queue existing metric snapshots: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return nil, fmt.Errorf("commit metric aggregation activation: %w", err)
	}
	return &MetricAggregationManager{database: database, registry: registry, config: config, observe: observer}, nil
}

func (m *MetricAggregationManager) Run(ctx context.Context) {
	m.Aggregate(ctx)
	ticker := time.NewTicker(m.config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.Aggregate(ctx)
		}
	}
}

func (m *MetricAggregationManager) Aggregate(ctx context.Context) AggregationObservation {
	m.mu.Lock()
	defer m.mu.Unlock()
	started := time.Now()
	result := AggregationObservation{}
	operationContext, cancel := context.WithTimeout(ctx, m.config.Timeout)
	defer cancel()
	transaction, err := m.database.BeginTx(operationContext, nil)
	if err != nil {
		result.Err = fmt.Errorf("begin metric aggregation: %w", err)
		return m.finish(started, result)
	}
	defer transaction.Rollback()
	processedSnapshots, updatedBuckets := 0, 0
	rows, err := transaction.QueryContext(operationContext, `SELECT pending.id, snapshots.device_id, snapshots.sampled_at, snapshots.envelope_json
		FROM metric_aggregation_pending AS pending
		JOIN metric_snapshots AS snapshots ON snapshots.batch_id = pending.batch_id AND snapshots.sequence = pending.sequence
		ORDER BY pending.id LIMIT ?`, m.config.MaxSnapshots)
	if err != nil {
		result.Err = fmt.Errorf("read pending metric aggregation: %w", err)
		return m.finish(started, result)
	}
	type pendingSnapshot struct {
		id        int64
		deviceID  string
		sampledAt string
		envelope  []byte
	}
	var pending []pendingSnapshot
	for rows.Next() {
		var item pendingSnapshot
		if err := rows.Scan(&item.id, &item.deviceID, &item.sampledAt, &item.envelope); err != nil {
			rows.Close()
			result.Err = fmt.Errorf("scan pending metric aggregation: %w", err)
			return m.finish(started, result)
		}
		pending = append(pending, item)
	}
	if err := rows.Close(); err != nil {
		result.Err = fmt.Errorf("close pending metric aggregation: %w", err)
		return m.finish(started, result)
	}
	for _, item := range pending {
		var envelope metrics.MetricEnvelope
		if err := json.Unmarshal(item.envelope, &envelope); err != nil {
			result.Err = fmt.Errorf("decode pending metric snapshot: %w", err)
			return m.finish(started, result)
		}
		if err := envelope.Validate(); err != nil || envelope.Device.ID != item.deviceID || storageTimestamp(envelope.Timestamp) != item.sampledAt {
			result.Err = errors.New("pending metric snapshot violates its indexed envelope")
			return m.finish(started, result)
		}
		for _, descriptor := range m.registry.List() {
			points, err := m.registry.Extract(descriptor.ID, envelope)
			if err != nil {
				result.Err = fmt.Errorf("extract pending metric %s: %w", descriptor.ID, err)
				return m.finish(started, result)
			}
			for _, point := range points {
				dimensions, err := json.Marshal(nonNilDimensions(point.Dimensions))
				if err != nil {
					result.Err = fmt.Errorf("encode aggregate dimensions: %w", err)
					return m.finish(started, result)
				}
				for _, resolution := range []int{minuteResolution, hourResolution, dayResolution} {
					bucket := envelope.Timestamp.UTC().Truncate(time.Duration(resolution) * time.Second)
					if err := upsertMetricAggregate(operationContext, transaction, resolution, bucket, item.deviceID, descriptor.ID, string(dimensions), envelope.Timestamp, point.Value); err != nil {
						result.Err = err
						return m.finish(started, result)
					}
					updatedBuckets++
				}
			}
		}
		if _, err := transaction.ExecContext(operationContext, `DELETE FROM metric_aggregation_pending WHERE id = ?`, item.id); err != nil {
			result.Err = fmt.Errorf("complete pending metric aggregation: %w", err)
			return m.finish(started, result)
		}
		processedSnapshots++
	}
	if err := transaction.QueryRowContext(operationContext, `SELECT COUNT(*) FROM metric_aggregation_pending`).Scan(&result.PendingSnapshots); err != nil {
		result.Err = fmt.Errorf("count pending metric aggregation: %w", err)
		return m.finish(started, result)
	}
	if err := transaction.Commit(); err != nil {
		result.Err = fmt.Errorf("commit metric aggregation: %w", err)
	} else {
		result.ProcessedSnapshots = processedSnapshots
		result.UpdatedBuckets = updatedBuckets
	}
	return m.finish(started, result)
}

func upsertMetricAggregate(ctx context.Context, transaction *sql.Tx, resolution int, bucket time.Time, deviceID, descriptorID, dimensions string, sampledAt time.Time, value *float64) error {
	valueCount := 0
	var numeric any
	if value != nil {
		valueCount, numeric = 1, *value
	}
	_, err := transaction.ExecContext(ctx, `INSERT INTO metric_aggregates
		(resolution_seconds, bucket_start, device_id, descriptor_id, dimensions_json, sample_count, value_count, value_sum, value_min, value_max, last_value, last_sampled_at)
		VALUES (?, ?, ?, ?, ?, 1, ?, COALESCE(?, 0), ?, ?, ?, ?)
		ON CONFLICT(resolution_seconds, bucket_start, device_id, descriptor_id, dimensions_json) DO UPDATE SET
			sample_count = sample_count + 1,
			value_count = value_count + excluded.value_count,
			value_sum = value_sum + excluded.value_sum,
			value_min = CASE WHEN excluded.value_count = 0 THEN value_min WHEN value_min IS NULL OR excluded.value_min < value_min THEN excluded.value_min ELSE value_min END,
			value_max = CASE WHEN excluded.value_count = 0 THEN value_max WHEN value_max IS NULL OR excluded.value_max > value_max THEN excluded.value_max ELSE value_max END,
			last_value = CASE WHEN excluded.last_sampled_at >= last_sampled_at THEN excluded.last_value ELSE last_value END,
			last_sampled_at = MAX(last_sampled_at, excluded.last_sampled_at)`, resolution, storageTimestamp(bucket), deviceID, descriptorID, dimensions, valueCount, numeric, numeric, numeric, numeric, storageTimestamp(sampledAt))
	if err != nil {
		return fmt.Errorf("update metric aggregate: %w", err)
	}
	return nil
}

func nonNilDimensions(dimensions map[string]string) map[string]string {
	if dimensions == nil {
		return map[string]string{}
	}
	return dimensions
}

func (m *MetricAggregationManager) finish(started time.Time, result AggregationObservation) AggregationObservation {
	result.Duration = time.Since(started)
	m.observe(result)
	return result
}

/*
Package storage restores current device state from durable metric batches.

The source returns one validated latest snapshot per device together with the
original Server receipt time. It does not replay events or device controls.
*/
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

// CurrentMetricSnapshot is one device's durable latest state.
type CurrentMetricSnapshot struct {
	Envelope   metrics.MetricEnvelope
	ReceivedAt time.Time
}

// MetricCurrentSource reads latest snapshots without changing stored data.
type MetricCurrentSource struct {
	database *sql.DB
}

// NewMetricCurrentSource binds current-state recovery to metrics.db.
func NewMetricCurrentSource(database *sql.DB) (*MetricCurrentSource, error) {
	if database == nil {
		return nil, errors.New("metrics database is required")
	}
	return &MetricCurrentSource{database: database}, nil
}

// LoadLatest returns one validated snapshot per device in device ID order.
func (s *MetricCurrentSource) LoadLatest(ctx context.Context) ([]CurrentMetricSnapshot, error) {
	rows, err := s.database.QueryContext(ctx, `SELECT latest.device_id, selected.envelope_json, selected.received_at
		FROM (
			SELECT devices.device_id,
				(SELECT snapshots.rowid
				 FROM metric_snapshots AS snapshots
				 JOIN metric_batches AS batches ON batches.batch_id = snapshots.batch_id
				 WHERE snapshots.device_id = devices.device_id
				 ORDER BY snapshots.sampled_at DESC, batches.received_at ASC, snapshots.rowid ASC
				 LIMIT 1) AS snapshot_rowid
			FROM (SELECT DISTINCT device_id FROM metric_snapshots) AS devices
		) AS latest
		JOIN (
			SELECT snapshots.rowid, snapshots.envelope_json, batches.received_at
			FROM metric_snapshots AS snapshots
			JOIN metric_batches AS batches ON batches.batch_id = snapshots.batch_id
		) AS selected ON selected.rowid = latest.snapshot_rowid
		ORDER BY latest.device_id`)
	if err != nil {
		return nil, fmt.Errorf("query latest metric snapshots: %w", err)
	}
	defer rows.Close()

	result := make([]CurrentMetricSnapshot, 0)
	for rows.Next() {
		var deviceID string
		var encoded []byte
		var receivedText string
		if err := rows.Scan(&deviceID, &encoded, &receivedText); err != nil {
			return nil, fmt.Errorf("scan latest metric snapshot: %w", err)
		}
		var envelope metrics.MetricEnvelope
		if err := json.Unmarshal(encoded, &envelope); err != nil {
			return nil, fmt.Errorf("decode latest metric snapshot: %w", err)
		}
		if err := envelope.Validate(); err != nil {
			return nil, fmt.Errorf("validate latest metric snapshot: %w", err)
		}
		if envelope.Device.ID != deviceID {
			return nil, errors.New("latest metric snapshot device does not match its storage identity")
		}
		receivedAt, err := time.Parse(storageTimeLayout, receivedText)
		if err != nil {
			return nil, fmt.Errorf("parse latest metric receipt time: %w", err)
		}
		result = append(result, CurrentMetricSnapshot{Envelope: envelope, ReceivedAt: receivedAt})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate latest metric snapshots: %w", err)
	}
	return result, nil
}

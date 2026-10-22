/*
Package storage reads bounded historical descriptor points from metrics.db.

The adapter decodes already persisted MetricEnvelope values and uses the
Dashboard registry for field semantics. It never contacts an Agent or starts
collection, aggregation, retention, or background work.
*/
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/DingZhizCA/homelab-monitor/internal/dashboard"
	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

// MetricHistorySource serves bounded raw observations from metrics.db.
type MetricHistorySource struct {
	database *sql.DB
	registry *dashboard.MetricRegistry
}

// NewMetricHistorySource binds raw history reads to the approved descriptor registry.
func NewMetricHistorySource(database *sql.DB, registry *dashboard.MetricRegistry) (*MetricHistorySource, error) {
	if database == nil || registry == nil {
		return nil, errors.New("metrics database and descriptor registry are required")
	}
	return &MetricHistorySource{database: database, registry: registry}, nil
}

// QueryHistory returns at most the requested number of raw descriptor points.
func (s *MetricHistorySource) QueryHistory(ctx context.Context, request dashboard.HistoryRequest) ([]dashboard.HistoricalPoint, error) {
	if strings.TrimSpace(request.DeviceID) == "" || strings.TrimSpace(request.DeviceID) != request.DeviceID || request.From.IsZero() || request.To.IsZero() || !request.From.Before(request.To) || request.Limit < 1 {
		return nil, errors.New("device, positive time range, and positive point limit are required")
	}
	if _, ok := s.registry.Lookup(request.DescriptorID); !ok {
		return nil, dashboard.ErrUnsupportedMetric
	}
	rows, err := s.database.QueryContext(ctx, `SELECT sampled_at, envelope_json
		FROM metric_snapshots
		WHERE device_id = ? AND sampled_at >= ? AND sampled_at < ?
		ORDER BY sampled_at, batch_id, sequence`, request.DeviceID, storageTimestamp(request.From), storageTimestamp(request.To))
	if err != nil {
		return nil, fmt.Errorf("query metric snapshots: %w", err)
	}
	defer rows.Close()
	result := make([]dashboard.HistoricalPoint, 0, request.Limit)
	for rows.Next() {
		var sampledAt string
		var encoded []byte
		if err := rows.Scan(&sampledAt, &encoded); err != nil {
			return nil, fmt.Errorf("scan metric snapshot: %w", err)
		}
		var envelope metrics.MetricEnvelope
		if err := json.Unmarshal(encoded, &envelope); err != nil {
			return nil, fmt.Errorf("decode metric snapshot: %w", err)
		}
		if err := envelope.Validate(); err != nil || envelope.Device.ID != request.DeviceID || storageTimestamp(envelope.Timestamp) != sampledAt {
			return nil, errors.New("stored metric snapshot violates its indexed envelope")
		}
		points, err := s.registry.Extract(request.DescriptorID, envelope)
		if err != nil {
			return nil, fmt.Errorf("extract historical metric: %w", err)
		}
		for _, point := range points {
			quality := "valid"
			if point.Value == nil {
				quality = "missing"
			}
			result = append(result, dashboard.HistoricalPoint{
				DeviceID:     request.DeviceID,
				DescriptorID: request.DescriptorID,
				SampledAt:    envelope.Timestamp.UTC(),
				Value:        point.Value,
				Dimensions:   point.Dimensions,
				Quality:      quality,
			})
			if len(result) == request.Limit {
				return result, nil
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate metric snapshots: %w", err)
	}
	return result, nil
}

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
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/dashboard"
	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

// MetricHistorySource serves bounded raw observations from metrics.db.
type MetricHistorySource struct {
	database *sql.DB
	registry *dashboard.MetricRegistry
	tiers    *HistoryTierPolicy
	now      func() time.Time
}

// HistoryTierPolicy selects raw, minute, hour, and day data by age. Windows
// must strictly increase so every retained instant has one preferred source.
type HistoryTierPolicy struct {
	RawWindow    time.Duration
	MinuteWindow time.Duration
	HourWindow   time.Duration
	DayWindow    time.Duration
}

// NewMetricHistorySource binds raw history reads to the approved descriptor registry.
func NewMetricHistorySource(database *sql.DB, registry *dashboard.MetricRegistry) (*MetricHistorySource, error) {
	if database == nil || registry == nil {
		return nil, errors.New("metrics database and descriptor registry are required")
	}
	return &MetricHistorySource{database: database, registry: registry}, nil
}

// NewTieredMetricHistorySource enables age-based history routing.
func NewTieredMetricHistorySource(database *sql.DB, registry *dashboard.MetricRegistry, policy HistoryTierPolicy) (*MetricHistorySource, error) {
	if policy.RawWindow <= 0 || policy.MinuteWindow <= policy.RawWindow || policy.HourWindow <= policy.MinuteWindow || policy.DayWindow <= policy.HourWindow {
		return nil, errors.New("history tier windows must be positive and strictly increase from raw through day")
	}
	source, err := NewMetricHistorySource(database, registry)
	if err != nil {
		return nil, err
	}
	source.tiers = &policy
	source.now = time.Now
	return source, nil
}

// QueryHistory returns at most the requested number of raw descriptor points.
func (s *MetricHistorySource) QueryHistory(ctx context.Context, request dashboard.HistoryRequest) ([]dashboard.HistoricalPoint, error) {
	if strings.TrimSpace(request.DeviceID) == "" || strings.TrimSpace(request.DeviceID) != request.DeviceID || request.From.IsZero() || request.To.IsZero() || !request.From.Before(request.To) || request.Limit < 1 {
		return nil, errors.New("device, positive time range, and positive point limit are required")
	}
	if _, ok := s.registry.Lookup(request.DescriptorID); !ok {
		return nil, dashboard.ErrUnsupportedMetric
	}
	if s.tiers != nil {
		return s.queryTiered(ctx, request)
	}
	return s.queryRaw(ctx, request)
}

func (s *MetricHistorySource) queryRaw(ctx context.Context, request dashboard.HistoryRequest) ([]dashboard.HistoricalPoint, error) {
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

func (s *MetricHistorySource) queryTiered(ctx context.Context, request dashboard.HistoryRequest) ([]dashboard.HistoricalPoint, error) {
	now := s.now().UTC()
	tiers := []struct {
		from       time.Time
		to         time.Time
		resolution int
	}{
		{now.Add(-s.tiers.DayWindow), ceilTime(now.Add(-s.tiers.HourWindow), time.Hour), dayResolution},
		{ceilTime(now.Add(-s.tiers.HourWindow), time.Hour), ceilTime(now.Add(-s.tiers.MinuteWindow), time.Minute), hourResolution},
		{ceilTime(now.Add(-s.tiers.MinuteWindow), time.Minute), now.Add(-s.tiers.RawWindow), minuteResolution},
		{now.Add(-s.tiers.RawWindow), now.Add(time.Nanosecond), 0},
	}
	result := make([]dashboard.HistoricalPoint, 0, request.Limit)
	for _, tier := range tiers {
		from, to := latestTime(request.From, tier.from), earliestTime(request.To, tier.to)
		if !from.Before(to) || len(result) == request.Limit {
			continue
		}
		segment := request
		segment.From, segment.To, segment.Limit = from, to, request.Limit-len(result)
		var points []dashboard.HistoricalPoint
		var err error
		if tier.resolution == 0 {
			points, err = s.queryRaw(ctx, segment)
		} else {
			points, err = s.queryAggregates(ctx, segment, tier.resolution)
		}
		if err != nil {
			return nil, err
		}
		result = append(result, points...)
	}
	return result, nil
}

func (s *MetricHistorySource) queryAggregates(ctx context.Context, request dashboard.HistoryRequest, resolution int) ([]dashboard.HistoricalPoint, error) {
	rows, err := s.database.QueryContext(ctx, `SELECT bucket_start, dimensions_json, value_count, value_sum
		FROM metric_aggregates
		WHERE device_id = ? AND descriptor_id = ? AND resolution_seconds = ? AND bucket_start >= ? AND bucket_start < ?
		ORDER BY bucket_start, dimensions_json LIMIT ?`, request.DeviceID, request.DescriptorID, resolution, storageTimestamp(request.From), storageTimestamp(request.To), request.Limit)
	if err != nil {
		return nil, fmt.Errorf("query metric aggregates: %w", err)
	}
	defer rows.Close()
	result := make([]dashboard.HistoricalPoint, 0, request.Limit)
	for rows.Next() {
		var sampledAt, dimensionsJSON string
		var valueCount int64
		var valueSum float64
		if err := rows.Scan(&sampledAt, &dimensionsJSON, &valueCount, &valueSum); err != nil {
			return nil, fmt.Errorf("scan metric aggregate: %w", err)
		}
		parsed, err := time.Parse(storageTimeLayout, sampledAt)
		if err != nil {
			return nil, fmt.Errorf("parse metric aggregate time: %w", err)
		}
		var dimensions map[string]string
		if err := json.Unmarshal([]byte(dimensionsJSON), &dimensions); err != nil {
			return nil, fmt.Errorf("decode metric aggregate dimensions: %w", err)
		}
		point := dashboard.HistoricalPoint{DeviceID: request.DeviceID, DescriptorID: request.DescriptorID, SampledAt: parsed, Dimensions: dimensions, Quality: "missing"}
		if valueCount > 0 {
			value := valueSum / float64(valueCount)
			point.Value, point.Quality = &value, "estimated"
		}
		result = append(result, point)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate metric aggregates: %w", err)
	}
	return result, nil
}

func latestTime(left, right time.Time) time.Time {
	if left.After(right) {
		return left
	}
	return right
}

func earliestTime(left, right time.Time) time.Time {
	if left.Before(right) {
		return left
	}
	return right
}

func ceilTime(value time.Time, resolution time.Duration) time.Time {
	truncated := value.Truncate(resolution)
	if truncated.Equal(value) {
		return value
	}
	return truncated.Add(resolution)
}

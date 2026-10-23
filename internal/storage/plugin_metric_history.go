/*
Package storage reads bounded raw plugin observations from metrics.db.

The scan limit is separate from the returned point limit because one collector
observation may omit the requested metric or contain several labeled samples.
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

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
	"github.com/DingZhizCA/homelab-monitor/internal/pluginmetrics"
)

// PluginMetricHistorySource reads raw observations under an explicit scan cap.
type PluginMetricHistorySource struct {
	database   *sql.DB
	maxScanned int
	tiers      *HistoryTierPolicy
	now        func() time.Time
}

// NewTieredPluginMetricHistorySource enables the shared age-based history policy.
func NewTieredPluginMetricHistorySource(database *sql.DB, maxScanned int, policy HistoryTierPolicy) (*PluginMetricHistorySource, error) {
	if policy.RawWindow <= 0 || policy.MinuteWindow <= policy.RawWindow || policy.HourWindow <= policy.MinuteWindow || policy.DayWindow <= policy.HourWindow {
		return nil, errors.New("plugin history tier windows must be positive and strictly increase from raw through day")
	}
	source, err := NewPluginMetricHistorySource(database, maxScanned)
	if err != nil {
		return nil, err
	}
	source.tiers, source.now = &policy, time.Now
	return source, nil
}

// NewPluginMetricHistorySource requires an explicit per-query scan limit.
func NewPluginMetricHistorySource(database *sql.DB, maxScanned int) (*PluginMetricHistorySource, error) {
	if database == nil || maxScanned < 1 {
		return nil, errors.New("metrics database and positive plugin observation scan limit are required")
	}
	return &PluginMetricHistorySource{database: database, maxScanned: maxScanned}, nil
}

// QueryPluginHistory returns samples matching the exact structured source identity.
func (s *PluginMetricHistorySource) QueryPluginHistory(ctx context.Context, request pluginmetrics.HistoryRequest) ([]pluginmetrics.HistoricalPoint, error) {
	if strings.TrimSpace(request.DeviceID) == "" || request.From.IsZero() || request.To.IsZero() || !request.From.Before(request.To) || request.Limit < 1 {
		return nil, errors.New("valid plugin history request is required")
	}
	if s.tiers != nil {
		return s.queryTiered(ctx, request)
	}
	return s.queryRaw(ctx, request)
}

func (s *PluginMetricHistorySource) queryRaw(ctx context.Context, request pluginmetrics.HistoryRequest) ([]pluginmetrics.HistoricalPoint, error) {
	rows, err := s.database.QueryContext(ctx, `SELECT observed_at, observation_json
		FROM plugin_metric_observations
		WHERE device_id = ? AND plugin_id = ? AND plugin_version = ? AND collector_id = ?
			AND observed_at >= ? AND observed_at < ?
		ORDER BY observed_at, batch_id, sequence LIMIT ?`, request.DeviceID, request.PluginID, request.PluginVersion, request.CollectorID,
		storageTimestamp(request.From), storageTimestamp(request.To), s.maxScanned+1)
	if err != nil {
		return nil, fmt.Errorf("query plugin metric observations: %w", err)
	}
	defer rows.Close()
	result := make([]pluginmetrics.HistoricalPoint, 0, request.Limit)
	scanned := 0
	for rows.Next() {
		scanned++
		if scanned > s.maxScanned {
			return nil, pluginmetrics.ErrScanLimit
		}
		var observedAt string
		var encoded []byte
		if err := rows.Scan(&observedAt, &encoded); err != nil {
			return nil, fmt.Errorf("scan plugin metric observation: %w", err)
		}
		var observation metrics.PluginObservation
		if err := json.Unmarshal(encoded, &observation); err != nil || observation.Validate() != nil || observation.DeviceID != request.DeviceID || observation.PluginID != request.PluginID || observation.PluginVersion != request.PluginVersion || observation.CollectorID != request.CollectorID || storageTimestamp(observation.ObservedAt) != observedAt {
			return nil, errors.New("stored plugin metric observation violates its indexed envelope")
		}
		for _, sample := range observation.Samples {
			if sample.MetricID != request.MetricID {
				continue
			}
			result = append(result, pluginmetrics.HistoricalPoint{ObservedAt: observation.ObservedAt.UTC(), Type: sample.Type, Unit: sample.Unit, Value: append(json.RawMessage(nil), sample.Value...), Quality: sample.Quality, Labels: cloneStringMap(sample.Labels), AggregationKind: "raw"})
			if len(result) == request.Limit {
				return result, nil
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate plugin metric observations: %w", err)
	}
	return result, nil
}

func (s *PluginMetricHistorySource) queryTiered(ctx context.Context, request pluginmetrics.HistoryRequest) ([]pluginmetrics.HistoricalPoint, error) {
	now := s.now().UTC()
	tiers := []struct {
		from, to   time.Time
		resolution int
	}{
		{now.Add(-s.tiers.DayWindow), ceilTime(now.Add(-s.tiers.HourWindow), time.Hour), dayResolution},
		{ceilTime(now.Add(-s.tiers.HourWindow), time.Hour), ceilTime(now.Add(-s.tiers.MinuteWindow), time.Minute), hourResolution},
		{ceilTime(now.Add(-s.tiers.MinuteWindow), time.Minute), now.Add(-s.tiers.RawWindow), minuteResolution},
		{now.Add(-s.tiers.RawWindow), now.Add(time.Nanosecond), 0},
	}
	result := make([]pluginmetrics.HistoricalPoint, 0, request.Limit)
	for _, tier := range tiers {
		from, to := latestTime(request.From, tier.from), earliestTime(request.To, tier.to)
		if !from.Before(to) || len(result) == request.Limit {
			continue
		}
		segment := request
		segment.From, segment.To, segment.Limit = from, to, request.Limit-len(result)
		var points []pluginmetrics.HistoricalPoint
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

func (s *PluginMetricHistorySource) queryAggregates(ctx context.Context, request pluginmetrics.HistoryRequest, resolution int) ([]pluginmetrics.HistoricalPoint, error) {
	rows, err := s.database.QueryContext(ctx, `SELECT bucket_start, metric_type, unit, labels_json, aggregation_kind, summary_json, quality
		FROM plugin_metric_aggregates
		WHERE device_id = ? AND plugin_id = ? AND plugin_version = ? AND collector_id = ? AND metric_id = ?
			AND resolution_seconds = ? AND bucket_start >= ? AND bucket_start < ?
		ORDER BY bucket_start, labels_json LIMIT ?`, request.DeviceID, request.PluginID, request.PluginVersion, request.CollectorID, request.MetricID,
		resolution, storageTimestamp(request.From), storageTimestamp(request.To), request.Limit)
	if err != nil {
		return nil, fmt.Errorf("query plugin metric aggregates: %w", err)
	}
	defer rows.Close()
	result := make([]pluginmetrics.HistoricalPoint, 0, request.Limit)
	for rows.Next() {
		var timestamp, typ, unit, labelsJSON, kind, quality string
		var summary []byte
		if err := rows.Scan(&timestamp, &typ, &unit, &labelsJSON, &kind, &summary, &quality); err != nil {
			return nil, fmt.Errorf("scan plugin metric aggregate: %w", err)
		}
		observedAt, err := time.Parse(storageTimeLayout, timestamp)
		if err != nil {
			return nil, fmt.Errorf("parse plugin metric aggregate time: %w", err)
		}
		var labels map[string]string
		if err := json.Unmarshal([]byte(labelsJSON), &labels); err != nil || !json.Valid(summary) {
			return nil, errors.New("stored plugin metric aggregate is invalid")
		}
		result = append(result, pluginmetrics.HistoricalPoint{ObservedAt: observedAt, Type: typ, Unit: unit, Summary: append(json.RawMessage(nil), summary...), Quality: quality, Labels: labels, ResolutionSeconds: resolution, AggregationKind: kind})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate plugin metric aggregates: %w", err)
	}
	return result, nil
}

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

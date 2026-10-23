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

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
	"github.com/DingZhizCA/homelab-monitor/internal/pluginmetrics"
)

// PluginMetricHistorySource reads raw observations under an explicit scan cap.
type PluginMetricHistorySource struct {
	database   *sql.DB
	maxScanned int
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
			result = append(result, pluginmetrics.HistoricalPoint{ObservedAt: observation.ObservedAt.UTC(), Type: sample.Type, Unit: sample.Unit, Value: append(json.RawMessage(nil), sample.Value...), Quality: sample.Quality, Labels: cloneStringMap(sample.Labels)})
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

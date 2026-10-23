/*
Package storage reads the observed plugin metric descriptor catalog without
contacting Agents or plugin processes.
*/
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/pluginmetrics"
)

type PluginMetricDescriptorSource struct{ database *sql.DB }

func NewPluginMetricDescriptorSource(database *sql.DB) (*PluginMetricDescriptorSource, error) {
	if database == nil {
		return nil, errors.New("metrics database is required")
	}
	return &PluginMetricDescriptorSource{database: database}, nil
}

func (s *PluginMetricDescriptorSource) ListPluginMetricDescriptors(ctx context.Context, deviceID string, limit int) ([]pluginmetrics.Descriptor, error) {
	if deviceID == "" || limit < 1 {
		return nil, errors.New("device and positive descriptor limit are required")
	}
	rows, err := s.database.QueryContext(ctx, `SELECT plugin_id, plugin_version, collector_id, metric_id, metric_type, unit, label_keys_json, first_observed_at, last_observed_at
		FROM plugin_metric_descriptors WHERE device_id = ? ORDER BY plugin_id, plugin_version, collector_id, metric_id LIMIT ?`, deviceID, limit+1)
	if err != nil {
		return nil, fmt.Errorf("query plugin metric descriptors: %w", err)
	}
	defer rows.Close()
	items := make([]pluginmetrics.Descriptor, 0, limit)
	for rows.Next() {
		var item pluginmetrics.Descriptor
		var labelsJSON, first, last string
		item.DeviceID = deviceID
		if err := rows.Scan(&item.PluginID, &item.PluginVersion, &item.CollectorID, &item.MetricID, &item.Type, &item.Unit, &labelsJSON, &first, &last); err != nil {
			return nil, fmt.Errorf("scan plugin metric descriptor: %w", err)
		}
		if err := json.Unmarshal([]byte(labelsJSON), &item.LabelKeys); err != nil {
			return nil, errors.New("stored plugin metric descriptor labels are invalid")
		}
		item.FirstObserved, err = time.Parse(storageTimeLayout, first)
		if err != nil {
			return nil, errors.New("stored plugin metric descriptor first time is invalid")
		}
		item.LastObserved, err = time.Parse(storageTimeLayout, last)
		if err != nil {
			return nil, errors.New("stored plugin metric descriptor last time is invalid")
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate plugin metric descriptors: %w", err)
	}
	if len(items) > limit {
		return nil, pluginmetrics.ErrDescriptorLimit
	}
	return items, nil
}

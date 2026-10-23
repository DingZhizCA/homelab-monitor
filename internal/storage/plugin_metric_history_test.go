/*
Package storage tests bounded extraction from raw plugin observation rows.
*/
package storage

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
	"github.com/DingZhizCA/homelab-monitor/internal/pluginmetrics"
)

func TestPluginMetricHistorySourceFiltersMetricAndLabels(t *testing.T) {
	databases := openTestDatabases(t)
	writer, err := NewPluginMetricBatchWriterWithGuard(databases.Metrics, 2, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	batch := storagePluginBatch("plugin-history")
	batch.Observations[0].Samples = append(batch.Observations[0].Samples, metrics.PluginMetricObservation{MetricID: "temperature", Type: "gauge", Unit: "celsius", Value: json.RawMessage(`43`), Quality: "partial", Labels: map[string]string{"disk": "sdb"}})
	if inserted, err := writer.AppendPluginBatch(context.Background(), batch); err != nil || !inserted {
		t.Fatalf("inserted=%t err=%v", inserted, err)
	}
	source, err := NewPluginMetricHistorySource(databases.Metrics, 10)
	if err != nil {
		t.Fatal(err)
	}
	when := batch.Observations[0].ObservedAt
	points, err := source.QueryPluginHistory(context.Background(), pluginmetrics.HistoryRequest{DeviceID: "node-a", PluginID: "smart", PluginVersion: "1.0.0", CollectorID: "temperature", MetricID: "temperature", From: when.Add(-time.Second), To: when.Add(time.Second), Limit: 5})
	if err != nil || len(points) != 2 || points[1].Labels["disk"] != "sdb" {
		t.Fatalf("points=%+v err=%v", points, err)
	}
}

func TestPluginMetricHistorySourceEnforcesObservationScanLimit(t *testing.T) {
	databases := openTestDatabases(t)
	writer, err := NewPluginMetricBatchWriterWithGuard(databases.Metrics, 2, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	batch := storagePluginBatch("plugin-sparse")
	second := batch.Observations[0]
	second.ObservedAt = second.ObservedAt.Add(time.Second)
	batch.Observations = append(batch.Observations, second)
	if _, err := writer.AppendPluginBatch(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	source, err := NewPluginMetricHistorySource(databases.Metrics, 1)
	if err != nil {
		t.Fatal(err)
	}
	when := batch.Observations[0].ObservedAt
	_, err = source.QueryPluginHistory(context.Background(), pluginmetrics.HistoryRequest{DeviceID: "node-a", PluginID: "smart", PluginVersion: "1.0.0", CollectorID: "temperature", MetricID: "missing", From: when.Add(-time.Second), To: when.Add(2 * time.Second), Limit: 1})
	if !errors.Is(err, pluginmetrics.ErrScanLimit) {
		t.Fatalf("scan error=%v", err)
	}
}

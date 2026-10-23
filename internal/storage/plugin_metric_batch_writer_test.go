/*
Package storage tests durable plugin batch idempotency and atomic persistence.
*/
package storage

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

func TestPluginMetricBatchWriterPersistsDeduplicatesAndConflicts(t *testing.T) {
	databases := openTestDatabases(t)
	writer, err := NewPluginMetricBatchWriterWithGuard(databases.Metrics, 2, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	batch := storagePluginBatch("plugin-batch-a")
	if inserted, err := writer.AppendPluginBatch(context.Background(), batch); err != nil || !inserted {
		t.Fatalf("inserted=%t err=%v", inserted, err)
	}
	if inserted, err := writer.AppendPluginBatch(context.Background(), batch); err != nil || inserted {
		t.Fatalf("retry inserted=%t err=%v", inserted, err)
	}
	conflict := batch
	conflict.Observations[0].Samples[0].Value = json.RawMessage(`43`)
	if inserted, err := writer.AppendPluginBatch(context.Background(), conflict); !errors.Is(err, metrics.ErrBatchConflict) || inserted {
		t.Fatalf("conflict inserted=%t err=%v", inserted, err)
	}
	var stored []byte
	if err := databases.Metrics.QueryRow(`SELECT observation_json FROM plugin_metric_observations WHERE batch_id = ?`, batch.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var observation metrics.PluginObservation
	if err := json.Unmarshal(stored, &observation); err != nil || observation.PluginID != "smart" || !observation.ObservedAt.Equal(batch.Observations[0].ObservedAt) {
		t.Fatalf("observation=%+v err=%v", observation, err)
	}
}

func TestPluginMetricBatchWriterUsesCapacityGuard(t *testing.T) {
	databases := openTestDatabases(t)
	guard := &metricWriteGuardFake{err: metrics.ErrMetricStorageCapacity}
	writer, err := NewPluginMetricBatchWriterWithGuard(databases.Metrics, 1, 1, guard)
	if err != nil {
		t.Fatal(err)
	}
	if inserted, err := writer.AppendPluginBatch(context.Background(), storagePluginBatch("plugin-batch-full")); !errors.Is(err, metrics.ErrMetricStorageCapacity) || inserted || guard.calls != 1 {
		t.Fatalf("inserted=%t calls=%d err=%v", inserted, guard.calls, err)
	}
}

func storagePluginBatch(id string) metrics.PluginBatch {
	return metrics.PluginBatch{ID: id, DeviceID: "node-a", Observations: []metrics.PluginObservation{{
		SchemaVersion: 1, DeviceID: "node-a", PluginID: "smart", PluginVersion: "1.0.0", CollectorID: "temperature",
		ObservedAt: time.Date(2026, 10, 23, 1, 2, 3, 4, time.FixedZone("fixture", 8*60*60)),
		Samples:    []metrics.PluginMetricObservation{{MetricID: "temperature", Type: "gauge", Unit: "celsius", Value: json.RawMessage(`42`), Quality: "valid"}},
	}}}
}

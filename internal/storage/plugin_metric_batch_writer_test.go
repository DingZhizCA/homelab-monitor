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

	"github.com/DingZhizCA/homelab-monitor/internal/dashboard"
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

func TestPluginMetricBatchWriterRejectsObservedDescriptorDrift(t *testing.T) {
	databases := openTestDatabases(t)
	writer, _ := NewPluginMetricBatchWriterWithGuard(databases.Metrics, 1, 1, nil)
	first := storagePluginBatch("descriptor-first")
	first.Observations[0].Samples[0].Labels = map[string]string{"disk": "sda"}
	if _, err := writer.AppendPluginBatch(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := storagePluginBatch("descriptor-drift")
	second.Observations[0].ObservedAt = second.Observations[0].ObservedAt.Add(time.Second)
	second.Observations[0].Samples[0].Unit = "fahrenheit"
	second.Observations[0].Samples[0].Labels = map[string]string{"disk": "sdb"}
	if inserted, err := writer.AppendPluginBatch(context.Background(), second); err == nil || inserted {
		t.Fatalf("inserted=%t err=%v", inserted, err)
	}
	var batches int
	if err := databases.Metrics.QueryRow(`SELECT COUNT(*) FROM plugin_metric_batches`).Scan(&batches); err != nil || batches != 1 {
		t.Fatalf("batches=%d err=%v", batches, err)
	}
	source, err := NewPluginMetricDescriptorSource(databases.Metrics)
	if err != nil {
		t.Fatal(err)
	}
	items, err := source.ListPluginMetricDescriptors(context.Background(), "node-a", 2)
	if err != nil || len(items) != 1 || items[0].Unit != "celsius" || len(items[0].LabelKeys) != 1 || items[0].LabelKeys[0] != "disk" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	reference := dashboard.PluginMetricReference{PluginID: "smart", PluginVersion: "1.0.0", CollectorID: "temperature", MetricID: "temperature"}
	if exists, err := source.HasPluginMetricDescriptor(context.Background(), "node-a", reference); err != nil || !exists {
		t.Fatalf("exact descriptor exists=%t err=%v", exists, err)
	}
	reference.PluginVersion = "2.0.0"
	if exists, err := source.HasPluginMetricDescriptor(context.Background(), "node-a", reference); err != nil || exists {
		t.Fatalf("unobserved version exists=%t err=%v", exists, err)
	}
}

func storagePluginBatch(id string) metrics.PluginBatch {
	return metrics.PluginBatch{ID: id, DeviceID: "node-a", Observations: []metrics.PluginObservation{{
		SchemaVersion: 1, DeviceID: "node-a", PluginID: "smart", PluginVersion: "1.0.0", CollectorID: "temperature",
		ObservedAt: time.Date(2026, 10, 23, 1, 2, 3, 4, time.FixedZone("fixture", 8*60*60)),
		Samples:    []metrics.PluginMetricObservation{{MetricID: "temperature", Type: "gauge", Unit: "celsius", Value: json.RawMessage(`42`), Quality: "valid"}},
	}}}
}

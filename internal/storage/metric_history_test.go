/*
Package storage verifies bounded historical reads from persisted raw snapshots,
including descriptor extraction, quality, ordering, and corruption rejection.
*/
package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/dashboard"
	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

func TestMetricHistorySourceReturnsBoundedOrderedDescriptorPoints(t *testing.T) {
	databases := openTestDatabases(t)
	writer, err := NewMetricBatchWriter(databases.Metrics, 3)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	batch := storageBatch("batch-a", base.Add(time.Minute), 20)
	earlier := batch.Snapshots[0]
	earlier.Timestamp = base
	earlier.CPU.UsagePercent = 10
	batch.Snapshots = append([]metrics.MetricEnvelope{earlier}, batch.Snapshots[0])
	if inserted, err := writer.AppendBatch(context.Background(), batch); err != nil || !inserted {
		t.Fatalf("append inserted=%t err=%v", inserted, err)
	}
	source, err := NewMetricHistorySource(databases.Metrics, dashboard.NewMetricRegistry())
	if err != nil {
		t.Fatal(err)
	}
	points, err := source.QueryHistory(context.Background(), dashboard.HistoryRequest{
		DescriptorID: "cpu.usage_percent", DeviceID: "node-a", From: base, To: base.Add(2 * time.Minute), Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 || points[0].Value == nil || *points[0].Value != 10 || points[1].Value == nil || *points[1].Value != 20 || points[0].Quality != "valid" || points[0].ResolutionSeconds != 0 || points[0].AggregationKind != "raw" {
		t.Fatalf("points=%+v", points)
	}
}

func TestMetricHistorySourcePreservesDimensionsAndMissingQuality(t *testing.T) {
	databases := openTestDatabases(t)
	writer, err := NewMetricBatchWriter(databases.Metrics, 2)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	batch := storageBatch("batch-a", base, 10)
	withNetwork := batch.Snapshots[0]
	withNetwork.Timestamp = base.Add(time.Second)
	withNetwork.Networks = []metrics.NetworkMetrics{
		{Interface: "eth0", RXBytesPerSecond: 1},
		{Interface: "tailscale0", RXBytesPerSecond: 2},
	}
	batch.Snapshots = append(batch.Snapshots, withNetwork)
	if inserted, err := writer.AppendBatch(context.Background(), batch); err != nil || !inserted {
		t.Fatalf("append inserted=%t err=%v", inserted, err)
	}
	source, err := NewMetricHistorySource(databases.Metrics, dashboard.NewMetricRegistry())
	if err != nil {
		t.Fatal(err)
	}
	points, err := source.QueryHistory(context.Background(), dashboard.HistoryRequest{
		DescriptorID: "network.rx_bytes_per_second", DeviceID: "node-a", From: base.Add(-time.Second), To: base.Add(2 * time.Second), Limit: 1,
	})
	if err != nil || len(points) != 1 || points[0].Dimensions["interface"] != "eth0" {
		t.Fatalf("network points=%+v err=%v", points, err)
	}
	points, err = source.QueryHistory(context.Background(), dashboard.HistoryRequest{
		DescriptorID: "cpu.temperature_celsius", DeviceID: "node-a", From: base.Add(-time.Second), To: base.Add(time.Second), Limit: 1,
	})
	if err != nil || len(points) != 1 || points[0].Value != nil || points[0].Quality != "missing" {
		t.Fatalf("temperature points=%+v err=%v", points, err)
	}
}

func TestMetricHistorySourceRejectsInvalidRequestsAndCorruptRows(t *testing.T) {
	databases := openTestDatabases(t)
	registry := dashboard.NewMetricRegistry()
	if _, err := NewMetricHistorySource(nil, registry); err == nil {
		t.Fatal("nil database was accepted")
	}
	source, err := NewMetricHistorySource(databases.Metrics, registry)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	if _, err := source.QueryHistory(context.Background(), dashboard.HistoryRequest{DescriptorID: "unknown", DeviceID: "node-a", From: base, To: base.Add(time.Minute), Limit: 1}); !errors.Is(err, dashboard.ErrUnsupportedMetric) {
		t.Fatalf("unsupported error=%v", err)
	}
	if _, err := source.QueryHistory(context.Background(), dashboard.HistoryRequest{}); err == nil {
		t.Fatal("empty request was accepted")
	}
	writer, err := NewMetricBatchWriter(databases.Metrics, 1)
	if err != nil {
		t.Fatal(err)
	}
	if inserted, err := writer.AppendBatch(context.Background(), storageBatch("batch-a", base, 10)); err != nil || !inserted {
		t.Fatalf("append inserted=%t err=%v", inserted, err)
	}
	if _, err := databases.Metrics.Exec(`UPDATE metric_snapshots SET envelope_json = '{' WHERE batch_id = 'batch-a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.QueryHistory(context.Background(), dashboard.HistoryRequest{DescriptorID: "cpu.usage_percent", DeviceID: "node-a", From: base.Add(-time.Second), To: base.Add(time.Second), Limit: 1}); err == nil {
		t.Fatal("corrupt snapshot was accepted")
	}
}

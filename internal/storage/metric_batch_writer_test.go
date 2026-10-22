/*
Package storage verifies durable metric-batch idempotency, conflict handling,
transaction rollback, and process-local concurrent retry serialization.
*/
package storage

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
	"github.com/DingZhizCA/homelab-monitor/internal/testutil"
)

func TestMetricBatchWriterPersistsAndDeduplicatesAcrossReopen(t *testing.T) {
	directory := filepath.Join(testutil.TempDir(t), "storage")
	databases, err := Open(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := NewMetricBatchWriter(databases.Metrics, 4)
	if err != nil {
		t.Fatal(err)
	}
	batch := storageBatch("batch-a", time.Date(2026, 10, 22, 1, 2, 3, 4, time.FixedZone("fixture", 8*60*60)), 10)
	if inserted, err := writer.AppendBatch(context.Background(), batch); err != nil || !inserted {
		t.Fatalf("first append inserted=%t err=%v", inserted, err)
	}
	if err := databases.Close(); err != nil {
		t.Fatal(err)
	}
	databases, err = Open(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = databases.Close() })
	writer, err = NewMetricBatchWriter(databases.Metrics, 4)
	if err != nil {
		t.Fatal(err)
	}
	if inserted, err := writer.AppendBatch(context.Background(), batch); err != nil || inserted {
		t.Fatalf("retry append inserted=%t err=%v", inserted, err)
	}

	var sampledAt string
	var encoded []byte
	if err := databases.Metrics.QueryRow(`SELECT sampled_at, envelope_json FROM metric_snapshots WHERE batch_id = ?`, batch.ID).Scan(&sampledAt, &encoded); err != nil {
		t.Fatal(err)
	}
	if sampledAt != "2026-10-21T17:02:03.000000004Z" {
		t.Fatalf("sampled_at=%q", sampledAt)
	}
	var stored metrics.MetricEnvelope
	if err := json.Unmarshal(encoded, &stored); err != nil || !stored.Timestamp.Equal(batch.Snapshots[0].Timestamp) || stored.CPU.UsagePercent != 10 {
		t.Fatalf("stored snapshot timestamp=%s cpu=%v err=%v", stored.Timestamp, stored.CPU.UsagePercent, err)
	}
}

func TestMetricBatchWriterRejectsConflictingReuse(t *testing.T) {
	databases := openTestDatabases(t)
	writer, err := NewMetricBatchWriter(databases.Metrics, 2)
	if err != nil {
		t.Fatal(err)
	}
	batch := storageBatch("batch-a", time.Now().UTC(), 10)
	if inserted, err := writer.AppendBatch(context.Background(), batch); err != nil || !inserted {
		t.Fatalf("first append inserted=%t err=%v", inserted, err)
	}
	conflicting := storageBatch(batch.ID, batch.Snapshots[0].Timestamp, 20)
	if inserted, err := writer.AppendBatch(context.Background(), conflicting); !errors.Is(err, metrics.ErrBatchConflict) || inserted {
		t.Fatalf("conflicting append inserted=%t err=%v", inserted, err)
	}
	assertMetricCounts(t, databases, 1, 1)
}

func TestMetricBatchWriterRollsBackIdentityWhenSnapshotInsertFails(t *testing.T) {
	databases := openTestDatabases(t)
	if _, err := databases.Metrics.Exec(`CREATE TRIGGER reject_second_snapshot BEFORE INSERT ON metric_snapshots
		WHEN NEW.sequence = 1 BEGIN SELECT RAISE(ABORT, 'fixture rejection'); END`); err != nil {
		t.Fatal(err)
	}
	writer, err := NewMetricBatchWriter(databases.Metrics, 2)
	if err != nil {
		t.Fatal(err)
	}
	batch := storageBatch("batch-a", time.Now().UTC(), 10)
	second := batch.Snapshots[0]
	second.Timestamp = second.Timestamp.Add(time.Second)
	batch.Snapshots = append(batch.Snapshots, second)
	if inserted, err := writer.AppendBatch(context.Background(), batch); err == nil || inserted {
		t.Fatalf("rejected append inserted=%t err=%v", inserted, err)
	}
	assertMetricCounts(t, databases, 0, 0)
}

func TestMetricBatchWriterSerializesConcurrentRetries(t *testing.T) {
	databases := openTestDatabases(t)
	writer, err := NewMetricBatchWriter(databases.Metrics, 1)
	if err != nil {
		t.Fatal(err)
	}
	batch := storageBatch("batch-concurrent", time.Now().UTC(), 10)
	var inserts, failures atomic.Int32
	var group sync.WaitGroup
	for range 20 {
		group.Add(1)
		go func() {
			defer group.Done()
			inserted, err := writer.AppendBatch(context.Background(), batch)
			if err != nil {
				failures.Add(1)
			} else if inserted {
				inserts.Add(1)
			}
		}()
	}
	group.Wait()
	if inserts.Load() != 1 || failures.Load() != 0 {
		t.Fatalf("inserts=%d failures=%d", inserts.Load(), failures.Load())
	}
	assertMetricCounts(t, databases, 1, 1)
}

func TestMetricBatchWriterRequiresLimitsAndHonorsCancelledContext(t *testing.T) {
	databases := openTestDatabases(t)
	if _, err := NewMetricBatchWriter(nil, 1); err == nil {
		t.Fatal("nil database was accepted")
	}
	if _, err := NewMetricBatchWriter(databases.Metrics, 0); err == nil {
		t.Fatal("zero batch limit was accepted")
	}
	writer, err := NewMetricBatchWriter(databases.Metrics, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if inserted, err := writer.AppendBatch(ctx, storageBatch("batch-a", time.Now().UTC(), 10)); !errors.Is(err, context.Canceled) || inserted {
		t.Fatalf("cancelled append inserted=%t err=%v", inserted, err)
	}
	assertMetricCounts(t, databases, 0, 0)
}

func openTestDatabases(t *testing.T) *Databases {
	t.Helper()
	databases, err := Open(context.Background(), filepath.Join(testutil.TempDir(t), "storage"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = databases.Close() })
	return databases
}

func assertMetricCounts(t *testing.T, databases *Databases, wantBatches, wantSnapshots int) {
	t.Helper()
	var batches, snapshots int
	if err := databases.Metrics.QueryRow(`SELECT COUNT(*) FROM metric_batches`).Scan(&batches); err != nil {
		t.Fatal(err)
	}
	if err := databases.Metrics.QueryRow(`SELECT COUNT(*) FROM metric_snapshots`).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if batches != wantBatches || snapshots != wantSnapshots {
		t.Fatalf("batches=%d snapshots=%d, want %d and %d", batches, snapshots, wantBatches, wantSnapshots)
	}
}

func storageBatch(id string, timestamp time.Time, cpuUsage float64) metrics.Batch {
	return metrics.Batch{
		ID:       id,
		DeviceID: "node-a",
		Snapshots: []metrics.MetricEnvelope{{
			SchemaVersion:           1,
			Timestamp:               timestamp,
			SamplingMode:            "idle",
			SamplingIntervalSeconds: 30,
			Device:                  metrics.DeviceInfo{ID: "node-a", DisplayName: "Node A", Hostname: "node-a"},
			CPU:                     metrics.CPUMetrics{UsagePercent: cpuUsage, LogicalCores: 4},
			Memory:                  metrics.MemoryMetrics{TotalBytes: 1024, UsedBytes: 512, AvailableBytes: 512, UsagePercent: 50},
		}},
	}
}

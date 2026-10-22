/*
Package storage verifies current-state recovery from durable batches.
*/
package storage

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

func TestMetricCurrentSourceReturnsLatestValidatedSnapshotPerDevice(t *testing.T) {
	databases := openTestDatabases(t)
	writer, err := NewMetricBatchWriter(databases.Metrics, 2)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	newer := storageBatch("batch-newer", base.Add(time.Minute), 20)
	older := storageBatch("batch-older", base, 10)
	other := storageBatch("batch-other", base.Add(30*time.Second), 30)
	other.DeviceID = "node-b"
	other.Snapshots[0].Device.ID = "node-b"
	for _, batch := range []metrics.Batch{newer, older, other} {
		if inserted, err := writer.AppendBatch(context.Background(), batch); err != nil || !inserted {
			t.Fatalf("append %s inserted=%t err=%v", batch.ID, inserted, err)
		}
	}
	source, err := NewMetricCurrentSource(databases.Metrics)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := source.LoadLatest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 2 || latest[0].Envelope.Device.ID != "node-a" || latest[0].Envelope.CPU.UsagePercent != 20 || latest[1].Envelope.Device.ID != "node-b" {
		t.Fatalf("latest=%+v", latest)
	}
	for _, snapshot := range latest {
		if snapshot.ReceivedAt.IsZero() {
			t.Fatalf("zero receipt time for %s", snapshot.Envelope.Device.ID)
		}
	}
}

func TestMetricCurrentSourceRejectsCorruptLatestSnapshot(t *testing.T) {
	databases := openTestDatabases(t)
	writer, err := NewMetricBatchWriter(databases.Metrics, 1)
	if err != nil {
		t.Fatal(err)
	}
	batch := storageBatch("batch-a", time.Now().UTC(), 10)
	if inserted, err := writer.AppendBatch(context.Background(), batch); err != nil || !inserted {
		t.Fatalf("append inserted=%t err=%v", inserted, err)
	}
	if _, err := databases.Metrics.Exec(`UPDATE metric_snapshots SET envelope_json = '{' WHERE batch_id = ?`, batch.ID); err != nil {
		t.Fatal(err)
	}
	source, err := NewMetricCurrentSource(databases.Metrics)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.LoadLatest(context.Background()); err == nil {
		t.Fatal("corrupt latest snapshot was accepted")
	}
}

func TestMetricCurrentSourceRejectsSnapshotIdentityMismatch(t *testing.T) {
	databases := openTestDatabases(t)
	writer, err := NewMetricBatchWriter(databases.Metrics, 1)
	if err != nil {
		t.Fatal(err)
	}
	batch := storageBatch("batch-a", time.Now().UTC(), 10)
	if inserted, err := writer.AppendBatch(context.Background(), batch); err != nil || !inserted {
		t.Fatalf("append inserted=%t err=%v", inserted, err)
	}
	mismatched := batch.Snapshots[0]
	mismatched.Device.ID = "node-b"
	encoded, err := json.Marshal(mismatched)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := databases.Metrics.Exec(`UPDATE metric_snapshots SET envelope_json = ? WHERE batch_id = ?`, encoded, batch.ID); err != nil {
		t.Fatal(err)
	}
	source, err := NewMetricCurrentSource(databases.Metrics)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.LoadLatest(context.Background()); err == nil {
		t.Fatal("snapshot with mismatched device identity was accepted")
	}
}

func TestMetricCurrentSourceRequiresDatabase(t *testing.T) {
	if _, err := NewMetricCurrentSource(nil); err == nil {
		t.Fatal("nil database was accepted")
	}
}

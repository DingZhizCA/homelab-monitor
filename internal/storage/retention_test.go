/*
Package storage verifies bounded raw-metric and retry-receipt retention.
*/
package storage

import (
	"context"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

func TestMetricRetentionManagerRequiresExplicitPolicy(t *testing.T) {
	databases := openTestDatabases(t)
	valid := RetentionConfig{
		Interval: time.Hour, RawRetention: time.Hour, ReceiptRetention: 2 * time.Hour,
		DeleteLimit: 10, Timeout: time.Second,
	}
	if _, err := NewMetricRetentionManager(nil, valid, func(RetentionObservation) {}); err == nil {
		t.Fatal("nil database was accepted")
	}
	if _, err := NewMetricRetentionManager(databases.Metrics, RetentionConfig{}, func(RetentionObservation) {}); err == nil {
		t.Fatal("implicit policy was accepted")
	}
	invalidWindow := valid
	invalidWindow.ReceiptRetention = invalidWindow.RawRetention
	if _, err := NewMetricRetentionManager(databases.Metrics, invalidWindow, func(RetentionObservation) {}); err == nil {
		t.Fatal("receipt retention no longer than raw retention was accepted")
	}
	if _, err := NewMetricRetentionManager(databases.Metrics, valid, nil); err == nil {
		t.Fatal("missing observer was accepted")
	}
}

func TestMetricRetentionManagerDeletesExpiredSnapshotsBeforeReceipts(t *testing.T) {
	databases := openTestDatabases(t)
	writer, err := NewMetricBatchWriter(databases.Metrics, 1)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	expiredWithLiveReceipt := storageBatch("recent-receipt", now.Add(-2*time.Hour), 10)
	expiredWithOldReceipt := storageBatch("old-receipt", now.Add(-3*time.Hour), 20)
	current := storageBatch("current", now.Add(-30*time.Minute), 30)
	for _, fixture := range []struct {
		batch      metrics.Batch
		receivedAt time.Time
	}{
		{expiredWithLiveReceipt, now.Add(-30 * time.Minute)},
		{expiredWithOldReceipt, now.Add(-3 * time.Hour)},
		{current, now.Add(-30 * time.Minute)},
	} {
		if inserted, err := writer.AppendBatch(context.Background(), fixture.batch); err != nil || !inserted {
			t.Fatalf("append %s inserted=%t err=%v", fixture.batch.ID, inserted, err)
		}
		if _, err := databases.Metrics.Exec(`UPDATE metric_batches SET received_at = ? WHERE batch_id = ?`, storageTimestamp(fixture.receivedAt), fixture.batch.ID); err != nil {
			t.Fatal(err)
		}
	}
	var observations []RetentionObservation
	manager, err := NewMetricRetentionManager(databases.Metrics, RetentionConfig{
		Interval: time.Hour, RawRetention: time.Hour, ReceiptRetention: 2 * time.Hour,
		DeleteLimit: 10, Timeout: time.Second,
	}, func(observation RetentionObservation) { observations = append(observations, observation) })
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return now }
	result := manager.Cleanup(context.Background())
	if result.Err != nil || result.DeletedSnapshots != 2 || result.DeletedReceipts != 1 || len(observations) != 1 {
		t.Fatalf("cleanup result=%+v observations=%+v", result, observations)
	}
	assertMetricCounts(t, databases, 2, 1)
	if inserted, err := writer.AppendBatch(context.Background(), expiredWithLiveReceipt); err != nil || inserted {
		t.Fatalf("retained receipt retry inserted=%t err=%v", inserted, err)
	}
	if inserted, err := writer.AppendBatch(context.Background(), expiredWithOldReceipt); err != nil || !inserted {
		t.Fatalf("expired receipt retry inserted=%t err=%v", inserted, err)
	}
}

func TestMetricRetentionManagerDeletesPluginObservationsBeforeReceipts(t *testing.T) {
	databases := openTestDatabases(t)
	writer, err := NewPluginMetricBatchWriterWithGuard(databases.Metrics, 1, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	batch := storagePluginBatch("expired-plugin-batch")
	if inserted, err := writer.AppendPluginBatch(context.Background(), batch); err != nil || !inserted {
		t.Fatalf("inserted=%t err=%v", inserted, err)
	}
	old := storageTimestamp(time.Now().UTC().Add(-4 * time.Hour))
	if _, err := databases.Metrics.Exec(`UPDATE plugin_metric_observations SET observed_at = ?; UPDATE plugin_metric_batches SET received_at = ?`, old, old); err != nil {
		t.Fatal(err)
	}
	manager, err := NewMetricRetentionManager(databases.Metrics, RetentionConfig{
		Interval: time.Hour, RawRetention: time.Hour, ReceiptRetention: 2 * time.Hour, DeleteLimit: 10, Timeout: time.Second,
	}, func(RetentionObservation) {})
	if err != nil {
		t.Fatal(err)
	}
	result := manager.Cleanup(context.Background())
	if result.Err != nil || result.DeletedPluginObservations != 1 || result.DeletedPluginReceipts != 1 {
		t.Fatalf("result=%+v", result)
	}
}

func TestMetricRetentionManagerBoundsEachCleanupAndStops(t *testing.T) {
	databases := openTestDatabases(t)
	writer, err := NewMetricBatchWriter(databases.Metrics, 1)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	for index, id := range []string{"batch-a", "batch-b", "batch-c"} {
		batch := storageBatch(id, now.Add(-time.Duration(index+2)*time.Hour), 10)
		if inserted, err := writer.AppendBatch(context.Background(), batch); err != nil || !inserted {
			t.Fatalf("append %s inserted=%t err=%v", id, inserted, err)
		}
	}
	manager, err := NewMetricRetentionManager(databases.Metrics, RetentionConfig{
		Interval: time.Millisecond, RawRetention: time.Hour, ReceiptRetention: 4 * time.Hour,
		DeleteLimit: 1, Timeout: time.Second,
	}, func(RetentionObservation) {})
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return now }
	if result := manager.Cleanup(context.Background()); result.Err != nil || result.DeletedSnapshots != 1 {
		t.Fatalf("bounded cleanup=%+v", result)
	}
	assertMetricCounts(t, databases, 3, 2)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		manager.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("retention loop did not stop")
	}
}

func TestMetricRetentionManagerRollsBackARejectedCleanup(t *testing.T) {
	databases := openTestDatabases(t)
	writer, err := NewMetricBatchWriter(databases.Metrics, 1)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"batch-a", "batch-b"} {
		batch := storageBatch(id, now.Add(-2*time.Hour), 10)
		if inserted, err := writer.AppendBatch(context.Background(), batch); err != nil || !inserted {
			t.Fatalf("append %s inserted=%t err=%v", id, inserted, err)
		}
	}
	if _, err := databases.Metrics.Exec(`CREATE TRIGGER reject_second_retention_delete
		BEFORE DELETE ON metric_snapshots WHEN OLD.batch_id = 'batch-b'
		BEGIN SELECT RAISE(ABORT, 'fixture rejection'); END`); err != nil {
		t.Fatal(err)
	}
	var observed RetentionObservation
	manager, err := NewMetricRetentionManager(databases.Metrics, RetentionConfig{
		Interval: time.Hour, RawRetention: time.Hour, ReceiptRetention: 2 * time.Hour,
		DeleteLimit: 2, Timeout: time.Second,
	}, func(observation RetentionObservation) { observed = observation })
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return now }
	result := manager.Cleanup(context.Background())
	if result.Err == nil || observed.Err == nil || result.DeletedSnapshots != 0 || result.DeletedReceipts != 0 {
		t.Fatalf("rejected cleanup result=%+v observed=%+v", result, observed)
	}
	assertMetricCounts(t, databases, 2, 2)
}

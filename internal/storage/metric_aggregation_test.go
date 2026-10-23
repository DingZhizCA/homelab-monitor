/*
Package storage verifies transactional metric aggregation and tiered history
routing without scheduling collection or relying on production policy values.
*/
package storage

import (
	"context"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/dashboard"
)

func TestMetricAggregationManagerBackfillsBoundsAndAverages(t *testing.T) {
	databases := openTestDatabases(t)
	writer, err := NewMetricBatchWriter(databases.Metrics, 2)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	batch := storageBatch("batch-a", base.Add(5*time.Second), 10)
	second := batch.Snapshots[0]
	second.Timestamp = base.Add(35 * time.Second)
	second.CPU.UsagePercent = 20
	batch.Snapshots = append(batch.Snapshots, second)
	if inserted, err := writer.AppendBatch(context.Background(), batch); err != nil || !inserted {
		t.Fatalf("append inserted=%t err=%v", inserted, err)
	}
	manager, err := NewMetricAggregationManager(databases.Metrics, dashboard.NewMetricRegistry(), AggregationConfig{
		Interval: time.Hour, MaxSnapshots: 1, Timeout: time.Second,
	}, func(AggregationObservation) {})
	if err != nil {
		t.Fatal(err)
	}
	first := manager.Aggregate(context.Background())
	if first.Err != nil || first.ProcessedSnapshots != 1 || first.PendingSnapshots != 1 {
		t.Fatalf("first aggregation=%+v", first)
	}
	secondResult := manager.Aggregate(context.Background())
	if secondResult.Err != nil || secondResult.ProcessedSnapshots != 1 || secondResult.PendingSnapshots != 0 {
		t.Fatalf("second aggregation=%+v", secondResult)
	}
	var sampleCount, valueCount int
	var sum float64
	if err := databases.Metrics.QueryRow(`SELECT sample_count, value_count, value_sum FROM metric_aggregates
		WHERE resolution_seconds = 60 AND descriptor_id = 'cpu.usage_percent'`).Scan(&sampleCount, &valueCount, &sum); err != nil {
		t.Fatal(err)
	}
	if sampleCount != 2 || valueCount != 2 || sum != 30 {
		t.Fatalf("aggregate samples=%d values=%d sum=%v", sampleCount, valueCount, sum)
	}
	reopened, err := NewMetricAggregationManager(databases.Metrics, dashboard.NewMetricRegistry(), AggregationConfig{
		Interval: time.Hour, MaxSnapshots: 2, Timeout: time.Second,
	}, func(AggregationObservation) {})
	if err != nil {
		t.Fatal(err)
	}
	if result := reopened.Aggregate(context.Background()); result.Err != nil || result.ProcessedSnapshots != 0 || result.PendingSnapshots != 0 {
		t.Fatalf("reopened aggregation replayed committed snapshots: %+v", result)
	}
}

func TestTieredMetricHistorySelectsOneSourcePerAgeWindow(t *testing.T) {
	databases := openTestDatabases(t)
	writer, err := NewMetricBatchWriter(databases.Metrics, 4)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	ages := []time.Duration{72 * time.Hour, 12 * time.Hour, 2 * time.Hour, 30 * time.Minute}
	batch := storageBatch("batch-a", now.Add(-ages[0]), 10)
	for index, age := range ages[1:] {
		snapshot := batch.Snapshots[0]
		snapshot.Timestamp = now.Add(-age)
		snapshot.CPU.UsagePercent = float64((index + 2) * 10)
		batch.Snapshots = append(batch.Snapshots, snapshot)
	}
	if inserted, err := writer.AppendBatch(context.Background(), batch); err != nil || !inserted {
		t.Fatalf("append inserted=%t err=%v", inserted, err)
	}
	manager, err := NewMetricAggregationManager(databases.Metrics, dashboard.NewMetricRegistry(), AggregationConfig{
		Interval: time.Hour, MaxSnapshots: 4, Timeout: time.Second,
	}, func(AggregationObservation) {})
	if err != nil {
		t.Fatal(err)
	}
	if result := manager.Aggregate(context.Background()); result.Err != nil || result.PendingSnapshots != 0 {
		t.Fatalf("aggregate=%+v", result)
	}
	source, err := NewTieredMetricHistorySource(databases.Metrics, dashboard.NewMetricRegistry(), HistoryTierPolicy{
		RawWindow: time.Hour, MinuteWindow: 6 * time.Hour, HourWindow: 48 * time.Hour, DayWindow: 10 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	source.now = func() time.Time { return now }
	points, err := source.QueryHistory(context.Background(), dashboard.HistoryRequest{
		DescriptorID: "cpu.usage_percent", DeviceID: "node-a", From: now.Add(-5 * 24 * time.Hour), To: now, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 4 {
		t.Fatalf("points=%+v", points)
	}
	for index, point := range points {
		if index < 3 && point.Quality != "estimated" {
			t.Fatalf("aggregate point %d quality=%q", index, point.Quality)
		}
		if index == 3 && point.Quality != "valid" {
			t.Fatalf("raw point quality=%q", point.Quality)
		}
	}
}

func TestAggregationQueueProtectsRawSnapshotsFromRetention(t *testing.T) {
	databases := openTestDatabases(t)
	manager, err := NewMetricAggregationManager(databases.Metrics, dashboard.NewMetricRegistry(), AggregationConfig{
		Interval: time.Hour, MaxSnapshots: 1, Timeout: time.Second,
	}, func(AggregationObservation) {})
	if err != nil {
		t.Fatal(err)
	}
	_ = manager
	writer, err := NewMetricBatchWriter(databases.Metrics, 1)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	if inserted, err := writer.AppendBatch(context.Background(), storageBatch("batch-a", now.Add(-2*time.Hour), 10)); err != nil || !inserted {
		t.Fatalf("append inserted=%t err=%v", inserted, err)
	}
	retention, err := NewMetricRetentionManager(databases.Metrics, RetentionConfig{
		Interval: time.Hour, RawRetention: time.Hour, ReceiptRetention: 3 * time.Hour, DeleteLimit: 10, Timeout: time.Second,
	}, func(RetentionObservation) {})
	if err != nil {
		t.Fatal(err)
	}
	retention.now = func() time.Time { return now }
	if result := retention.Cleanup(context.Background()); result.Err != nil || result.DeletedSnapshots != 0 {
		t.Fatalf("retention removed pending raw data: %+v", result)
	}
}

func TestMetricRetentionDeletesEachExpiredAggregateTier(t *testing.T) {
	databases := openTestDatabases(t)
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	for _, resolution := range []int{minuteResolution, hourResolution, dayResolution} {
		if _, err := databases.Metrics.Exec(`INSERT INTO metric_aggregates
			(resolution_seconds, bucket_start, device_id, descriptor_id, dimensions_json, sample_count, value_count, value_sum, value_min, value_max, last_value, last_sampled_at)
			VALUES (?, ?, 'node-a', 'cpu.usage_percent', '{}', 1, 1, 10, 10, 10, 10, ?)`, resolution, storageTimestamp(now.Add(-40*24*time.Hour)), storageTimestamp(now.Add(-40*24*time.Hour))); err != nil {
			t.Fatal(err)
		}
	}
	manager, err := NewMetricRetentionManager(databases.Metrics, RetentionConfig{
		Interval: time.Hour, RawRetention: time.Hour, ReceiptRetention: 60 * 24 * time.Hour,
		MinuteRetention: 2 * time.Hour, HourRetention: 7 * 24 * time.Hour, DayRetention: 30 * 24 * time.Hour,
		DeleteLimit: 1, Timeout: time.Second,
	}, func(RetentionObservation) {})
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return now }
	result := manager.Cleanup(context.Background())
	if result.Err != nil || result.DeletedMinute != 1 || result.DeletedHour != 1 || result.DeletedDay != 1 {
		t.Fatalf("aggregate retention=%+v", result)
	}
}

/*
Package storage verifies typed plugin aggregation, late arrival recomputation,
and bounded queue processing independently from the Server scheduler.
*/
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
	"github.com/DingZhizCA/homelab-monitor/internal/pluginmetrics"
)

func TestPluginMetricAggregationPreservesTypedSemantics(t *testing.T) {
	databases := openTestDatabases(t)
	base := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	writer, err := NewPluginMetricBatchWriterWithGuard(databases.Metrics, 3, 12, nil)
	if err != nil {
		t.Fatal(err)
	}
	batch := typedPluginBatch("typed-a", base, []typedValues{
		{offset: 5 * time.Second, gauge: `10`, counter: `100`, state: `"idle"`},
		{offset: 35 * time.Second, gauge: `20`, counter: `120`, state: `"active"`},
		{offset: 25 * time.Second, gauge: `15`, counter: `5`, counterQuality: "reset", state: `"idle"`},
	})
	if inserted, err := writer.AppendPluginBatch(context.Background(), batch); err != nil || !inserted {
		t.Fatalf("append inserted=%t err=%v", inserted, err)
	}
	manager, err := NewPluginMetricAggregationManager(databases.Metrics, PluginAggregationConfig{
		Interval: time.Hour, MaxObservations: 3, MaxScanned: 100, RawRetention: time.Hour, Timeout: time.Second,
	}, func(PluginAggregationObservation) {})
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return base.Add(time.Hour) }
	result := manager.Aggregate(context.Background())
	if result.Err != nil || result.ProcessedObservations != 3 || result.PendingObservations != 0 {
		t.Fatalf("aggregation=%+v", result)
	}
	assertPluginSummary(t, databases.Metrics, "temperature", "gauge_summary", map[string]float64{
		"mean": 15, "min": 10, "max": 20, "last": 20, "sample_count": 3,
	})
	assertPluginSummary(t, databases.Metrics, "requests", "counter_delta", map[string]float64{
		"delta": 120, "first": 100, "last": 120, "reset_count": 1, "sample_count": 3,
	})
	assertPluginSummary(t, databases.Metrics, "mode", "state_summary", map[string]any{
		"last": "active", "transition_count": float64(1), "sample_count": float64(3),
	})
	assertPluginSummary(t, databases.Metrics, "failure", "event_count", map[string]float64{
		"event_count": 3, "sample_count": 3,
	})
	var minuteQuality, dayQuality string
	if err := databases.Metrics.QueryRow(`SELECT quality FROM plugin_metric_aggregates WHERE resolution_seconds = 60 AND metric_id = 'temperature'`).Scan(&minuteQuality); err != nil {
		t.Fatal(err)
	}
	if err := databases.Metrics.QueryRow(`SELECT quality FROM plugin_metric_aggregates WHERE resolution_seconds = 86400 AND metric_id = 'temperature'`).Scan(&dayQuality); err != nil {
		t.Fatal(err)
	}
	if minuteQuality != "estimated" || dayQuality != "partial" {
		t.Fatalf("minute quality=%q day quality=%q", minuteQuality, dayQuality)
	}
}

func TestPluginMetricAggregationRecomputesLateObservation(t *testing.T) {
	databases := openTestDatabases(t)
	base := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	writer, _ := NewPluginMetricBatchWriterWithGuard(databases.Metrics, 1, 4, nil)
	if inserted, err := writer.AppendPluginBatch(context.Background(), typedPluginBatch("late-new", base, []typedValues{{offset: 40 * time.Second, gauge: `20`, counter: `20`, state: `"active"`}})); err != nil || !inserted {
		t.Fatal(err)
	}
	manager, err := NewPluginMetricAggregationManager(databases.Metrics, PluginAggregationConfig{Interval: time.Hour, MaxObservations: 1, MaxScanned: 100, RawRetention: time.Hour, Timeout: time.Second}, func(PluginAggregationObservation) {})
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return base.Add(2 * time.Minute) }
	if result := manager.Aggregate(context.Background()); result.Err != nil {
		t.Fatal(result.Err)
	}
	if inserted, err := writer.AppendPluginBatch(context.Background(), typedPluginBatch("late-old", base, []typedValues{{offset: 10 * time.Second, gauge: `10`, counter: `10`, state: `"idle"`}})); err != nil || !inserted {
		t.Fatal(err)
	}
	if result := manager.Aggregate(context.Background()); result.Err != nil {
		t.Fatal(result.Err)
	}
	assertPluginSummary(t, databases.Metrics, "temperature", "gauge_summary", map[string]float64{"mean": 15, "min": 10, "max": 20, "last": 20, "sample_count": 2})
	assertPluginSummary(t, databases.Metrics, "requests", "counter_delta", map[string]float64{"delta": 10, "first": 10, "last": 20, "reset_count": 0, "sample_count": 2})
}

func TestPluginMetricAggregationRollsBackAtScanLimit(t *testing.T) {
	databases := openTestDatabases(t)
	base := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	writer, _ := NewPluginMetricBatchWriterWithGuard(databases.Metrics, 2, 8, nil)
	batch := typedPluginBatch("bounded", base, []typedValues{{offset: time.Second, gauge: `1`, counter: `1`, state: `"a"`}, {offset: 2 * time.Second, gauge: `2`, counter: `2`, state: `"b"`}})
	if _, err := writer.AppendPluginBatch(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	manager, err := NewPluginMetricAggregationManager(databases.Metrics, PluginAggregationConfig{Interval: time.Hour, MaxObservations: 2, MaxScanned: 1, RawRetention: time.Hour, Timeout: time.Second}, func(PluginAggregationObservation) {})
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return base.Add(time.Minute) }
	result := manager.Aggregate(context.Background())
	if result.Err == nil || result.ProcessedObservations != 0 {
		t.Fatalf("aggregation=%+v", result)
	}
	var pending int
	if err := databases.Metrics.QueryRow(`SELECT COUNT(*) FROM plugin_metric_aggregation_pending`).Scan(&pending); err != nil || pending != 2 {
		t.Fatalf("pending=%d err=%v", pending, err)
	}
}

func TestTieredPluginMetricHistoryExposesResolutionAndAggregationKind(t *testing.T) {
	databases := openTestDatabases(t)
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	oldBucket := now.Add(-2 * time.Hour).Truncate(time.Minute)
	if _, err := databases.Metrics.Exec(`INSERT INTO plugin_metric_aggregates
		(resolution_seconds, bucket_start, device_id, plugin_id, plugin_version, collector_id, metric_id, metric_type, unit, labels_json, aggregation_kind, summary_json, quality, sample_count, first_observed_at, last_observed_at)
		VALUES (60, ?, 'node-a', 'typed', '1.0.0', 'collector', 'temperature', 'gauge', 'celsius', '{"sensor":"cpu"}', 'gauge_summary', '{"mean":15,"min":10,"max":20,"last":20,"sample_count":2}', 'estimated', 2, ?, ?)`, storageTimestamp(oldBucket), storageTimestamp(oldBucket), storageTimestamp(oldBucket.Add(30*time.Second))); err != nil {
		t.Fatal(err)
	}
	writer, _ := NewPluginMetricBatchWriterWithGuard(databases.Metrics, 1, 4, nil)
	if _, err := writer.AppendPluginBatch(context.Background(), typedPluginBatch("recent", now.Add(-30*time.Minute), []typedValues{{gauge: `30`, counter: `30`, state: `"active"`}})); err != nil {
		t.Fatal(err)
	}
	source, err := NewTieredPluginMetricHistorySource(databases.Metrics, 10, HistoryTierPolicy{RawWindow: time.Hour, MinuteWindow: 6 * time.Hour, HourWindow: 48 * time.Hour, DayWindow: 10 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	source.now = func() time.Time { return now }
	points, err := source.QueryPluginHistory(context.Background(), pluginmetrics.HistoryRequest{DeviceID: "node-a", PluginID: "typed", PluginVersion: "1.0.0", CollectorID: "collector", MetricID: "temperature", From: now.Add(-3 * time.Hour), To: now, Limit: 10})
	if err != nil || len(points) != 2 {
		t.Fatalf("points=%+v err=%v", points, err)
	}
	if points[0].ResolutionSeconds != 60 || points[0].AggregationKind != "gauge_summary" || len(points[0].Summary) == 0 || len(points[0].Value) != 0 {
		t.Fatalf("aggregate point=%+v", points[0])
	}
	if points[1].ResolutionSeconds != 0 || points[1].AggregationKind != "raw" || string(points[1].Value) != "30" || len(points[1].Summary) != 0 {
		t.Fatalf("raw point=%+v", points[1])
	}
}

func TestPluginAggregationQueueProtectsRawObservationFromRetention(t *testing.T) {
	databases := openTestDatabases(t)
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	manager, err := NewPluginMetricAggregationManager(databases.Metrics, PluginAggregationConfig{Interval: time.Hour, MaxObservations: 1, MaxScanned: 10, RawRetention: time.Hour, Timeout: time.Second}, func(PluginAggregationObservation) {})
	if err != nil {
		t.Fatal(err)
	}
	_ = manager
	writer, _ := NewPluginMetricBatchWriterWithGuard(databases.Metrics, 1, 4, nil)
	if _, err := writer.AppendPluginBatch(context.Background(), typedPluginBatch("pending-old", now.Add(-2*time.Hour), []typedValues{{gauge: `1`, counter: `1`, state: `"idle"`}})); err != nil {
		t.Fatal(err)
	}
	retention, err := NewMetricRetentionManager(databases.Metrics, RetentionConfig{Interval: time.Hour, RawRetention: time.Hour, ReceiptRetention: 3 * time.Hour, DeleteLimit: 10, Timeout: time.Second}, func(RetentionObservation) {})
	if err != nil {
		t.Fatal(err)
	}
	retention.now = func() time.Time { return now }
	result := retention.Cleanup(context.Background())
	if result.Err != nil || result.DeletedPluginObservations != 0 {
		t.Fatalf("retention=%+v", result)
	}
}

type typedValues struct {
	offset                time.Duration
	gauge, counter, state string
	counterQuality        string
}

func typedPluginBatch(id string, base time.Time, values []typedValues) metrics.PluginBatch {
	observations := make([]metrics.PluginObservation, 0, len(values))
	for _, value := range values {
		quality := value.counterQuality
		if quality == "" {
			quality = "valid"
		}
		observations = append(observations, metrics.PluginObservation{
			SchemaVersion: 1, DeviceID: "node-a", PluginID: "typed", PluginVersion: "1.0.0", CollectorID: "collector", ObservedAt: base.Add(value.offset),
			Samples: []metrics.PluginMetricObservation{
				{MetricID: "temperature", Type: "gauge", Unit: "celsius", Value: json.RawMessage(value.gauge), Quality: "valid", Labels: map[string]string{"sensor": "cpu"}},
				{MetricID: "requests", Type: "counter", Unit: "count", Value: json.RawMessage(value.counter), Quality: quality},
				{MetricID: "mode", Type: "state", Unit: "text", Value: json.RawMessage(value.state), Quality: "valid"},
				{MetricID: "failure", Type: "event", Unit: "event", Value: json.RawMessage(`{"code":"E"}`), Quality: "valid"},
			},
		})
	}
	return metrics.PluginBatch{ID: id, DeviceID: "node-a", Observations: observations}
}

func assertPluginSummary(t *testing.T, database *sql.DB, metricID, kind string, expected any) {
	t.Helper()
	var actualKind string
	var encoded []byte
	if err := database.QueryRow(`SELECT aggregation_kind, summary_json FROM plugin_metric_aggregates WHERE resolution_seconds = 60 AND metric_id = ?`, metricID).Scan(&actualKind, &encoded); err != nil {
		t.Fatal(err)
	}
	if actualKind != kind {
		t.Fatalf("kind=%q want=%q", actualKind, kind)
	}
	var actual any
	if err := json.Unmarshal(encoded, &actual); err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(expected)
	var normalized any
	_ = json.Unmarshal(want, &normalized)
	if !reflect.DeepEqual(actual, normalized) {
		t.Fatalf("summary=%s want=%s", encoded, want)
	}
}

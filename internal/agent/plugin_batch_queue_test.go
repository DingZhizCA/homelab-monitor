/*
Package agent tests bounded plugin observation batching, retry, and acknowledgment.
*/
package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

func TestPluginBatchQueueRetainsPendingIDAcrossFailedSend(t *testing.T) {
	limits := testPluginQueueLimits()
	queue, err := NewPluginBatchQueue("node-a", limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Append(testPluginObservation(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	batch, ok, err := queue.PendingBatch()
	if err != nil || !ok {
		t.Fatalf("pending=%t err=%v", ok, err)
	}
	failed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "later", http.StatusServiceUnavailable) }))
	defer failed.Close()
	if _, sent, err := queue.SendPending(context.Background(), NewSender(failed.URL, "")); err == nil || !sent {
		t.Fatalf("failed send sent=%t err=%v", sent, err)
	}
	retry, ok, err := queue.PendingBatch()
	if err != nil || !ok || retry.ID != batch.ID {
		t.Fatalf("retry=%+v pending=%t err=%v", retry, ok, err)
	}
	success := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/plugin-metrics/batch" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"accepted_observations": 1, "duplicate": false})
	}))
	defer success.Close()
	if _, sent, err := queue.SendPending(context.Background(), NewSender(success.URL, "")); err != nil || !sent {
		t.Fatalf("successful send sent=%t err=%v", sent, err)
	}
	if observations, samples, bytes := queue.Usage(); observations != 0 || samples != 0 || bytes != 0 {
		t.Fatalf("usage=%d,%d,%d", observations, samples, bytes)
	}
}

func TestPluginBatchQueueEnforcesAllCapacities(t *testing.T) {
	limits := testPluginQueueLimits()
	limits.MaxQueuedObservations = 1
	limits.MaxBatchObservations = 1
	queue, err := NewPluginBatchQueue("node-a", limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Append(testPluginObservation(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	if err := queue.Append(testPluginObservation(time.Now().UTC().Add(time.Second))); err != ErrPluginBatchQueueFull {
		t.Fatalf("second append err=%v", err)
	}
	oversized := testPluginObservation(time.Now().UTC())
	oversized.Samples = append(oversized.Samples, oversized.Samples[0])
	oversized.Samples[1].MetricID = "other"
	if err := queue.Append(oversized); err == nil {
		t.Fatal("oversized single observation was accepted")
	}
}

func testPluginQueueLimits() PluginBatchQueueLimits {
	return PluginBatchQueueLimits{MaxQueuedObservations: 4, MaxQueuedSamples: 4, MaxQueuedBytes: 64 * 1024, MaxBatchObservations: 2, MaxBatchSamples: 1, MaxBatchBodyBytes: 16 * 1024}
}

func testPluginObservation(observedAt time.Time) metrics.PluginObservation {
	return metrics.PluginObservation{SchemaVersion: 1, DeviceID: "node-a", PluginID: "smart", PluginVersion: "1.0.0", CollectorID: "temperature", ObservedAt: observedAt,
		Samples: []metrics.PluginMetricObservation{{MetricID: "temperature", Type: "gauge", Unit: "celsius", Value: json.RawMessage(`42`), Quality: "valid"}}}
}

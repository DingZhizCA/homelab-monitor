package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	agentpkg "github.com/DingZhizCA/homelab-monitor/internal/agent"
	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

type pluginLoopRunner struct{ calls chan context.Context }

func (r *pluginLoopRunner) ReconcileOnce(ctx context.Context) error {
	r.calls <- ctx
	<-ctx.Done()
	return nil
}

func TestPluginControlLoopUsesIndependentCallerSelectedCadence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runner := &pluginLoopRunner{calls: make(chan context.Context, 2)}
	done := make(chan struct{})
	go func() {
		pluginControlLoop(ctx, runner, time.Hour, time.Second)
		close(done)
	}()
	operationCtx := <-runner.calls
	if _, ok := operationCtx.Deadline(); !ok {
		t.Fatal("plugin control operation had no deadline")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("plugin control loop did not stop after context cancellation")
	}
	if len(runner.calls) != 0 {
		t.Fatal("plugin loop polled again before the selected interval")
	}
}

func TestBatchReporterRetainsFailureAndLaterSendsQueuedSnapshots(t *testing.T) {
	attempts := 0
	batchSizes := make([]int, 0, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var batch metrics.Batch
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			t.Error(err)
		}
		batchSizes = append(batchSizes, len(batch.Snapshots))
		attempts++
		if attempts == 1 {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]int{"accepted_snapshots": len(batch.Snapshots)})
	}))
	defer server.Close()

	queue, err := agentpkg.NewBatchQueue("node-a", 4, 64*1024, 2, 32*1024)
	if err != nil {
		t.Fatal(err)
	}
	reporter := &batchReporter{queue: queue, sender: agentpkg.NewSender(server.URL, "")}
	base := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	if _, err := reporter.Send(context.Background(), mainTestEnvelope(base)); err == nil {
		t.Fatal("first temporary failure was accepted")
	}
	if _, err := reporter.Send(context.Background(), mainTestEnvelope(base.Add(time.Minute))); err != nil {
		t.Fatalf("retry pending batch: %v", err)
	}
	if queue.Len() != 1 {
		t.Fatalf("queue length after retry=%d, want one newer snapshot retained", queue.Len())
	}
	if _, err := reporter.Send(context.Background(), mainTestEnvelope(base.Add(2*time.Minute))); err != nil {
		t.Fatalf("send accumulated batch: %v", err)
	}
	if queue.Len() != 0 {
		t.Fatalf("queue length after recovery=%d, want empty", queue.Len())
	}
	if len(batchSizes) != 3 || batchSizes[0] != 1 || batchSizes[1] != 1 || batchSizes[2] != 2 {
		t.Fatalf("batch sizes=%v, want [1 1 2]", batchSizes)
	}
}

func TestBatchReporterRejectsNewSnapshotWhenFailedQueueIsFull(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporary", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	queue, err := agentpkg.NewBatchQueue("node-a", 1, 32*1024, 1, 16*1024)
	if err != nil {
		t.Fatal(err)
	}
	reporter := &batchReporter{queue: queue, sender: agentpkg.NewSender(server.URL, "")}
	base := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	if _, err := reporter.Send(context.Background(), mainTestEnvelope(base)); err == nil {
		t.Fatal("temporary send failure was accepted")
	}
	if _, err := reporter.Send(context.Background(), mainTestEnvelope(base.Add(time.Minute))); !errors.Is(err, agentpkg.ErrBatchQueueFull) {
		t.Fatalf("full queue error=%v, want ErrBatchQueueFull", err)
	}
	if queue.Len() != 1 {
		t.Fatalf("full queue length=%d, want retained pending snapshot", queue.Len())
	}
}

func mainTestEnvelope(at time.Time) metrics.MetricEnvelope {
	return metrics.MetricEnvelope{
		SchemaVersion:           1,
		SamplingMode:            "idle",
		SamplingIntervalSeconds: 60,
		Device:                  metrics.DeviceInfo{ID: "node-a", DisplayName: "Node A", Hostname: "node-a"},
		Timestamp:               at,
		System:                  metrics.SystemMetrics{UptimeSeconds: 1},
		CPU:                     metrics.CPUMetrics{UsagePercent: 1, LogicalCores: 1},
		Memory:                  metrics.MemoryMetrics{TotalBytes: 2, UsedBytes: 1, AvailableBytes: 1, UsagePercent: 50},
	}
}

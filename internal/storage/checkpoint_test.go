/*
Package storage verifies explicit passive checkpoint scheduling and reporting.
*/
package storage

import (
	"context"
	"testing"
	"time"
)

func TestCheckpointManagerRequiresExplicitPolicyAndObserver(t *testing.T) {
	databases := openTestDatabases(t)
	valid := CheckpointConfig{Interval: time.Second, WALThresholdBytes: 1, Timeout: time.Second}
	if _, err := NewCheckpointManager(nil, valid, func(CheckpointObservation) {}); err == nil {
		t.Fatal("nil databases were accepted")
	}
	if _, err := NewCheckpointManager(databases, CheckpointConfig{}, func(CheckpointObservation) {}); err == nil {
		t.Fatal("implicit checkpoint policy was accepted")
	}
	if _, err := NewCheckpointManager(databases, valid, nil); err == nil {
		t.Fatal("missing observer was accepted")
	}
}

func TestCheckpointManagerRunsOnlyAfterThresholdAndReportsResults(t *testing.T) {
	databases := openTestDatabases(t)
	writer, err := NewMetricBatchWriter(databases.Metrics, 1)
	if err != nil {
		t.Fatal(err)
	}
	if inserted, err := writer.AppendBatch(context.Background(), storageBatch("checkpoint-batch", time.Now().UTC(), 10)); err != nil || !inserted {
		t.Fatalf("append inserted=%t err=%v", inserted, err)
	}
	observations := make([]CheckpointObservation, 0)
	manager, err := NewCheckpointManager(databases, CheckpointConfig{
		Interval: time.Second, WALThresholdBytes: 1 << 60, Timeout: time.Second,
	}, func(observation CheckpointObservation) { observations = append(observations, observation) })
	if err != nil {
		t.Fatal(err)
	}
	manager.CheckIfNeeded(context.Background())
	if len(observations) != 0 {
		t.Fatalf("checkpoint ran below threshold: %+v", observations)
	}
	manager.config.WALThresholdBytes = 1
	manager.CheckIfNeeded(context.Background())
	if len(observations) == 0 {
		t.Fatal("checkpoint did not run after threshold")
	}
	foundMetrics := false
	for _, observation := range observations {
		if observation.Database == "metrics" {
			foundMetrics = true
			if observation.Reason != "threshold" || observation.WALBytes < 1 || observation.Err != nil || observation.Busy != 0 || observation.LogFrames < observation.CheckpointedFrames {
				t.Fatalf("metrics observation=%+v", observation)
			}
		}
	}
	if !foundMetrics {
		t.Fatalf("observations=%+v", observations)
	}
	count := len(observations)
	manager.CheckIfNeeded(context.Background())
	if len(observations) != count {
		t.Fatalf("unchanged WAL checkpointed again: before=%d after=%d", count, len(observations))
	}
	manager.CheckpointAll(context.Background())
	if len(observations) != count+2 || observations[len(observations)-1].Reason != "shutdown" {
		t.Fatalf("shutdown observations=%+v", observations[count:])
	}
}

func TestCheckpointManagerRunStopsWithContext(t *testing.T) {
	databases := openTestDatabases(t)
	manager, err := NewCheckpointManager(databases, CheckpointConfig{
		Interval: time.Millisecond, WALThresholdBytes: 1 << 60, Timeout: time.Second,
	}, func(CheckpointObservation) {})
	if err != nil {
		t.Fatal(err)
	}
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
		t.Fatal("checkpoint loop did not stop")
	}
}

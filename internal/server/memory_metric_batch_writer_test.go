/*
Package server tests bounded storage and atomic retry behavior of the
development-only metric batch writer.
*/
package server

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

func TestMemoryMetricBatchWriterDeduplicatesAndRejectsConflictingReuse(t *testing.T) {
	writer, err := NewMemoryMetricBatchWriter(2, 2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	batch := memoryWriterBatch("batch-a", time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC), 10)
	inserted, err := writer.AppendBatch(context.Background(), batch)
	if err != nil || !inserted {
		t.Fatalf("first append inserted=%t err=%v", inserted, err)
	}
	inserted, err = writer.AppendBatch(context.Background(), batch)
	if err != nil || inserted {
		t.Fatalf("identical retry inserted=%t err=%v", inserted, err)
	}
	conflicting := memoryWriterBatch(batch.ID, batch.Snapshots[0].Timestamp, 20)
	if inserted, err = writer.AppendBatch(context.Background(), conflicting); !errors.Is(err, metrics.ErrBatchConflict) || inserted {
		t.Fatalf("conflicting retry inserted=%t err=%v", inserted, err)
	}
	if len(writer.batches) != 1 || writer.snapshotCount != 1 {
		t.Fatalf("conflict changed stored state: batches=%d snapshots=%d", len(writer.batches), writer.snapshotCount)
	}
}

func TestMemoryMetricBatchWriterCapacityFailureIsAtomic(t *testing.T) {
	writer, err := NewMemoryMetricBatchWriter(1, 1, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	first := memoryWriterBatch("batch-a", time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC), 10)
	if inserted, err := writer.AppendBatch(context.Background(), first); err != nil || !inserted {
		t.Fatalf("first append inserted=%t err=%v", inserted, err)
	}
	second := memoryWriterBatch("batch-b", first.Snapshots[0].Timestamp.Add(time.Minute), 20)
	if inserted, err := writer.AppendBatch(context.Background(), second); !errors.Is(err, ErrMetricBatchMemoryFull) || inserted {
		t.Fatalf("full append inserted=%t err=%v", inserted, err)
	}
	if len(writer.batches) != 1 || writer.snapshotCount != 1 {
		t.Fatalf("capacity failure changed stored state: batches=%d snapshots=%d", len(writer.batches), writer.snapshotCount)
	}
}

func TestMemoryMetricBatchWriterRejectsInvalidLimitsAndCancelledRequests(t *testing.T) {
	for _, limits := range [][3]int{{0, 1, 1}, {1, 0, 1}, {1, 1, 0}} {
		if _, err := NewMemoryMetricBatchWriter(limits[0], limits[1], limits[2]); err == nil {
			t.Fatalf("limits %v were accepted", limits)
		}
	}
	writer, err := NewMemoryMetricBatchWriter(1, 1, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if inserted, err := writer.AppendBatch(ctx, memoryWriterBatch("batch-a", time.Now().UTC(), 10)); !errors.Is(err, context.Canceled) || inserted {
		t.Fatalf("cancelled append inserted=%t err=%v", inserted, err)
	}
	if len(writer.batches) != 0 {
		t.Fatalf("cancelled append stored %d batches", len(writer.batches))
	}
}

func TestMemoryMetricBatchWriterConcurrentRetryHasSingleInsert(t *testing.T) {
	writer, err := NewMemoryMetricBatchWriter(1, 1, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	batch := memoryWriterBatch("batch-concurrent", time.Now().UTC(), 10)
	var inserted atomic.Int32
	var failures atomic.Int32
	var group sync.WaitGroup
	for index := 0; index < 20; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			created, err := writer.AppendBatch(context.Background(), batch)
			if err != nil {
				failures.Add(1)
				return
			}
			if created {
				inserted.Add(1)
			}
		}()
	}
	group.Wait()
	if inserted.Load() != 1 || failures.Load() != 0 || len(writer.batches) != 1 || writer.snapshotCount != 1 {
		t.Fatalf("concurrent result inserts=%d failures=%d batches=%d snapshots=%d", inserted.Load(), failures.Load(), len(writer.batches), writer.snapshotCount)
	}
}

func TestMemoryMetricBatchWriterEnforcesEncodedByteLimit(t *testing.T) {
	probe, err := NewMemoryMetricBatchWriter(1, 1, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	batch := memoryWriterBatch("batch-a", time.Now().UTC(), 10)
	if _, err := probe.AppendBatch(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	encodedSize := probe.encodedBytes
	writer, err := NewMemoryMetricBatchWriter(1, 1, encodedSize-1)
	if err != nil {
		t.Fatal(err)
	}
	if inserted, err := writer.AppendBatch(context.Background(), batch); !errors.Is(err, ErrMetricBatchMemoryFull) || inserted {
		t.Fatalf("oversize append inserted=%t err=%v", inserted, err)
	}
	if len(writer.batches) != 0 || writer.encodedBytes != 0 || writer.snapshotCount != 0 {
		t.Fatalf("oversize append changed state: batches=%d bytes=%d snapshots=%d", len(writer.batches), writer.encodedBytes, writer.snapshotCount)
	}
}

func memoryWriterBatch(id string, timestamp time.Time, cpuUsage float64) metrics.Batch {
	return metrics.Batch{
		ID:        id,
		DeviceID:  "node-a",
		Snapshots: []metrics.MetricEnvelope{serverBatchEnvelope(timestamp, cpuUsage)},
	}
}

func ExampleNewMemoryMetricBatchWriter() {
	writer, err := NewMemoryMetricBatchWriter(8, 32, 1<<20)
	if err != nil {
		panic(err)
	}
	fmt.Println(writer != nil)
	// Output: true
}

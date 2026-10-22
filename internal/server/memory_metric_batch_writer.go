/*
Package server provides a bounded, in-memory implementation of the metric
batch writer contract for development and tests.

This writer is deliberately not a production persistence adapter. It rejects
new data when its explicit limits are exhausted rather than evicting IDs and
silently weakening retry deduplication.
*/
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

var ErrMetricBatchMemoryFull = errors.New("in-memory metric batch writer capacity exhausted")

// MemoryMetricBatchWriter stores retry keys and encoded batches within caller-supplied limits.
type MemoryMetricBatchWriter struct {
	mu              sync.Mutex
	maxSnapshots    int
	maxBatches      int
	maxEncodedBytes int
	encodedBytes    int
	snapshotCount   int
	batches         map[string][]byte
}

// NewMemoryMetricBatchWriter requires positive limits and is intended for tests or local development only.
func NewMemoryMetricBatchWriter(maxSnapshotsPerBatch, maxBatches, maxEncodedBytes int) (*MemoryMetricBatchWriter, error) {
	if maxSnapshotsPerBatch < 1 || maxBatches < 1 || maxEncodedBytes < 1 {
		return nil, errors.New("positive in-memory batch limits are required")
	}
	return &MemoryMetricBatchWriter{
		maxSnapshots:    maxSnapshotsPerBatch,
		maxBatches:      maxBatches,
		maxEncodedBytes: maxEncodedBytes,
		batches:         make(map[string][]byte),
	}, nil
}

// AppendBatch atomically records a batch or recognizes an identical retry.
func (w *MemoryMetricBatchWriter) AppendBatch(ctx context.Context, batch metrics.Batch) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := batch.Validate(w.maxSnapshots); err != nil {
		return false, fmt.Errorf("validate metric batch: %w", err)
	}
	encoded, err := json.Marshal(batch)
	if err != nil {
		return false, fmt.Errorf("encode metric batch: %w", err)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if previous, exists := w.batches[batch.ID]; exists {
		if !bytes.Equal(previous, encoded) {
			return false, metrics.ErrBatchConflict
		}
		return false, nil
	}
	if len(w.batches) >= w.maxBatches || len(encoded) > w.maxEncodedBytes-w.encodedBytes {
		return false, ErrMetricBatchMemoryFull
	}
	w.batches[batch.ID] = append([]byte(nil), encoded...)
	w.encodedBytes += len(encoded)
	w.snapshotCount += len(batch.Snapshots)
	return true, nil
}

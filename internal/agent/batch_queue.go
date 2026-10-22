/*
Package agent provides an explicitly bounded in-memory queue for metric batches.

The queue never schedules sampling or network requests. Callers choose when to
append, request a pending batch, retry it, and acknowledge a successful upload.
*/
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

var ErrBatchQueueFull = errors.New("metrics batch queue is full")

// BatchQueue retains validated snapshots in memory until their batch is acknowledged.
type BatchQueue struct {
	mu                 sync.Mutex
	sendMu             sync.Mutex
	deviceID           string
	maxQueuedSnapshots int
	maxQueuedBytes     int64
	maxBatchSnapshots  int
	maxBatchBodyBytes  int64
	snapshots          []metrics.MetricEnvelope
	snapshotBytes      []int64
	queuedBytes        int64
	pending            *metrics.Batch
}

// NewBatchQueue requires explicit count and byte memory bounds for one device.
func NewBatchQueue(deviceID string, maxQueuedSnapshots int, maxQueuedBytes int64, maxBatchSnapshots int, maxBatchBodyBytes int64) (*BatchQueue, error) {
	if strings.TrimSpace(deviceID) == "" || strings.TrimSpace(deviceID) != deviceID || maxQueuedSnapshots < 1 || maxQueuedBytes < 1 || maxBatchSnapshots < 1 || maxBatchBodyBytes < 1 {
		return nil, errors.New("device ID and positive queue count, queue byte, batch count, and batch body limits are required")
	}
	if maxBatchSnapshots > maxQueuedSnapshots {
		return nil, errors.New("batch snapshot limit cannot exceed queue capacity")
	}
	return &BatchQueue{
		deviceID: deviceID, maxQueuedSnapshots: maxQueuedSnapshots, maxQueuedBytes: maxQueuedBytes,
		maxBatchSnapshots: maxBatchSnapshots, maxBatchBodyBytes: maxBatchBodyBytes,
	}, nil
}

// Append adds one valid, ordered snapshot without exceeding the configured queue capacity.
func (q *BatchQueue) Append(snapshot metrics.MetricEnvelope) error {
	if err := snapshot.Validate(); err != nil {
		return fmt.Errorf("validate queued snapshot: %w", err)
	}
	if snapshot.Device.ID != q.deviceID {
		return errors.New("queued snapshot belongs to another device")
	}
	copyOfSnapshot, err := cloneEnvelope(snapshot)
	if err != nil {
		return fmt.Errorf("copy queued snapshot: %w", err)
	}
	encoded, err := json.Marshal(copyOfSnapshot)
	if err != nil {
		return fmt.Errorf("measure queued snapshot: %w", err)
	}
	snapshotBytes := int64(len(encoded))
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.snapshots) >= q.maxQueuedSnapshots || snapshotBytes > q.maxQueuedBytes-q.queuedBytes {
		return ErrBatchQueueFull
	}
	if count := len(q.snapshots); count > 0 && !copyOfSnapshot.Timestamp.After(q.snapshots[count-1].Timestamp) {
		return errors.New("queued snapshots must have strictly increasing timestamps")
	}
	q.snapshots = append(q.snapshots, copyOfSnapshot)
	q.snapshotBytes = append(q.snapshotBytes, snapshotBytes)
	q.queuedBytes += snapshotBytes
	return nil
}

// PendingBatch returns the same batch ID until Acknowledge accepts that batch.
func (q *BatchQueue) PendingBatch() (metrics.Batch, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pending != nil {
		batch, err := cloneBatch(*q.pending)
		return batch, true, err
	}
	if len(q.snapshots) == 0 {
		return metrics.Batch{}, false, nil
	}

	batchID, err := metrics.NewBatchID()
	if err != nil {
		return metrics.Batch{}, false, err
	}
	candidate := metrics.Batch{ID: batchID, DeviceID: q.deviceID}
	for _, snapshot := range q.snapshots[:min(len(q.snapshots), q.maxBatchSnapshots)] {
		candidate.Snapshots = append(candidate.Snapshots, snapshot)
		body, marshalErr := json.Marshal(candidate)
		if marshalErr != nil {
			return metrics.Batch{}, false, fmt.Errorf("encode pending metrics batch: %w", marshalErr)
		}
		if int64(len(body)) > q.maxBatchBodyBytes {
			candidate.Snapshots = candidate.Snapshots[:len(candidate.Snapshots)-1]
			if len(candidate.Snapshots) == 0 {
				return metrics.Batch{}, false, errors.New("one metrics snapshot exceeds configured batch body limit")
			}
			break
		}
	}
	q.pending = &candidate
	batch, err := cloneBatch(candidate)
	return batch, true, err
}

// Acknowledge removes the leading pending snapshots only when their batch ID matches.
func (q *BatchQueue) Acknowledge(batchID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pending == nil || q.pending.ID != batchID {
		return errors.New("acknowledgment does not match the pending metrics batch")
	}
	acknowledgedCount := len(q.pending.Snapshots)
	for _, snapshotBytes := range q.snapshotBytes[:acknowledgedCount] {
		q.queuedBytes -= snapshotBytes
	}
	q.snapshots = q.snapshots[acknowledgedCount:]
	q.snapshotBytes = q.snapshotBytes[acknowledgedCount:]
	q.pending = nil
	return nil
}

// SendPending sends one pending batch and acknowledges it only after a valid server response.
func (q *BatchQueue) SendPending(ctx context.Context, sender *Sender) (SendResult, bool, error) {
	if sender == nil {
		return SendResult{}, false, errors.New("metrics sender is required")
	}
	q.sendMu.Lock()
	defer q.sendMu.Unlock()
	batch, ok, err := q.PendingBatch()
	if err != nil || !ok {
		return SendResult{}, ok, err
	}
	result, err := sender.SendBatch(ctx, batch, q.maxBatchSnapshots, q.maxBatchBodyBytes)
	if err != nil {
		return SendResult{}, true, err
	}
	if err := q.Acknowledge(batch.ID); err != nil {
		return result, true, fmt.Errorf("acknowledge sent metrics batch: %w", err)
	}
	return result, true, nil
}

// Len reports the number of snapshots retained in memory, including the pending batch.
func (q *BatchQueue) Len() int {
	snapshots, _ := q.Usage()
	return snapshots
}

// Usage reports the retained snapshot count and their encoded JSON byte size.
func (q *BatchQueue) Usage() (int, int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.snapshots), q.queuedBytes
}

func cloneEnvelope(envelope metrics.MetricEnvelope) (metrics.MetricEnvelope, error) {
	data, err := json.Marshal(envelope)
	if err != nil {
		return metrics.MetricEnvelope{}, err
	}
	var cloned metrics.MetricEnvelope
	if err := json.Unmarshal(data, &cloned); err != nil {
		return metrics.MetricEnvelope{}, err
	}
	return cloned, nil
}

func cloneBatch(batch metrics.Batch) (metrics.Batch, error) {
	data, err := json.Marshal(batch)
	if err != nil {
		return metrics.Batch{}, err
	}
	var cloned metrics.Batch
	if err := json.Unmarshal(data, &cloned); err != nil {
		return metrics.Batch{}, err
	}
	return cloned, nil
}

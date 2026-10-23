/*
Package agent retains bounded plugin observations until an idempotent batch is acknowledged.

The queue owns no timer. Callers decide when collection and one send attempt occur.
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

var ErrPluginBatchQueueFull = errors.New("plugin metric batch queue is full")

// PluginBatchQueueLimits bounds retained work and each network request.
type PluginBatchQueueLimits struct {
	MaxQueuedObservations int
	MaxQueuedSamples      int
	MaxQueuedBytes        int64
	MaxBatchObservations  int
	MaxBatchSamples       int
	MaxBatchBodyBytes     int64
}

// PluginBatchQueue retains detached plugin observations for one device.
type PluginBatchQueue struct {
	mu, sendMu       sync.Mutex
	deviceID         string
	limits           PluginBatchQueueLimits
	observations     []metrics.PluginObservation
	observationBytes []int64
	queuedSamples    int
	queuedBytes      int64
	pending          *metrics.PluginBatch
	store            pluginBatchQueueStateStore
}

type pluginBatchQueueStateStore interface {
	Save(pluginBatchQueueDiskState) (bool, error)
	Close() error
}

// NewPluginBatchQueue creates an in-memory queue with explicit limits.
func NewPluginBatchQueue(deviceID string, limits PluginBatchQueueLimits) (*PluginBatchQueue, error) {
	return newPluginBatchQueue(deviceID, limits, nil, pluginBatchQueueDiskState{})
}

func newPluginBatchQueue(deviceID string, limits PluginBatchQueueLimits, store pluginBatchQueueStateStore, restored pluginBatchQueueDiskState) (*PluginBatchQueue, error) {
	if strings.TrimSpace(deviceID) == "" || strings.TrimSpace(deviceID) != deviceID || limits.MaxQueuedObservations < 1 || limits.MaxQueuedSamples < 1 || limits.MaxQueuedBytes < 1 || limits.MaxBatchObservations < 1 || limits.MaxBatchSamples < 1 || limits.MaxBatchBodyBytes < 1 {
		return nil, errors.New("device ID and positive plugin queue and batch limits are required")
	}
	if limits.MaxBatchObservations > limits.MaxQueuedObservations || limits.MaxBatchSamples > limits.MaxQueuedSamples {
		return nil, errors.New("plugin batch limits cannot exceed queue capacities")
	}
	queue := &PluginBatchQueue{deviceID: deviceID, limits: limits, store: store}
	if err := queue.restore(restored); err != nil {
		return nil, err
	}
	return queue, nil
}

// Append retains one validated observation after proving it can fit one batch.
func (q *PluginBatchQueue) Append(observation metrics.PluginObservation) error {
	if err := observation.Validate(); err != nil {
		return fmt.Errorf("validate queued plugin observation: %w", err)
	}
	if observation.DeviceID != q.deviceID {
		return errors.New("queued plugin observation belongs to another device")
	}
	cloned, encoded, err := clonePluginObservation(observation)
	if err != nil {
		return fmt.Errorf("copy queued plugin observation: %w", err)
	}
	samples := len(cloned.Samples)
	probe := metrics.PluginBatch{ID: strings.Repeat("0", 32), DeviceID: q.deviceID, Observations: []metrics.PluginObservation{cloned}}
	probeBody, err := json.Marshal(probe)
	if err != nil || samples > q.limits.MaxBatchSamples || int64(len(probeBody)) > q.limits.MaxBatchBodyBytes {
		return errors.New("one plugin observation exceeds configured batch limits")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.observations) >= q.limits.MaxQueuedObservations || samples > q.limits.MaxQueuedSamples-q.queuedSamples || int64(len(encoded)) > q.limits.MaxQueuedBytes-q.queuedBytes {
		return ErrPluginBatchQueueFull
	}
	next := append(q.observations, cloned)
	published, persistErr := q.saveState(next, q.pending)
	if persistErr != nil && !published {
		return fmt.Errorf("persist queued plugin observation: %w", persistErr)
	}
	q.observations = next
	q.observationBytes = append(q.observationBytes, int64(len(encoded)))
	q.queuedSamples += samples
	q.queuedBytes += int64(len(encoded))
	if persistErr != nil {
		return fmt.Errorf("persist queued plugin observation: %w", persistErr)
	}
	return nil
}

// PendingBatch returns the same batch ID until acknowledgment.
func (q *PluginBatchQueue) PendingBatch() (metrics.PluginBatch, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pending != nil {
		batch, err := clonePluginBatch(*q.pending)
		return batch, true, err
	}
	if len(q.observations) == 0 {
		return metrics.PluginBatch{}, false, nil
	}
	id, err := metrics.NewBatchID()
	if err != nil {
		return metrics.PluginBatch{}, false, err
	}
	candidate := metrics.PluginBatch{ID: id, DeviceID: q.deviceID}
	samples := 0
	for _, observation := range q.observations[:min(len(q.observations), q.limits.MaxBatchObservations)] {
		if len(observation.Samples) > q.limits.MaxBatchSamples-samples {
			break
		}
		candidate.Observations = append(candidate.Observations, observation)
		body, marshalErr := json.Marshal(candidate)
		if marshalErr != nil {
			return metrics.PluginBatch{}, false, fmt.Errorf("encode pending plugin batch: %w", marshalErr)
		}
		if int64(len(body)) > q.limits.MaxBatchBodyBytes {
			candidate.Observations = candidate.Observations[:len(candidate.Observations)-1]
			break
		}
		samples += len(observation.Samples)
	}
	if len(candidate.Observations) == 0 {
		return metrics.PluginBatch{}, false, errors.New("queued plugin observation cannot fit a batch")
	}
	published, persistErr := q.saveState(q.observations, &candidate)
	if persistErr != nil && !published {
		return metrics.PluginBatch{}, false, fmt.Errorf("persist pending plugin batch: %w", persistErr)
	}
	q.pending = &candidate
	if persistErr != nil {
		return metrics.PluginBatch{}, false, fmt.Errorf("persist pending plugin batch: %w", persistErr)
	}
	batch, err := clonePluginBatch(candidate)
	return batch, true, err
}

// Acknowledge removes only the observations in the matching pending batch.
func (q *PluginBatchQueue) Acknowledge(batchID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pending == nil || q.pending.ID != batchID {
		return errors.New("acknowledgment does not match the pending plugin batch")
	}
	count := len(q.pending.Observations)
	published, persistErr := q.saveState(q.observations[count:], nil)
	if persistErr != nil && !published {
		return fmt.Errorf("persist plugin batch acknowledgment: %w", persistErr)
	}
	for index, observation := range q.observations[:count] {
		q.queuedSamples -= len(observation.Samples)
		q.queuedBytes -= q.observationBytes[index]
	}
	q.observations = q.observations[count:]
	q.observationBytes = q.observationBytes[count:]
	q.pending = nil
	if persistErr != nil {
		return fmt.Errorf("persist plugin batch acknowledgment: %w", persistErr)
	}
	return nil
}

// SendPending performs one send and acknowledges only a complete response.
func (q *PluginBatchQueue) SendPending(ctx context.Context, sender *Sender) (PluginSendResult, bool, error) {
	if sender == nil {
		return PluginSendResult{}, false, errors.New("plugin metric sender is required")
	}
	q.sendMu.Lock()
	defer q.sendMu.Unlock()
	batch, ok, err := q.PendingBatch()
	if err != nil || !ok {
		return PluginSendResult{}, ok, err
	}
	result, err := sender.SendPluginBatch(ctx, batch, q.limits.MaxBatchObservations, q.limits.MaxBatchSamples, q.limits.MaxBatchBodyBytes)
	if err != nil {
		return PluginSendResult{}, true, err
	}
	if err := q.Acknowledge(batch.ID); err != nil {
		return result, true, fmt.Errorf("acknowledge sent plugin batch: %w", err)
	}
	return result, true, nil
}

// Usage returns retained observation, sample, and encoded byte counts.
func (q *PluginBatchQueue) Usage() (int, int, int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.observations), q.queuedSamples, q.queuedBytes
}

// Close releases the persistent queue lock when present.
func (q *PluginBatchQueue) Close() error {
	q.sendMu.Lock()
	defer q.sendMu.Unlock()
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.store == nil {
		return nil
	}
	err := q.store.Close()
	q.store = nil
	return err
}

func (q *PluginBatchQueue) saveState(observations []metrics.PluginObservation, pending *metrics.PluginBatch) (bool, error) {
	if q.store == nil {
		return true, nil
	}
	state := pluginBatchQueueDiskState{Version: pluginBatchQueueStateVersion, DeviceIDHash: batchQueueDeviceHash(q.deviceID), Observations: observations}
	if pending != nil {
		state.PendingBatchID = pending.ID
		state.PendingObservations = len(pending.Observations)
	}
	return q.store.Save(state)
}

func (q *PluginBatchQueue) restore(state pluginBatchQueueDiskState) error {
	if state.Version == 0 {
		return nil
	}
	if state.Version != pluginBatchQueueStateVersion || state.DeviceIDHash != batchQueueDeviceHash(q.deviceID) || len(state.Observations) > q.limits.MaxQueuedObservations {
		return errors.New("persisted plugin queue identity, version, or count is invalid")
	}
	for index, observation := range state.Observations {
		cloned, encoded, err := clonePluginObservation(observation)
		if err != nil || cloned.DeviceID != q.deviceID || len(cloned.Samples) > q.limits.MaxQueuedSamples-q.queuedSamples || int64(len(encoded)) > q.limits.MaxQueuedBytes-q.queuedBytes {
			return fmt.Errorf("persisted plugin queue observation %d is invalid or exceeds capacity", index)
		}
		probe, probeErr := json.Marshal(metrics.PluginBatch{ID: strings.Repeat("0", 32), DeviceID: q.deviceID, Observations: []metrics.PluginObservation{cloned}})
		if probeErr != nil || len(cloned.Samples) > q.limits.MaxBatchSamples || int64(len(probe)) > q.limits.MaxBatchBodyBytes {
			return fmt.Errorf("persisted plugin queue observation %d cannot fit one batch", index)
		}
		q.observations = append(q.observations, cloned)
		q.observationBytes = append(q.observationBytes, int64(len(encoded)))
		q.queuedSamples += len(cloned.Samples)
		q.queuedBytes += int64(len(encoded))
	}
	if state.PendingBatchID == "" && state.PendingObservations == 0 {
		return nil
	}
	if state.PendingBatchID == "" || state.PendingObservations < 1 || state.PendingObservations > len(q.observations) || state.PendingObservations > q.limits.MaxBatchObservations {
		return errors.New("persisted pending plugin batch is invalid")
	}
	pending := metrics.PluginBatch{ID: state.PendingBatchID, DeviceID: q.deviceID, Observations: append([]metrics.PluginObservation(nil), q.observations[:state.PendingObservations]...)}
	if err := pending.Validate(q.limits.MaxBatchObservations, q.limits.MaxBatchSamples); err != nil {
		return fmt.Errorf("validate persisted pending plugin batch: %w", err)
	}
	body, err := json.Marshal(pending)
	if err != nil || int64(len(body)) > q.limits.MaxBatchBodyBytes {
		return errors.New("persisted pending plugin batch exceeds its body limit")
	}
	q.pending = &pending
	return nil
}

func clonePluginObservation(observation metrics.PluginObservation) (metrics.PluginObservation, []byte, error) {
	data, err := json.Marshal(observation)
	if err != nil {
		return metrics.PluginObservation{}, nil, err
	}
	var cloned metrics.PluginObservation
	if err := json.Unmarshal(data, &cloned); err != nil {
		return metrics.PluginObservation{}, nil, err
	}
	return cloned, data, nil
}

func clonePluginBatch(batch metrics.PluginBatch) (metrics.PluginBatch, error) {
	data, err := json.Marshal(batch)
	if err != nil {
		return metrics.PluginBatch{}, err
	}
	var cloned metrics.PluginBatch
	if err := json.Unmarshal(data, &cloned); err != nil {
		return metrics.PluginBatch{}, err
	}
	return cloned, nil
}

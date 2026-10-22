/*
Package server implements current device state and refresh-job storage.

本文件负责保存每台设备最新快照、根据真实接收时间计算在线状态，并管理一次性磁盘
刷新任务。本文件不持久化历史、不处理 HTTP，也不采集指标。

数据链路：metrics Handler -> Store -> REST reads and SSE publication
*/
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/dashboard"
	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

// DeviceState pairs the latest snapshot with Server receipt time and liveness.
// DeviceState 将 Agent 快照、Server 接收时间和动态在线状态组合为 API 数据。
type DeviceState struct {
	// Snapshot is the latest valid payload received from the Agent.
	Snapshot metrics.MetricEnvelope `json:"snapshot"`

	// ReceivedAt is Server wall-clock time, not Agent sampling time.
	ReceivedAt time.Time `json:"received_at"`

	// Status is online, stale, or offline and is recalculated when read.
	Status string `json:"status"`
}

type storedSnapshot struct {
	snapshot   metrics.MetricEnvelope
	receivedAt time.Time
}

// RefreshJob tracks one user-authorized disk refresh request.
// RefreshJob 跟踪一次可能唤醒 HDD 的用户授权刷新任务。
type RefreshJob struct {
	// ID is an unpredictable correlation identifier returned to Dashboard.
	ID string `json:"request_id"`

	// DeviceID binds authorization to one stable Agent identity.
	DeviceID string `json:"device_id"`

	// Status is queued, delivered, completed, failed, or expired.
	Status string `json:"status"`

	// CreatedAt and UpdatedAt use Server time for lifecycle decisions.
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Error is populated only when an Agent reports refresh failure.
	Error string `json:"error,omitempty"`
}

// Store holds current device snapshots and refresh jobs in memory.
// Store 使用锁保护共享 map；Server 重启后其中的数据不会保留。
type Store struct {
	mu                 sync.RWMutex
	snapshots          map[string]storedSnapshot
	pendingDiskRefresh map[string]string
	refreshJobs        map[string]RefreshJob
	now                func() time.Time
}

// NewStore creates an initialized in-memory Store.
func NewStore() *Store {
	return &Store{snapshots: make(map[string]storedSnapshot), pendingDiskRefresh: make(map[string]string), refreshJobs: make(map[string]RefreshJob), now: time.Now}
}

// Update replaces one device's latest snapshot using Server receipt time.
func (s *Store) Update(snapshot metrics.MetricEnvelope) DeviceState {
	now := s.now()
	s.mu.Lock()
	state := s.updateLocked(snapshot, now)
	s.mu.Unlock()
	return state
}

// UpdateIfNewer replaces current state only when the sample timestamp advances.
func (s *Store) UpdateIfNewer(snapshot metrics.MetricEnvelope) (DeviceState, bool) {
	now := s.now()
	s.mu.Lock()
	if current, exists := s.snapshots[snapshot.Device.ID]; exists && !snapshot.Timestamp.After(current.snapshot.Timestamp) {
		s.mu.Unlock()
		return stateAt(current.snapshot, current.receivedAt, now), false
	}
	state := s.updateLocked(snapshot, now)
	s.mu.Unlock()
	return state, true
}

func (s *Store) updateLocked(snapshot metrics.MetricEnvelope, now time.Time) DeviceState {
	s.snapshots[snapshot.Device.ID] = storedSnapshot{snapshot, now}
	if snapshot.RefreshRequestID != "" {
		if job, ok := s.refreshJobs[snapshot.RefreshRequestID]; ok && job.DeviceID == snapshot.Device.ID && job.Status == "delivered" {
			job.Status = "completed"
			if snapshot.RefreshError != "" {
				job.Status, job.Error = "failed", snapshot.RefreshError
			}
			job.UpdatedAt = now
			s.refreshJobs[job.ID] = job
		}
	}
	return stateAt(snapshot, now, now)
}

// RequestDiskRefresh queues a single refresh unless the device is unknown.
func (s *Store) RequestDiskRefresh(deviceID string) (RefreshJob, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.snapshots[deviceID]; !ok {
		return RefreshJob{}, false, nil
	}
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return RefreshJob{}, false, err
	}
	now := s.now()
	job := RefreshJob{ID: hex.EncodeToString(bytes), DeviceID: deviceID, Status: "queued", CreatedAt: now, UpdatedAt: now}
	s.pendingDiskRefresh[deviceID] = job.ID
	s.refreshJobs[job.ID] = job
	return job, true, nil
}

// TakeDiskRefresh atomically delivers and removes a device's pending request.
func (s *Store) TakeDiskRefresh(deviceID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	requestID := s.pendingDiskRefresh[deviceID]
	delete(s.pendingDiskRefresh, deviceID)
	if job, ok := s.refreshJobs[requestID]; ok {
		job.Status, job.UpdatedAt = "delivered", s.now()
		s.refreshJobs[requestID] = job
	}
	return requestID
}

// GetRefresh returns a refresh job and expires overdue unfinished work.
func (s *Store) GetRefresh(requestID string) (RefreshJob, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.refreshJobs[requestID]
	if !ok {
		return RefreshJob{}, false
	}
	if job.Status != "completed" && s.now().Sub(job.CreatedAt) > 30*time.Minute {
		job.Status, job.UpdatedAt = "expired", s.now()
		s.refreshJobs[requestID] = job
	}
	return job, true
}

// Get returns one device with liveness calculated at read time.
func (s *Store) Get(deviceID string) (DeviceState, bool) {
	s.mu.RLock()
	stored, ok := s.snapshots[deviceID]
	s.mu.RUnlock()
	if !ok {
		return DeviceState{}, false
	}
	return stateAt(stored.snapshot, stored.receivedAt, s.now()), true
}

// Current returns the latest snapshot projection required by the Dashboard query boundary.
func (s *Store) Current(_ context.Context, deviceID string) (dashboard.Snapshot, bool, error) {
	state, ok := s.Get(deviceID)
	if !ok {
		return dashboard.Snapshot{}, false, nil
	}
	return dashboard.Snapshot{Envelope: state.Snapshot, ReceivedAt: state.ReceivedAt, Status: state.Status}, true, nil
}

// List returns all devices sorted by stable device ID with current liveness.
func (s *Store) List() []DeviceState {
	s.mu.RLock()
	items := make([]storedSnapshot, 0, len(s.snapshots))
	for _, item := range s.snapshots {
		items = append(items, item)
	}
	s.mu.RUnlock()
	now := s.now()
	states := make([]DeviceState, 0, len(items))
	for _, item := range items {
		states = append(states, stateAt(item.snapshot, item.receivedAt, now))
	}
	sort.Slice(states, func(i, j int) bool { return states[i].Snapshot.Device.ID < states[j].Snapshot.Device.ID })
	return states
}

func stateAt(snapshot metrics.MetricEnvelope, receivedAt, now time.Time) DeviceState {
	age, status := now.Sub(receivedAt), "online"
	interval := time.Duration(snapshot.SamplingIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if age > 12*interval {
		status = "offline"
	} else if age > 3*interval {
		status = "stale"
	}
	return DeviceState{snapshot, receivedAt, status}
}

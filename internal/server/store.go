package server

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

type DeviceState struct {
	Snapshot   metrics.MetricEnvelope `json:"snapshot"`
	ReceivedAt time.Time              `json:"received_at"`
	Status     string                 `json:"status"`
}

type storedSnapshot struct {
	snapshot   metrics.MetricEnvelope
	receivedAt time.Time
}

type RefreshJob struct {
	ID        string    `json:"request_id"`
	DeviceID  string    `json:"device_id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Error     string    `json:"error,omitempty"`
}

type Store struct {
	mu                 sync.RWMutex
	snapshots          map[string]storedSnapshot
	pendingDiskRefresh map[string]string
	refreshJobs        map[string]RefreshJob
	now                func() time.Time
}

func NewStore() *Store {
	return &Store{snapshots: make(map[string]storedSnapshot), pendingDiskRefresh: make(map[string]string), refreshJobs: make(map[string]RefreshJob), now: time.Now}
}

func (s *Store) Update(snapshot metrics.MetricEnvelope) DeviceState {
	now := s.now()
	s.mu.Lock()
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
	s.mu.Unlock()
	return stateAt(snapshot, now, now)
}

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

func (s *Store) Get(deviceID string) (DeviceState, bool) {
	s.mu.RLock()
	stored, ok := s.snapshots[deviceID]
	s.mu.RUnlock()
	if !ok {
		return DeviceState{}, false
	}
	return stateAt(stored.snapshot, stored.receivedAt, s.now()), true
}

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

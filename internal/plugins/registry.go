/*
Package plugins keeps the current in-memory desired and reported plugin state.

The registry is a domain boundary for future Server control-plane integration;
it does not install artifacts or claim durable state across process restarts.
*/
package plugins

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/DingZhizCA/homelab-monitor/internal/audit"
)

const maxRevision = uint64(1<<63 - 1)

// DesiredState is the Server-authored target for one device and plugin.
type DesiredState struct {
	DeviceID string            `json:"device_id"`
	PluginID string            `json:"plugin_id"`
	Revision uint64            `json:"revision"`
	Version  string            `json:"version,omitempty"`
	Artifact ArtifactReference `json:"artifact"`
	Enabled  bool              `json:"enabled"`
}

// ArtifactReference identifies one immutable package object to be pulled by an Agent.
type ArtifactReference struct {
	Platform Platform `json:"platform"`
	SHA256   string   `json:"sha256"`
}

// Key binds a desired plugin version to its immutable package identity.
func (reference ArtifactReference) Key(pluginID, version string) ArtifactKey {
	return ArtifactKey{PluginID: pluginID, Version: version, Platform: reference.Platform, SHA256: reference.SHA256}
}

// Validate checks that an enabled target names an immutable, platform-specific package.
func (state DesiredState) Validate() error {
	if strings.TrimSpace(state.DeviceID) == "" || strings.TrimSpace(state.DeviceID) != state.DeviceID || !identifierPattern.MatchString(state.PluginID) || state.Revision == 0 || state.Revision > maxRevision {
		return errors.New("trimmed device_id, valid plugin_id, and revision between 1 and 2^63-1 are required")
	}
	if !state.Enabled {
		return nil
	}
	if !versionPattern.MatchString(state.Version) {
		return errors.New("enabled desired state requires a valid version")
	}
	if !identifierPattern.MatchString(state.Artifact.Platform.OS) || !identifierPattern.MatchString(state.Artifact.Platform.Arch) || !sha256Pattern.MatchString(state.Artifact.SHA256) {
		return errors.New("enabled desired state requires a valid artifact platform and SHA-256 digest")
	}
	return nil
}

// ReportedState records what an Agent has inspected and successfully applied.
type ReportedState struct {
	DeviceID      string         `json:"device_id"`
	PluginID      string         `json:"plugin_id"`
	ObservedRev   uint64         `json:"observed_revision"`
	AppliedRev    uint64         `json:"applied_revision"`
	Lifecycle     LifecycleState `json:"lifecycle_state"`
	RefusalReason string         `json:"refusal_reason,omitempty"`
}

type registryKey struct{ deviceID, pluginID string }

// ErrStateConflict identifies stale or conflicting plugin state revisions.
var ErrStateConflict = errors.New("plugin state revision conflicts with current state")

const (
	// DesiredStateAuditAction identifies an Owner publication of plugin intent.
	DesiredStateAuditAction = "plugin.desired.update"
	// DesiredStateAuditResourceType groups plugin intent changes in the audit stream.
	DesiredStateAuditResourceType = "plugin_desired_state"
)

// DesiredStateAuditResourceID returns the stable audit target for one device/plugin pair.
func DesiredStateAuditResourceID(state DesiredState) string {
	return state.DeviceID + "/" + state.PluginID
}

// Registry stores desired and reported state separately to preserve reconciliation semantics.
type Registry struct {
	mu       sync.RWMutex
	desired  map[registryKey]DesiredState
	reported map[registryKey]ReportedState
	audits   []audit.Event
}

// NewRegistry returns an empty, concurrency-safe in-memory registry.
func NewRegistry() *Registry {
	return &Registry{desired: make(map[registryKey]DesiredState), reported: make(map[registryKey]ReportedState)}
}

// SetDesired accepts a newer revision, permits an identical retry, and rejects stale/conflicting input.
func (r *Registry) SetDesired(state DesiredState) error {
	if err := state.Validate(); err != nil {
		return err
	}
	k := registryKey{state.DeviceID, state.PluginID}
	r.mu.Lock()
	defer r.mu.Unlock()
	current, ok := r.desired[k]
	if ok && state.Revision < current.Revision {
		return fmt.Errorf("%w: desired revision %d is older than current revision %d", ErrStateConflict, state.Revision, current.Revision)
	}
	if ok && state.Revision == current.Revision && state != current {
		return fmt.Errorf("%w: desired revision %d conflicts with current state", ErrStateConflict, state.Revision)
	}
	r.desired[k] = state
	return nil
}

// SetDesiredWithAudit atomically records a human-authored revision and its audit event.
func (r *Registry) SetDesiredWithAudit(state DesiredState, event audit.Event) error {
	if err := state.Validate(); err != nil {
		return err
	}
	if err := event.Validate(); err != nil {
		return fmt.Errorf("validate desired-state audit event: %w", err)
	}
	if event.Action != DesiredStateAuditAction || event.ResourceType != DesiredStateAuditResourceType || event.ResourceID != DesiredStateAuditResourceID(state) {
		return errors.New("desired-state audit target does not match plugin state")
	}
	key := registryKey{state.DeviceID, state.PluginID}
	r.mu.Lock()
	defer r.mu.Unlock()
	current, exists := r.desired[key]
	if exists && state.Revision < current.Revision {
		return fmt.Errorf("%w: desired revision %d is older than current revision %d", ErrStateConflict, state.Revision, current.Revision)
	}
	if exists && state.Revision == current.Revision {
		if state != current {
			return fmt.Errorf("%w: desired revision %d conflicts with current state", ErrStateConflict, state.Revision)
		}
		return nil
	}
	r.desired[key] = state
	r.audits = append(r.audits, event)
	return nil
}

// SetDesiredWithAuditContext exposes the storage-neutral context-aware writer boundary.
func (r *Registry) SetDesiredWithAuditContext(_ context.Context, state DesiredState, event audit.Event) error {
	return r.SetDesiredWithAudit(state, event)
}

// AuditEvents returns a copy of development-only in-memory management events.
func (r *Registry) AuditEvents() []audit.Event {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]audit.Event(nil), r.audits...)
}

// Desired returns the current target for a device/plugin pair.
func (r *Registry) Desired(deviceID, pluginID string) (DesiredState, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	state, ok := r.desired[registryKey{deviceID, pluginID}]
	return state, ok
}

// DesiredForDevice returns a stable snapshot suitable for a batched poll response.
func (r *Registry) DesiredForDevice(deviceID string) []DesiredState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	states := make([]DesiredState, 0)
	for key, state := range r.desired {
		if key.deviceID == deviceID {
			states = append(states, state)
		}
	}
	sort.Slice(states, func(i, j int) bool { return states[i].PluginID < states[j].PluginID })
	return states
}

// DesiredForDeviceContext exposes the storage-neutral context-aware reader boundary.
func (r *Registry) DesiredForDeviceContext(_ context.Context, deviceID string) ([]DesiredState, error) {
	return r.DesiredForDevice(deviceID), nil
}

// SetReported records the Agent's observed and applied revisions independently.
func (r *Registry) SetReported(state ReportedState) error {
	return r.SetReportedBatch([]ReportedState{state})
}

// SetReportedBatch validates every update before committing any of them.
func (r *Registry) SetReportedBatch(states []ReportedState) error {
	if err := ValidateReportedBatch(states); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, state := range states {
		key := registryKey{state.DeviceID, state.PluginID}
		if current, ok := r.reported[key]; ok && state.ObservedRev < current.ObservedRev {
			return fmt.Errorf("%w: observed revision %d is older than current revision %d", ErrStateConflict, state.ObservedRev, current.ObservedRev)
		}
	}
	for _, state := range states {
		key := registryKey{state.DeviceID, state.PluginID}
		r.reported[key] = state
	}
	return nil
}

// ValidateReportedBatch checks identity, lifecycle, revision, and duplicate constraints.
func ValidateReportedBatch(states []ReportedState) error {
	seen := make(map[registryKey]struct{}, len(states))
	for _, state := range states {
		if strings.TrimSpace(state.DeviceID) == "" || strings.TrimSpace(state.DeviceID) != state.DeviceID || !identifierPattern.MatchString(state.PluginID) || !ValidLifecycleState(state.Lifecycle) {
			return errors.New("trimmed device_id, valid plugin_id, and valid lifecycle_state are required")
		}
		if state.ObservedRev > maxRevision || state.AppliedRev > state.ObservedRev {
			return errors.New("reported revisions must fit signed 64-bit storage and applied revision cannot exceed observed revision")
		}
		key := registryKey{state.DeviceID, state.PluginID}
		if _, ok := seen[key]; ok {
			return fmt.Errorf("reported batch contains duplicate plugin %q for device %q", state.PluginID, state.DeviceID)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// SetReportedBatchContext exposes the storage-neutral context-aware writer boundary.
func (r *Registry) SetReportedBatchContext(_ context.Context, states []ReportedState) error {
	return r.SetReportedBatch(states)
}

// Reported returns the latest local report for a device/plugin pair.
func (r *Registry) Reported(deviceID, pluginID string) (ReportedState, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	state, ok := r.reported[registryKey{deviceID, pluginID}]
	return state, ok
}

// ReportedForDevice returns a stable snapshot for a batched Agent status report.
func (r *Registry) ReportedForDevice(deviceID string) []ReportedState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	states := make([]ReportedState, 0)
	for key, state := range r.reported {
		if key.deviceID == deviceID {
			states = append(states, state)
		}
	}
	sort.Slice(states, func(i, j int) bool { return states[i].PluginID < states[j].PluginID })
	return states
}

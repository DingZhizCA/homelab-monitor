/*
Package plugins defines the Server-authored collection policy that is kept
separate from plugin package lifecycle state.
*/
package plugins

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/audit"
)

// MaxCollectionConfigBytes is the wire safety bound for one collector config.
const MaxCollectionConfigBytes = 64 << 10

// CollectionPolicy is one independently revisioned collector schedule.
type CollectionPolicy struct {
	DeviceID            string          `json:"device_id"`
	PluginID            string          `json:"plugin_id"`
	CollectorID         string          `json:"collector_id"`
	Revision            uint64          `json:"revision"`
	PluginRevision      uint64          `json:"plugin_revision,omitempty"`
	Enabled             bool            `json:"enabled"`
	IntervalSeconds     uint64          `json:"interval_seconds,omitempty"`
	TimeoutMilliseconds uint64          `json:"timeout_milliseconds,omitempty"`
	Config              json.RawMessage `json:"config,omitempty"`
}

// Validate rejects implicit cadence, unbounded config, and invalid identities.
func (p CollectionPolicy) Validate() error {
	if strings.TrimSpace(p.DeviceID) == "" || strings.TrimSpace(p.DeviceID) != p.DeviceID || !identifierPattern.MatchString(p.PluginID) || !identifierPattern.MatchString(p.CollectorID) || p.Revision == 0 || p.Revision > maxRevision {
		return errors.New("trimmed device_id, valid plugin_id and collector_id, and revision between 1 and 2^63-1 are required")
	}
	if !p.Enabled {
		if p.PluginRevision != 0 || p.IntervalSeconds != 0 || p.TimeoutMilliseconds != 0 || len(p.Config) != 0 {
			return errors.New("disabled collection policy must not retain runtime settings")
		}
		return nil
	}
	if p.PluginRevision == 0 || p.PluginRevision > maxRevision || p.IntervalSeconds == 0 || p.IntervalSeconds > uint64((time.Duration(1<<63-1))/time.Second) || p.TimeoutMilliseconds == 0 || p.TimeoutMilliseconds > p.IntervalSeconds*1000 {
		return errors.New("enabled collection policy requires a plugin revision, positive interval, and timeout no longer than the interval")
	}
	if len(p.Config) == 0 || len(p.Config) > MaxCollectionConfigBytes || !json.Valid(p.Config) {
		return fmt.Errorf("enabled collection policy config must be valid JSON within %d bytes", MaxCollectionConfigBytes)
	}
	return nil
}

// EqualCollectionPolicy compares raw configuration bytes as part of revision identity.
func EqualCollectionPolicy(left, right CollectionPolicy) bool {
	return left.DeviceID == right.DeviceID && left.PluginID == right.PluginID && left.CollectorID == right.CollectorID &&
		left.Revision == right.Revision && left.PluginRevision == right.PluginRevision && left.Enabled == right.Enabled && left.IntervalSeconds == right.IntervalSeconds &&
		left.TimeoutMilliseconds == right.TimeoutMilliseconds && bytes.Equal(left.Config, right.Config)
}

const (
	CollectionPolicyAuditAction       = "plugin.collection_policy.update"
	CollectionPolicyAuditResourceType = "plugin_collection_policy"
)

// ErrCollectionTargetUnavailable means the bound plugin revision is not enabled.
var ErrCollectionTargetUnavailable = errors.New("collection policy plugin revision is unavailable")

// ErrCollectionPolicyCapacity means the configured per-device policy bound is full.
var ErrCollectionPolicyCapacity = errors.New("collection policy capacity is exhausted")

func CollectionPolicyAuditResourceID(policy CollectionPolicy) string {
	return policy.DeviceID + "/" + policy.PluginID + "/" + policy.CollectorID
}

func ValidateCollectionPolicyMutation(policy CollectionPolicy, event audit.Event) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	if err := event.Validate(); err != nil {
		return fmt.Errorf("validate collection-policy audit event: %w", err)
	}
	if event.Action != CollectionPolicyAuditAction || event.ResourceType != CollectionPolicyAuditResourceType || event.ResourceID != CollectionPolicyAuditResourceID(policy) {
		return errors.New("collection-policy audit target does not match policy")
	}
	return nil
}

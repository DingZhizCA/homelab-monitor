/*
Package metrics defines the storage-independent envelope for collector plugin observations.

This model is separate from MetricEnvelope v1 so plugin measurements can gain
their own ingestion path without changing the established Agent snapshot protocol.
*/
package metrics

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	pluginMetricIdentifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
	pluginMetricVersion    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.+_-]{0,127}$`)
)

// PluginObservation is one collector result tied to its package and device identity.
type PluginObservation struct {
	SchemaVersion int                       `json:"schema_version"`
	DeviceID      string                    `json:"device_id"`
	PluginID      string                    `json:"plugin_id"`
	PluginVersion string                    `json:"plugin_version"`
	CollectorID   string                    `json:"collector_id"`
	ObservedAt    time.Time                 `json:"observed_at"`
	Samples       []PluginMetricObservation `json:"samples"`
}

// PluginMetricObservation carries one typed value and its declared dimensions.
type PluginMetricObservation struct {
	MetricID string            `json:"metric_id"`
	Type     string            `json:"type"`
	Unit     string            `json:"unit"`
	Value    json.RawMessage   `json:"value"`
	Quality  string            `json:"quality"`
	Labels   map[string]string `json:"labels,omitempty"`
}

// Validate checks the transport-neutral shape without deciding storage or business policy.
func (o PluginObservation) Validate() error {
	if o.SchemaVersion != 1 {
		return errors.New("plugin observation schema_version must be 1")
	}
	identities := []struct{ field, value string }{
		{"device_id", o.DeviceID}, {"plugin_id", o.PluginID},
		{"plugin_version", o.PluginVersion}, {"collector_id", o.CollectorID},
	}
	for _, identity := range identities {
		field, value := identity.field, identity.value
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return errors.New("plugin observation " + field + " is required and must be trimmed")
		}
		if (field == "plugin_version" && !pluginMetricVersion.MatchString(value)) ||
			(field != "device_id" && field != "plugin_version" && !pluginMetricIdentifier.MatchString(value)) {
			return errors.New("plugin observation " + field + " must be a stable identifier")
		}
	}
	if o.ObservedAt.IsZero() {
		return errors.New("plugin observation observed_at is required")
	}
	if o.Samples == nil {
		return errors.New("plugin observation samples must be present")
	}
	for i, sample := range o.Samples {
		if !pluginMetricIdentifier.MatchString(sample.MetricID) || strings.TrimSpace(sample.Unit) == "" {
			return errors.New("plugin observation sample identity and unit are required")
		}
		if sample.Type != "counter" && sample.Type != "gauge" && sample.Type != "state" && sample.Type != "event" {
			return errors.New("plugin observation sample type is unsupported")
		}
		if len(sample.Value) == 0 || !utf8.Valid(sample.Value) || !json.Valid(sample.Value) {
			return errors.New("plugin observation sample value must be valid JSON")
		}
		switch sample.Quality {
		case "valid", "partial", "estimated", "missing", "reset", "stale":
		default:
			return errors.New("plugin observation sample quality is unsupported")
		}
		for key, value := range sample.Labels {
			if !pluginMetricIdentifier.MatchString(key) || !utf8.ValidString(value) {
				return errors.New("plugin observation sample labels must have stable keys and UTF-8 values")
			}
		}
		for j := 0; j < i; j++ {
			if o.Samples[j].MetricID == sample.MetricID && samePluginLabels(o.Samples[j].Labels, sample.Labels) {
				return errors.New("plugin observation contains duplicate metric and labels")
			}
		}
	}
	return nil
}

func samePluginLabels(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		other, exists := b[key]
		if !exists || other != value {
			return false
		}
	}
	return true
}

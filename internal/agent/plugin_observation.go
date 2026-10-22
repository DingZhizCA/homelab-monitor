/*
Package agent adapts validated plugin RPC results into the shared metric model.

The adapter preserves the collector's observed time, raw JSON values, quality,
and dimensions; it does not enqueue, transmit, store, or schedule observations.
*/
package agent

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
	"github.com/DingZhizCA/homelab-monitor/internal/plugins"
)

// NormalizePluginObservation validates and detaches a plugin result from RPC-owned memory.
func NormalizePluginObservation(deviceID, pluginID, pluginVersion string, descriptor plugins.CollectorDescriptor, result plugins.CollectResult) (metrics.PluginObservation, error) {
	if deviceID == "" || pluginID == "" || pluginVersion == "" {
		return metrics.PluginObservation{}, errors.New("device, plugin, and version identities are required")
	}
	if err := result.Validate(descriptor); err != nil {
		return metrics.PluginObservation{}, fmt.Errorf("validate plugin collection result: %w", err)
	}
	descriptors := make(map[string]plugins.MetricDescriptor, len(descriptor.Metrics))
	for _, metric := range descriptor.Metrics {
		descriptors[metric.ID] = metric
	}
	observation := metrics.PluginObservation{
		SchemaVersion: 1, DeviceID: deviceID, PluginID: pluginID,
		PluginVersion: pluginVersion, CollectorID: descriptor.ID,
		ObservedAt: result.ObservedAt, Samples: make([]metrics.PluginMetricObservation, 0, len(result.Samples)),
	}
	for _, sample := range result.Samples {
		descriptor := descriptors[sample.MetricID]
		labels := make(map[string]string, len(sample.Labels))
		for key, value := range sample.Labels {
			labels[key] = value
		}
		observation.Samples = append(observation.Samples, metrics.PluginMetricObservation{
			MetricID: sample.MetricID, Type: string(descriptor.Type), Unit: descriptor.Unit,
			Value: append(json.RawMessage(nil), sample.Value...), Quality: string(sample.Quality), Labels: labels,
		})
	}
	if err := observation.Validate(); err != nil {
		return metrics.PluginObservation{}, fmt.Errorf("validate normalized plugin observation: %w", err)
	}
	return observation, nil
}

// NormalizePluginOutcome converts one successful scheduler outcome using its captured metadata.
func NormalizePluginOutcome(deviceID string, outcome plugins.CollectionOutcome) (metrics.PluginObservation, error) {
	if outcome.Err != nil {
		return metrics.PluginObservation{}, fmt.Errorf("plugin collection failed: %w", outcome.Err)
	}
	if outcome.Descriptor.ID == "" || outcome.Descriptor.ID != outcome.CollectorID {
		return metrics.PluginObservation{}, errors.New("scheduler outcome does not contain the matching collector descriptor")
	}
	return NormalizePluginObservation(deviceID, outcome.PluginID, outcome.PluginVersion, outcome.Descriptor, outcome.Result)
}

/*
Package metrics defines the bounded, retryable transport for plugin observations.

Plugin batches remain separate from the built-in MetricEnvelope protocol so
collector schemas can evolve without changing the established Agent contract.
*/
package metrics

import (
	"errors"
	"fmt"
	"strings"
)

// PluginBatch groups one device's plugin observations under a retryable ID.
type PluginBatch struct {
	ID           string              `json:"batch_id"`
	DeviceID     string              `json:"device_id"`
	Observations []PluginObservation `json:"observations"`
}

// Validate enforces explicit observation and total sample budgets.
func (b PluginBatch) Validate(maxObservations, maxSamples int) error {
	if !batchIDPattern.MatchString(b.ID) || strings.TrimSpace(b.DeviceID) == "" || strings.TrimSpace(b.DeviceID) != b.DeviceID {
		return errors.New("batch ID and device ID are required")
	}
	if maxObservations < 1 || len(b.Observations) == 0 || len(b.Observations) > maxObservations {
		return fmt.Errorf("plugin batch must contain between 1 and %d observations", maxObservations)
	}
	if maxSamples < 1 {
		return errors.New("positive plugin sample limit is required")
	}
	totalSamples := 0
	for index, observation := range b.Observations {
		if observation.DeviceID != b.DeviceID {
			return fmt.Errorf("plugin observation %d belongs to a different device", index)
		}
		if err := observation.Validate(); err != nil {
			return fmt.Errorf("plugin observation %d is invalid: %w", index, err)
		}
		if len(observation.Samples) > maxSamples-totalSamples {
			return fmt.Errorf("plugin batch exceeds the %d sample limit", maxSamples)
		}
		totalSamples += len(observation.Samples)
	}
	return nil
}

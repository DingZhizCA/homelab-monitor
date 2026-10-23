/*
Package metrics tests plugin batch identity, device binding, and work limits.
*/
package metrics

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPluginBatchValidateBoundsAndBindsDevice(t *testing.T) {
	valid := PluginBatch{ID: "plugin-batch-1", DeviceID: "node-a", Observations: []PluginObservation{pluginBatchObservation("node-a")}}
	if err := valid.Validate(1, 1); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		batch PluginBatch
		obs   int
		samp  int
		match string
	}{
		{name: "empty", batch: PluginBatch{ID: "batch", DeviceID: "node-a"}, obs: 1, samp: 1, match: "between 1"},
		{name: "device", batch: PluginBatch{ID: "batch", DeviceID: "node-b", Observations: valid.Observations}, obs: 1, samp: 1, match: "different device"},
		{name: "observations", batch: PluginBatch{ID: "batch", DeviceID: "node-a", Observations: append(valid.Observations, valid.Observations[0])}, obs: 1, samp: 2, match: "between 1"},
		{name: "samples", batch: valid, obs: 1, samp: 0, match: "positive plugin sample"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.batch.Validate(test.obs, test.samp); err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func pluginBatchObservation(deviceID string) PluginObservation {
	return PluginObservation{
		SchemaVersion: 1, DeviceID: deviceID, PluginID: "smart", PluginVersion: "1.0.0", CollectorID: "temperature",
		ObservedAt: time.Date(2026, 10, 23, 1, 2, 3, 4, time.UTC),
		Samples:    []PluginMetricObservation{{MetricID: "temperature", Type: "gauge", Unit: "celsius", Value: json.RawMessage(`42.5`), Quality: "valid"}},
	}
}

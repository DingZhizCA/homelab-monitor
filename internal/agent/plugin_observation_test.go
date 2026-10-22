/*
Package agent tests conversion of plugin RPC results into detached metric observations.
*/
package agent

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/plugins"
)

func TestNormalizePluginObservationPreservesIdentityAndDetachesData(t *testing.T) {
	descriptor := plugins.CollectorDescriptor{ID: "cpu", Metrics: []plugins.MetricDescriptor{{ID: "usage", Type: plugins.MetricGauge, Unit: "percent", LabelKeys: []string{"core"}}}}
	result := plugins.CollectResult{ObservedAt: time.Unix(123, 0).UTC(), Samples: []plugins.MetricSample{{MetricID: "usage", Value: json.RawMessage(`42.5`), Quality: plugins.QualityValid, Labels: map[string]string{"core": "0"}}}}
	got, err := NormalizePluginObservation("device-a", "system", "1.2.3", descriptor, result)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeviceID != "device-a" || got.PluginID != "system" || got.PluginVersion != "1.2.3" || got.CollectorID != "cpu" || !got.ObservedAt.Equal(result.ObservedAt) {
		t.Fatalf("identity/time not preserved: %+v", got)
	}
	result.Samples[0].Value[0] = '9'
	result.Samples[0].Labels["core"] = "1"
	if string(got.Samples[0].Value) != "42.5" || got.Samples[0].Labels["core"] != "0" {
		t.Fatalf("normalized result aliases RPC input: %+v", got.Samples[0])
	}
}

func TestNormalizePluginObservationRejectsInvalidResult(t *testing.T) {
	descriptor := plugins.CollectorDescriptor{ID: "cpu", Metrics: []plugins.MetricDescriptor{{ID: "usage", Type: plugins.MetricGauge, Unit: "percent"}}}
	_, err := NormalizePluginObservation("device-a", "system", "1.2.3", descriptor, plugins.CollectResult{ObservedAt: time.Now(), Samples: []plugins.MetricSample{{MetricID: "unknown", Value: json.RawMessage(`1`), Quality: plugins.QualityValid}}})
	if err == nil {
		t.Fatal("expected undescribed metric to be rejected")
	}
}

func TestNormalizePluginOutcomeUsesCapturedDescriptor(t *testing.T) {
	outcome := plugins.CollectionOutcome{
		PluginID: "system", PluginVersion: "1.2.3", CollectorID: "cpu",
		Descriptor: plugins.CollectorDescriptor{ID: "cpu", Metrics: []plugins.MetricDescriptor{{ID: "usage", Type: plugins.MetricGauge, Unit: "percent"}}},
		Result:     plugins.CollectResult{ObservedAt: time.Unix(123, 0).UTC(), Samples: []plugins.MetricSample{{MetricID: "usage", Value: json.RawMessage(`42`), Quality: plugins.QualityValid}}},
	}
	got, err := NormalizePluginOutcome("device-a", outcome)
	if err != nil {
		t.Fatal(err)
	}
	if got.PluginID != outcome.PluginID || got.PluginVersion != outcome.PluginVersion || got.CollectorID != outcome.CollectorID {
		t.Fatalf("normalized scheduler metadata = %+v", got)
	}
}

package plugins

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCollectorDescriptorValidation(t *testing.T) {
	descriptor := CollectorDescriptor{
		ID:      "network.flow",
		Metrics: []MetricDescriptor{{ID: "network.rx_bytes", Type: MetricCounter, Unit: "byte", LabelKeys: []string{"interface"}}},
	}
	if err := descriptor.Validate(); err != nil {
		t.Fatalf("valid descriptor rejected: %v", err)
	}
	descriptor.Metrics = append(descriptor.Metrics, descriptor.Metrics[0])
	if err := descriptor.Validate(); err == nil {
		t.Fatal("duplicate metric descriptor was accepted")
	}
}

func TestCollectResultRequiresDescribedMetricsAndObservationTime(t *testing.T) {
	descriptor := CollectorDescriptor{ID: "network.flow", Metrics: []MetricDescriptor{{ID: "network.rx_bytes", Type: MetricCounter, Unit: "byte", LabelKeys: []string{"interface"}}}}
	result := CollectResult{
		ObservedAt: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC),
		Samples:    []MetricSample{{MetricID: "network.rx_bytes", Value: json.RawMessage("123"), Quality: QualityValid, Labels: map[string]string{"interface": "eth0"}}},
	}
	if err := result.Validate(descriptor); err != nil {
		t.Fatalf("valid result rejected: %v", err)
	}
	result.Samples[0].MetricID = "network.unknown"
	if err := result.Validate(descriptor); err == nil {
		t.Fatal("sample for an undescribed metric was accepted")
	}
	result.Samples[0].MetricID = "network.rx_bytes"
	result.ObservedAt = time.Time{}
	if err := result.Validate(descriptor); err == nil {
		t.Fatal("result without actual observation time was accepted")
	}
}

func TestCollectResultValidatesDimensionLabelsAndAllowsDistinctSeries(t *testing.T) {
	descriptor := CollectorDescriptor{ID: "network.flow", Metrics: []MetricDescriptor{{
		ID: "network.rx_bytes", Type: MetricCounter, Unit: "byte", LabelKeys: []string{"interface", "direction"},
	}}}
	base := MetricSample{MetricID: "network.rx_bytes", Value: json.RawMessage("123"), Quality: QualityValid}
	result := CollectResult{ObservedAt: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), Samples: []MetricSample{
		{MetricID: base.MetricID, Value: base.Value, Quality: base.Quality, Labels: map[string]string{"interface": "eth0", "direction": "rx"}},
		{MetricID: base.MetricID, Value: base.Value, Quality: base.Quality, Labels: map[string]string{"interface": "eth1", "direction": "rx"}},
	}}
	if err := result.Validate(descriptor); err != nil {
		t.Fatalf("distinct labeled series rejected: %v", err)
	}
	result.Samples[1].Labels["interface"] = "eth0"
	if err := result.Validate(descriptor); err == nil {
		t.Fatal("duplicate metric and label identity was accepted")
	}
	result.Samples[1].Labels = map[string]string{"interface": "eth1"}
	if err := result.Validate(descriptor); err == nil {
		t.Fatal("sample missing a declared label was accepted")
	}
	result.Samples[1].Labels = map[string]string{"interface": "eth1", "direction": "rx", "unexpected": "x"}
	if err := result.Validate(descriptor); err == nil {
		t.Fatal("sample with an undeclared label was accepted")
	}
	result.Samples[1].Labels = map[string]string{"interface": string([]byte{0xff}), "direction": "rx"}
	if err := result.Validate(descriptor); err == nil {
		t.Fatal("sample with a non-UTF-8 label value was accepted")
	}
}

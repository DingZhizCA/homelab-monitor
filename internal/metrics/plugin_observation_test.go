/*
Package metrics tests validation of the storage-independent plugin observation envelope.
*/
package metrics

import (
	"encoding/json"
	"testing"
	"time"
)

func TestPluginObservationValidate(t *testing.T) {
	base := PluginObservation{SchemaVersion: 1, DeviceID: "d1", PluginID: "p1", PluginVersion: "1", CollectorID: "c1", ObservedAt: time.Now(), Samples: []PluginMetricObservation{{MetricID: "load", Type: "gauge", Unit: "ratio", Value: json.RawMessage(`0.5`), Quality: "valid", Labels: map[string]string{"cpu": "0"}}}}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid observation rejected: %v", err)
	}
	duplicate := base
	duplicate.Samples = append(append([]PluginMetricObservation(nil), base.Samples...), base.Samples[0])
	if err := duplicate.Validate(); err == nil {
		t.Fatal("duplicate metric dimension accepted")
	}
	invalid := base
	invalid.Samples = append([]PluginMetricObservation(nil), base.Samples...)
	invalid.Samples[0].Value = json.RawMessage(`{`)
	if err := invalid.Validate(); err == nil {
		t.Fatal("invalid JSON value accepted")
	}
}

func TestPluginObservationRejectsInvalidIdentifiersAndUTF8(t *testing.T) {
	base := PluginObservation{SchemaVersion: 1, DeviceID: "d1", PluginID: "p1", PluginVersion: "1", CollectorID: "c1", ObservedAt: time.Now(), Samples: []PluginMetricObservation{{MetricID: "load", Type: "gauge", Unit: "ratio", Value: json.RawMessage(`0.5`), Quality: "valid"}}}
	cases := []struct {
		name string
		edit func(*PluginObservation)
	}{
		{"metric identifier", func(o *PluginObservation) { o.Samples[0].MetricID = "bad/id" }},
		{"label key", func(o *PluginObservation) { o.Samples[0].Labels = map[string]string{"bad/key": "x"} }},
		{"label utf8", func(o *PluginObservation) { o.Samples[0].Labels = map[string]string{"iface": string([]byte{0xff})} }},
		{"value utf8", func(o *PluginObservation) { o.Samples[0].Value = json.RawMessage{'"', 0xff, '"'} }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			observation := base
			observation.Samples = append([]PluginMetricObservation(nil), base.Samples...)
			test.edit(&observation)
			if err := observation.Validate(); err == nil {
				t.Fatal("invalid observation was accepted")
			}
		})
	}
}

func TestPluginObservationDuplicateIdentityIncludesLabelKeys(t *testing.T) {
	base := PluginObservation{SchemaVersion: 1, DeviceID: "d1", PluginID: "p1", PluginVersion: "1", CollectorID: "c1", ObservedAt: time.Now(), Samples: []PluginMetricObservation{
		{MetricID: "load", Type: "gauge", Unit: "ratio", Value: json.RawMessage(`0.5`), Quality: "valid", Labels: map[string]string{"core": ""}},
		{MetricID: "load", Type: "gauge", Unit: "ratio", Value: json.RawMessage(`0.6`), Quality: "valid", Labels: map[string]string{"thread": ""}},
	}}
	if err := base.Validate(); err != nil {
		t.Fatalf("distinct label keys were treated as duplicate series: %v", err)
	}
}

func TestPluginObservationPreservesWhitespaceInLabelValues(t *testing.T) {
	observation := PluginObservation{SchemaVersion: 1, DeviceID: "d1", PluginID: "p1", PluginVersion: "1", CollectorID: "c1", ObservedAt: time.Now(), Samples: []PluginMetricObservation{{
		MetricID: "state", Type: "state", Unit: "text", Value: json.RawMessage(`"active"`), Quality: "valid", Labels: map[string]string{"interface": " eth0 "},
	}}}
	if err := observation.Validate(); err != nil {
		t.Fatalf("valid label value with whitespace was rejected: %v", err)
	}
}

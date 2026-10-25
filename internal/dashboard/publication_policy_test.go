/*
Package dashboard tests shared publication validation against the built-in
Viewer metric descriptor surface.
*/
package dashboard

import (
	"context"
	"errors"
	"testing"
)

type pluginPublicationSourceFake struct {
	exists    bool
	err       error
	deviceID  string
	reference PluginMetricReference
}

func (f *pluginPublicationSourceFake) HasPluginMetricDescriptor(_ context.Context, deviceID string, reference PluginMetricReference) (bool, error) {
	f.deviceID, f.reference = deviceID, reference
	return f.exists, f.err
}

func TestViewerPublicationPolicyAcceptsBuiltinsAndRejectsUnknownDescriptors(t *testing.T) {
	policy, err := NewViewerPublicationPolicy(NewMetricRegistry(), nil)
	if err != nil {
		t.Fatal(err)
	}
	document := Document{Widgets: []Widget{{ID: "cpu", Kind: WidgetMetricCard, Title: "CPU", DescriptorID: "cpu.usage_percent"}}}
	if err := policy.ValidateViewerAccess(context.Background(), document); err != nil {
		t.Fatal(err)
	}
	document.Widgets[0].DescriptorID = "plugin.unpublished"
	if err := policy.ValidateViewerAccess(context.Background(), document); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unknown descriptor error=%v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := policy.ValidateViewerAccess(canceled, Document{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled publication error=%v", err)
	}
}

func TestViewerPublicationPolicyRequiresAnObservedExactPluginMetric(t *testing.T) {
	reference := PluginMetricReference{PluginID: "smart", PluginVersion: "1.0.0", CollectorID: "health", MetricID: "temperature"}
	document := Document{Widgets: []Widget{{ID: "temperature", Kind: WidgetTimeSeries, Title: "Disk temperature", DeviceID: "node-a", PluginMetric: &reference}}}
	source := &pluginPublicationSourceFake{exists: true}
	policy, err := NewViewerPublicationPolicy(NewMetricRegistry(), source)
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.ValidateViewerAccess(context.Background(), document); err != nil {
		t.Fatal(err)
	}
	if source.deviceID != "node-a" || source.reference != reference {
		t.Fatalf("exact plugin reference was not checked: device=%q reference=%+v", source.deviceID, source.reference)
	}
	source.exists = false
	if err := policy.ValidateViewerAccess(context.Background(), document); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unobserved plugin metric error=%v", err)
	}
	policy, err = NewViewerPublicationPolicy(NewMetricRegistry(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.ValidateViewerAccess(context.Background(), document); !errors.Is(err, ErrForbidden) {
		t.Fatalf("disabled plugin publication error=%v", err)
	}
}

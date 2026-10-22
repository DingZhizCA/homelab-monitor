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

func TestViewerPublicationPolicyAcceptsBuiltinsAndRejectsUnknownDescriptors(t *testing.T) {
	policy, err := NewViewerPublicationPolicy(NewMetricRegistry())
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

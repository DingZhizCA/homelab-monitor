/* Package pluginmetrics tests catalog authorization, limits, and detached schemas. */
package pluginmetrics

import (
	"context"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/auth"
)

type descriptorSourceFake struct{ items []Descriptor }

func (f descriptorSourceFake) ListPluginMetricDescriptors(context.Context, string, int) ([]Descriptor, error) {
	return f.items, nil
}

func TestCatalogServiceReturnsVersionedObservedDescriptors(t *testing.T) {
	now := time.Now().UTC()
	sourceLabels := []string{"disk"}
	service, err := NewCatalogService(descriptorSourceFake{[]Descriptor{{DeviceID: "node-a", PluginID: "smart", PluginVersion: "1.0.0", CollectorID: "temperature", MetricID: "temperature", Type: "gauge", Unit: "celsius", LabelKeys: sourceLabels, FirstObserved: now, LastObserved: now}}}, 2)
	if err != nil {
		t.Fatal(err)
	}
	items, err := service.List(context.Background(), auth.Principal{UserID: "viewer", Role: auth.RoleViewer}, "node-a")
	if err != nil || len(items) != 1 || items[0].PluginVersion != "1.0.0" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	sourceLabels[0] = "changed"
	if items[0].LabelKeys[0] != "disk" {
		t.Fatal("catalog retained source-owned label keys")
	}
}

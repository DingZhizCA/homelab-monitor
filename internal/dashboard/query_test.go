/*
Package dashboard tests descriptor lookup, authorization order, and metric quality.

Fixtures use only in-memory latest snapshots and never schedule Agent sampling.
*/
package dashboard

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/auth"
	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

type snapshotSourceFake struct {
	snapshot Snapshot
	found    bool
	calls    int
}

func (f *snapshotSourceFake) Current(_ context.Context, _ string) (Snapshot, bool, error) {
	f.calls++
	return f.snapshot, f.found, nil
}

type metricPolicyFake struct{ err error }

func (f metricPolicyFake) AuthorizeMetric(auth.Principal, string, string) error { return f.err }

func TestMetricRegistryListsStableDescriptorsAndCopiesDimensions(t *testing.T) {
	registry := NewMetricRegistry()
	items := registry.List()
	if len(items) != 9 {
		t.Fatalf("descriptor count=%d, want 9", len(items))
	}
	for index := 1; index < len(items); index++ {
		if items[index-1].ID >= items[index].ID {
			t.Fatalf("descriptors are not sorted: %q then %q", items[index-1].ID, items[index].ID)
		}
	}
	descriptor, ok := registry.Lookup("network.rx_bytes_per_second")
	if !ok || len(descriptor.Dimensions) != 1 || descriptor.Dimensions[0] != "interface" {
		t.Fatalf("network descriptor=%+v found=%t", descriptor, ok)
	}
	descriptor.Dimensions[0] = "mutated"
	again, _ := registry.Lookup("network.rx_bytes_per_second")
	if again.Dimensions[0] != "interface" {
		t.Fatal("caller mutation changed registry descriptor")
	}
}

func TestQueryLatestAuthorizesBeforeReadingAndPreservesMissingAndStaleQuality(t *testing.T) {
	source := &snapshotSourceFake{found: true, snapshot: Snapshot{
		Envelope: metrics.MetricEnvelope{
			Device:    metrics.DeviceInfo{ID: "node-a"},
			Timestamp: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC),
			Disks:     []metrics.DiskMetrics{{MountPoint: "/data", CapacityAvailable: false}},
			Networks:  []metrics.NetworkMetrics{{Interface: "eth0", RXBytesPerSecond: 12.5}},
		},
		ReceivedAt: time.Date(2026, 10, 17, 12, 0, 1, 0, time.UTC),
		Status:     "stale",
	}}
	actor := auth.Principal{UserID: "viewer-1", Role: auth.RoleViewer}
	service, err := NewQueryService(source, metricPolicyFake{err: auth.ErrForbidden}, NewMetricRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.QueryLatest(context.Background(), actor, "cpu.usage_percent", "node-a"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("unauthorized query error=%v", err)
	}
	if source.calls != 0 {
		t.Fatal("unauthorized query read a snapshot")
	}

	service, err = NewQueryService(source, metricPolicyFake{}, NewMetricRegistry())
	if err != nil {
		t.Fatal(err)
	}
	disk, err := service.QueryLatest(context.Background(), actor, "disk.usage_percent", "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(disk.Points) != 1 || disk.Points[0].Value != nil || disk.Points[0].Quality != "missing" || disk.Points[0].Dimensions["mount_point"] != "/data" {
		t.Fatalf("deferred disk capacity was not represented as missing: %+v", disk.Points)
	}
	network, err := service.QueryLatest(context.Background(), actor, "network.rx_bytes_per_second", "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(network.Points) != 1 || network.Points[0].Value == nil || *network.Points[0].Value != 12.5 || network.Points[0].Quality != "stale" || network.Points[0].Dimensions["interface"] != "eth0" {
		t.Fatalf("network value quality/dimension incorrect: %+v", network.Points)
	}
}

func TestQueryLatestDistinguishesUnsupportedAndUnavailableData(t *testing.T) {
	source := &snapshotSourceFake{}
	service, err := NewQueryService(source, metricPolicyFake{}, NewMetricRegistry())
	if err != nil {
		t.Fatal(err)
	}
	actor := auth.Principal{UserID: "owner-1", Role: auth.RoleOwner}
	if _, err := service.QueryLatest(context.Background(), actor, "unknown.metric", "node-a"); !errors.Is(err, ErrUnsupportedMetric) {
		t.Fatalf("unsupported metric error=%v", err)
	}
	if _, err := service.QueryLatest(context.Background(), actor, "cpu.usage_percent", "node-a"); !errors.Is(err, ErrSnapshotUnavailable) {
		t.Fatalf("missing snapshot error=%v", err)
	}
	if _, err := service.QueryLatest(context.Background(), actor, "cpu.usage_percent", ""); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("empty device error=%v", err)
	}
}

func TestQueryLatestRejectsNonCanonicalIdentityAndOutOfScopeSnapshots(t *testing.T) {
	actor := auth.Principal{UserID: "owner-1", Role: auth.RoleOwner}
	source := &snapshotSourceFake{found: true, snapshot: Snapshot{
		Envelope: metrics.MetricEnvelope{Device: metrics.DeviceInfo{ID: "node-b"}, Timestamp: time.Now()},
	}}
	service, err := NewQueryService(source, metricPolicyFake{}, NewMetricRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.QueryLatest(context.Background(), actor, "cpu.usage_percent", " node-a "); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("untrimmed device ID error=%v", err)
	}
	if source.calls != 0 {
		t.Fatal("untrimmed identity reached the snapshot source")
	}
	if _, err := service.QueryLatest(context.Background(), actor, "cpu.usage_percent", "node-a"); !errors.Is(err, ErrInvalidMetricSnapshot) {
		t.Fatalf("out-of-scope snapshot error=%v", err)
	}
}

func TestQueryLatestRejectsInvalidExtractedMetricValuesAndDimensions(t *testing.T) {
	actor := auth.Principal{UserID: "owner-1", Role: auth.RoleOwner}
	for _, test := range []struct {
		name       string
		descriptor string
		envelope   metrics.MetricEnvelope
	}{
		{
			name: "non-finite value", descriptor: "network.rx_bytes_per_second",
			envelope: metrics.MetricEnvelope{Device: metrics.DeviceInfo{ID: "node-a"}, Networks: []metrics.NetworkMetrics{{Interface: "eth0", RXBytesPerSecond: math.NaN()}}},
		},
		{
			name: "invalid dimension encoding", descriptor: "network.rx_bytes_per_second",
			envelope: metrics.MetricEnvelope{Device: metrics.DeviceInfo{ID: "node-a"}, Networks: []metrics.NetworkMetrics{{Interface: string([]byte{0xff})}}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &snapshotSourceFake{found: true, snapshot: Snapshot{Envelope: test.envelope}}
			service, err := NewQueryService(source, metricPolicyFake{}, NewMetricRegistry())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.QueryLatest(context.Background(), actor, test.descriptor, "node-a"); !errors.Is(err, ErrInvalidMetricSnapshot) {
				t.Fatalf("invalid snapshot error=%v", err)
			}
		})
	}
}

func TestQueryLatestDetachesOptionalMetricValueFromSnapshotSource(t *testing.T) {
	temperature := 47.5
	source := &snapshotSourceFake{found: true, snapshot: Snapshot{
		Envelope: metrics.MetricEnvelope{Device: metrics.DeviceInfo{ID: "node-a"}, CPU: metrics.CPUMetrics{TemperatureCelsius: &temperature}},
	}}
	service, err := NewQueryService(source, metricPolicyFake{}, NewMetricRegistry())
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.QueryLatest(context.Background(), auth.Principal{UserID: "owner-1", Role: auth.RoleOwner}, "cpu.temperature_celsius", "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Points) != 1 || result.Points[0].Value == nil {
		t.Fatalf("temperature result=%+v", result.Points)
	}
	*result.Points[0].Value = 99
	if temperature != 47.5 {
		t.Fatalf("result mutation changed source snapshot temperature to %v", temperature)
	}
}

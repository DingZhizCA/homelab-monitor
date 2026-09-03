package agent

import (
	"math"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

func TestCPUUsageDelta(t *testing.T) {
	previous := cpuTimes{total: 100, idle: 40}
	current := cpuTimes{total: 200, idle: 60}
	total, idle := current.total-previous.total, current.idle-previous.idle
	got := float64(total-idle) * 100 / float64(total)
	if math.Abs(got-80) > 0.001 {
		t.Fatalf("usage = %f", got)
	}
}

func TestDiskRefreshSafetyPolicy(t *testing.T) {
	tests := []struct {
		name                                             string
		rotational, scheduled, allowWake, inFlight, want bool
	}{
		{"idle HDD", true, true, false, false, false},
		{"busy HDD", true, false, false, true, true},
		{"explicit HDD wake", true, false, true, false, true},
		{"unscheduled SSD", false, false, false, false, false},
		{"scheduled SSD", false, true, false, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldRefreshDisk(tt.rotational, tt.scheduled, tt.allowWake, tt.inFlight); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDiskInFlightParsing(t *testing.T) {
	if ioInFlight([]byte("1 2 3 4 5 6 7 8 0 10 11")) {
		t.Fatal("idle disk reported busy")
	}
	if !ioInFlight([]byte("1 2 3 4 5 6 7 8 1 10 11")) {
		t.Fatal("busy disk reported idle")
	}
	if ioInFlight([]byte("invalid")) {
		t.Fatal("invalid stats reported busy")
	}
}

func TestDiskCachePreservesCollectionTime(t *testing.T) {
	c := NewCollector(Options{})
	collectedAt := time.Date(2026, 9, 3, 1, 2, 3, 0, time.UTC)
	c.mergeDisks([]metrics.DiskMetrics{{MountPoint: "/data", CollectedAt: collectedAt}}, map[string]bool{"/data": true})
	c.mergeDisks(nil, map[string]bool{"/data": true})
	if len(c.disks) != 1 || !c.disks[0].CollectedAt.Equal(collectedAt) {
		t.Fatalf("cached timestamp changed: %+v", c.disks)
	}
}

func TestUnescapeMount(t *testing.T) {
	if got := unescapeMount(`/media/My\040Disk`); got != "/media/My Disk" {
		t.Fatalf("got %q", got)
	}
}

func TestLiveCollection(t *testing.T) {
	c := NewCollector(Options{Disk: true, Network: true})
	snapshot, err := c.Collect(metrics.DeviceInfo{ID: "test", DisplayName: "Test"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SchemaVersion != 1 || snapshot.Memory.TotalBytes == 0 || snapshot.CPU.LogicalCores == 0 {
		t.Fatalf("invalid snapshot: %+v", snapshot)
	}
	second, err := c.Collect(metrics.DeviceInfo{ID: "test", DisplayName: "Test"})
	if err != nil {
		t.Fatal(err)
	}
	if second.CPU.FrequencyMHz != snapshot.CPU.FrequencyMHz || second.CPU.TemperatureCelsius != snapshot.CPU.TemperatureCelsius {
		t.Fatal("cached sensor pointers changed before the sensor interval elapsed")
	}
}

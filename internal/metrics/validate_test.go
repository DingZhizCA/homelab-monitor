package metrics

import (
	"math"
	"testing"
	"time"
)

func validEnvelope() MetricEnvelope {
	return MetricEnvelope{SchemaVersion: 1, SamplingMode: "idle", SamplingIntervalSeconds: 60, Device: DeviceInfo{ID: "n1", DisplayName: "Phicomm N1", Hostname: "armbian"}, Timestamp: time.Now(), CPU: CPUMetrics{UsagePercent: 12.5, LogicalCores: 4}, Memory: MemoryMetrics{TotalBytes: 100, UsedBytes: 25, AvailableBytes: 75, UsagePercent: 25}}
}

func TestValidateRejectsNonFiniteMetricValues(t *testing.T) {
	positiveInfinity := math.Inf(1)
	negativeInfinity := math.Inf(-1)
	tests := []struct {
		name string
		edit func(*MetricEnvelope)
	}{
		{"system load1", func(m *MetricEnvelope) { m.System.Load1 = math.NaN() }},
		{"system load5", func(m *MetricEnvelope) { m.System.Load5 = positiveInfinity }},
		{"system load15", func(m *MetricEnvelope) { m.System.Load15 = negativeInfinity }},
		{"cpu usage", func(m *MetricEnvelope) { m.CPU.UsagePercent = math.NaN() }},
		{"cpu frequency", func(m *MetricEnvelope) { m.CPU.FrequencyMHz = &positiveInfinity }},
		{"cpu temperature", func(m *MetricEnvelope) { m.CPU.TemperatureCelsius = &negativeInfinity }},
		{"memory usage", func(m *MetricEnvelope) { m.Memory.UsagePercent = math.NaN() }},
		{"disk usage", func(m *MetricEnvelope) {
			m.Disks = []DiskMetrics{{MountPoint: "/data", UsagePercent: positiveInfinity}}
		}},
		{"network receive rate", func(m *MetricEnvelope) {
			m.Networks = []NetworkMetrics{{Interface: "eth0", RXBytesPerSecond: math.NaN()}}
		}},
		{"network transmit rate", func(m *MetricEnvelope) {
			m.Networks = []NetworkMetrics{{Interface: "eth0", TXBytesPerSecond: negativeInfinity}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			envelope := validEnvelope()
			test.edit(&envelope)
			if err := envelope.Validate(); err == nil {
				t.Fatal("non-finite metric value was accepted")
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		edit    func(*MetricEnvelope)
		wantErr bool
	}{
		{name: "valid"},
		{name: "schema", edit: func(m *MetricEnvelope) { m.SchemaVersion = 2 }, wantErr: true},
		{name: "device id", edit: func(m *MetricEnvelope) { m.Device.ID = "" }, wantErr: true},
		{name: "untrimmed device id", edit: func(m *MetricEnvelope) { m.Device.ID = " n1 " }, wantErr: true},
		{name: "cpu percentage", edit: func(m *MetricEnvelope) { m.CPU.UsagePercent = 101 }, wantErr: true},
		{name: "negative load average", edit: func(m *MetricEnvelope) { m.System.Load1 = -0.1 }, wantErr: true},
		{name: "unavailable disk percentage range", edit: func(m *MetricEnvelope) {
			m.Disks = []DiskMetrics{{MountPoint: "/data", CapacityAvailable: false, UsagePercent: 101}}
		}, wantErr: true},
		{name: "zero sensor timestamp", edit: func(m *MetricEnvelope) { zero := time.Time{}; m.CPU.SensorCollectedAt = &zero }, wantErr: true},
		{name: "zero power timestamp", edit: func(m *MetricEnvelope) {
			zero := time.Time{}
			m.Disks = []DiskMetrics{{MountPoint: "/data", PhysicalDevices: []DiskDeviceInfo{{Name: "sda", PowerState: "unknown", PowerStateCollectedAt: &zero}}}}
		}, wantErr: true},
		{name: "memory capacity", edit: func(m *MetricEnvelope) { m.Memory.UsedBytes = 101 }, wantErr: true},
		{name: "deferred disk capacity", edit: func(m *MetricEnvelope) { m.Disks = []DiskMetrics{{MountPoint: "/data", MediaType: "hdd"}} }},
		{name: "available disk missing timestamp", edit: func(m *MetricEnvelope) {
			m.Disks = []DiskMetrics{{MountPoint: "/data", CapacityAvailable: true, TotalBytes: 100}}
		}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := validEnvelope()
			if tt.edit != nil {
				tt.edit(&m)
			}
			if got := m.Validate(); (got != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", got, tt.wantErr)
			}
		})
	}
}

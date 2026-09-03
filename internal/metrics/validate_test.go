package metrics

import (
	"testing"
	"time"
)

func validEnvelope() MetricEnvelope {
	return MetricEnvelope{SchemaVersion: 1, SamplingMode: "idle", SamplingIntervalSeconds: 60, Device: DeviceInfo{ID: "n1", DisplayName: "Phicomm N1", Hostname: "armbian"}, Timestamp: time.Now(), CPU: CPUMetrics{UsagePercent: 12.5, LogicalCores: 4}, Memory: MemoryMetrics{TotalBytes: 100, UsedBytes: 25, AvailableBytes: 75, UsagePercent: 25}}
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
		{name: "cpu percentage", edit: func(m *MetricEnvelope) { m.CPU.UsagePercent = 101 }, wantErr: true},
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

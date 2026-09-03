/*
Package metrics validates the Agent-to-Server metrics protocol.

本文件负责在快照发送和入库前检查协议不变量，包括必需字段、数值范围、时间戳、
磁盘容量可用性和可选电源状态。本文件不负责采集、修正或补全指标。

数据链路：MetricEnvelope -> Validate -> Agent sender / Server Store
*/
package metrics

import (
	"fmt"
	"strings"
)

// ValidationError identifies the protocol field that violated an invariant.
// ValidationError 标识违反协议约束的字段及原因，便于日志定位而不修改原始数据。
type ValidationError struct {
	Field   string
	Message string
}

// Error formats a ValidationError for logs and HTTP error envelopes.
func (e *ValidationError) Error() string {
	if e.Field == "" {
		return e.Message
	}
	return fmt.Sprintf("%s %s", e.Field, e.Message)
}

// Validate checks protocol invariants before a snapshot becomes server state.
// Validate 只验证数据，不把缺失值或异常值静默改写成看似有效的数据。
func (m MetricEnvelope) Validate() error {
	if m.SchemaVersion != 1 {
		return &ValidationError{"schema_version", "must be 1"}
	}
	if m.SamplingMode != "idle" && m.SamplingMode != "active" {
		return &ValidationError{"sampling_mode", "must be idle or active"}
	}
	if m.SamplingIntervalSeconds == 0 {
		return &ValidationError{"sampling_interval_seconds", "must be greater than 0"}
	}
	if m.RefreshError != "" && m.RefreshRequestID == "" {
		return &ValidationError{"refresh_error", "requires refresh_request_id"}
	}
	if strings.TrimSpace(m.Device.ID) == "" {
		return &ValidationError{"device.id", "is required"}
	}
	if strings.TrimSpace(m.Device.DisplayName) == "" {
		return &ValidationError{"device.display_name", "is required"}
	}
	if strings.TrimSpace(m.Device.Hostname) == "" {
		return &ValidationError{"device.hostname", "is required"}
	}
	if m.Timestamp.IsZero() {
		return &ValidationError{"timestamp", "is required"}
	}
	if err := percent("cpu.usage_percent", m.CPU.UsagePercent); err != nil {
		return err
	}
	if m.CPU.LogicalCores < 1 {
		return &ValidationError{"cpu.logical_cores", "must be greater than 0"}
	}
	if m.CPU.FrequencyMHz != nil && *m.CPU.FrequencyMHz < 0 {
		return &ValidationError{"cpu.frequency_mhz", "must not be negative"}
	}
	if m.Memory.TotalBytes == 0 {
		return &ValidationError{"memory.total_bytes", "must be greater than 0"}
	}
	if m.Memory.UsedBytes > m.Memory.TotalBytes {
		return &ValidationError{"memory.used_bytes", "must not exceed total_bytes"}
	}
	if m.Memory.AvailableBytes > m.Memory.TotalBytes {
		return &ValidationError{"memory.available_bytes", "must not exceed total_bytes"}
	}
	if err := percent("memory.usage_percent", m.Memory.UsagePercent); err != nil {
		return err
	}
	for i, disk := range m.Disks {
		prefix := fmt.Sprintf("disks[%d]", i)
		if strings.TrimSpace(disk.MountPoint) == "" {
			return &ValidationError{prefix + ".mount_point", "is required"}
		}
		capacityAvailable := disk.CapacityAvailable || disk.TotalBytes > 0 // accept protocol-v1 agents predating the flag
		if capacityAvailable {
			if disk.TotalBytes == 0 {
				return &ValidationError{prefix + ".total_bytes", "must be greater than 0 when capacity is available"}
			}
			if disk.UsedBytes > disk.TotalBytes || disk.AvailableBytes > disk.TotalBytes {
				return &ValidationError{prefix, "capacity values must not exceed total_bytes"}
			}
			if err := percent(prefix+".usage_percent", disk.UsagePercent); err != nil {
				return err
			}
			if disk.CollectedAt.IsZero() {
				return &ValidationError{prefix + ".collected_at", "is required"}
			}
			if disk.CollectionMode != "passive" && disk.CollectionMode != "active" {
				return &ValidationError{prefix + ".collection_mode", "must be passive or active"}
			}
		}
		for j, device := range disk.PhysicalDevices {
			if device.PowerState != "" && device.PowerState != "active" && device.PowerState != "standby" && device.PowerState != "sleeping" && device.PowerState != "unknown" {
				return &ValidationError{fmt.Sprintf("%s.physical_devices[%d].power_state", prefix, j), "must be active, standby, sleeping, or unknown"}
			}
			if device.PowerState != "" && device.PowerStateCollectedAt == nil {
				return &ValidationError{fmt.Sprintf("%s.physical_devices[%d].power_state_collected_at", prefix, j), "is required when power_state is present"}
			}
		}
	}
	for i, network := range m.Networks {
		if strings.TrimSpace(network.Interface) == "" {
			return &ValidationError{fmt.Sprintf("networks[%d].interface", i), "is required"}
		}
		if network.RXBytesPerSecond < 0 || network.TXBytesPerSecond < 0 {
			return &ValidationError{fmt.Sprintf("networks[%d]", i), "rates must not be negative"}
		}
	}
	return nil
}

func percent(field string, value float64) error {
	if value < 0 || value > 100 {
		return &ValidationError{field, "must be between 0 and 100"}
	}
	return nil
}

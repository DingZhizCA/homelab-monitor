/*
Package metrics defines the metric data structures shared by homelab-agent
and homelab-server.

本文件职责：
- 定义 Agent 与 Server 之间传输的指标数据结构
- 定义 JSON 协议字段与 Go 类型之间的映射
- 为后续指标采集、HTTP 传输和 Server 解码提供统一的数据模型

本文件不负责：
- 从 Linux /proc 或 /sys 采集指标
- 发送或处理 HTTP 请求
- 保存 Server 的 latest snapshot

数据链路：
Linux collectors -> metrics data model -> HTTP JSON -> homelab-server
*/
package metrics

import "time"

// DeviceInfo represents the stable identity and display information of a monitored device.
// DeviceInfo 表示被监控设备的稳定身份信息和显示信息。
type DeviceInfo struct {
	// ID is the stable and unique identifier of the device.
	// ID 是设备稳定且唯一的标识，不应依赖可能变化的 IP 地址。
	ID string `json:"id"`

	// DisplayName is the human-readable device name shown in the Dashboard.
	// DisplayName 是 Dashboard 中展示给用户看的设备名称。
	DisplayName string `json:"display_name"`

	// Hostname is the current hostname reported by the operating system.
	// Hostname 是操作系统当前报告的主机名，它可以变化，因此不作为设备唯一标识。
	Hostname string `json:"hostname"`
}

// SystemMetrics represents operating-system-level metrics collected for one sample.
// SystemMetrics 表示一次采样中获取的操作系统级指标。
// Load1、Load5 和 Load15 分别表示最近 1、5、15 分钟的系统平均负载，
// 它们表示运行或等待资源的任务负载，不等同于 CPU 使用率。
type SystemMetrics struct {
	// UptimeSeconds is the number of seconds the operating system has been running.
	// UptimeSeconds 表示操作系统自启动以来已经运行的秒数。
	UptimeSeconds uint64 `json:"uptime_seconds"`

	// Load1 is the system load average over the last 1 minute.
	// Load1 表示最近 1 分钟的系统平均负载。
	Load1 float64 `json:"load1"`

	// Load5 is the system load average over the last 5 minutes.
	// Load5 表示最近 5 分钟的系统平均负载。
	Load5 float64 `json:"load5"`

	// Load15 is the system load average over the last 15 minutes.
	// Load15 表示最近 15 分钟的系统平均负载。
	Load15 float64 `json:"load15"`
}

// CPUMetrics represents CPU-related metrics collected for one sample.
// CPUMetrics 表示一次采样中获取的 CPU 指标。
// FrequencyMHz 和 TemperatureCelsius 使用指针表示可选数据；
// nil 表示当前无法获取该指标，而不是把缺失数据误表示为 0。
type CPUMetrics struct {
	// UsagePercent is the total CPU usage percentage calculated from CPU time deltas.
	// UsagePercent 表示根据两次 CPU 累计时间差计算得到的总 CPU 使用率。
	UsagePercent float64 `json:"usage_percent"`

	// LogicalCores is the number of logical CPU cores visible to the operating system.
	// LogicalCores 表示操作系统可见的逻辑 CPU 核心数量。
	LogicalCores int `json:"logical_cores"`

	// FrequencyMHz is the current or representative CPU frequency in MHz.
	// FrequencyMHz 表示当前或具有代表性的 CPU 频率，单位为 MHz；
	// nil 表示当前系统无法提供该数据。
	FrequencyMHz *float64 `json:"frequency_mhz"`

	// TemperatureCelsius is the CPU temperature in degrees Celsius when available.
	// TemperatureCelsius 表示 CPU 温度，单位为摄氏度；
	// nil 表示当前没有可用的温度传感器数据。
	TemperatureCelsius *float64 `json:"temperature_celsius"`

	// SensorCollectedAt is when the cached frequency and temperature values
	// were last read from hardware-facing kernel interfaces.
	SensorCollectedAt *time.Time `json:"sensor_collected_at"`
}

// MemoryMetrics represents memory usage metrics collected for one sample.
// MemoryMetrics 表示一次采样中获取的内存使用指标。
type MemoryMetrics struct {
	// TotalBytes is the total amount of physical memory, in bytes.
	// TotalBytes 表示物理内存总容量，单位为字节。
	TotalBytes uint64 `json:"total_bytes"`

	// UsedBytes is the amount of memory considered currently in use, in bytes.
	// UsedBytes 表示当前已使用的内存容量，单位为字节。
	UsedBytes uint64 `json:"used_bytes"`

	// AvailableBytes is the amount of memory currently available for applications, in bytes.
	// AvailableBytes 表示当前可供应用程序使用的内存容量，单位为字节。
	AvailableBytes uint64 `json:"available_bytes"`

	// UsagePercent is the percentage of total memory currently considered used.
	// UsagePercent 表示当前已使用内存占总内存的百分比。
	UsagePercent float64 `json:"usage_percent"`
}

// DiskMetrics represents filesystem capacity and usage metrics for one mounted filesystem.
// DiskMetrics 表示一个已挂载文件系统的容量和使用情况。
// 当前仅记录文件系统容量、安全的 sysfs 元数据和可选 ATA 电源状态；不采集
// SMART、硬盘温度或磁头计数，避免监控行为唤醒 standby 状态的机械硬盘。
type DiskMetrics struct {
	// Device identifies the underlying device when it can be safely determined.
	// Device 表示该文件系统对应的底层设备，例如 /dev/nvme0n1p2。
	Device string `json:"device"`

	// MountPoint is the directory where the filesystem is mounted.
	// MountPoint 表示文件系统的挂载点，例如 / 或 /data。
	MountPoint string `json:"mount_point"`

	// Filesystem is the filesystem type, such as ext4 or xfs.
	// Filesystem 表示文件系统类型，例如 ext4、xfs。
	Filesystem string `json:"filesystem"`

	// MediaType is derived from the kernel's rotational flag without issuing
	// any command or read to the storage medium.
	MediaType string `json:"media_type"`

	// PhysicalDevices describes the leaf block devices backing this mount.
	// The values come from sysfs and are safe to inspect while an HDD sleeps.
	PhysicalDevices []DiskDeviceInfo `json:"physical_devices"`

	// CapacityAvailable distinguishes a deliberately deferred low-power read
	// from a real filesystem whose capacity happens to be zero.
	CapacityAvailable bool `json:"capacity_available"`

	// TotalBytes is the total filesystem capacity, in bytes.
	// TotalBytes 表示文件系统总容量，单位为字节。
	TotalBytes uint64 `json:"total_bytes"`

	// UsedBytes is the amount of filesystem space currently used, in bytes.
	// UsedBytes 表示文件系统当前已使用的容量，单位为字节。
	UsedBytes uint64 `json:"used_bytes"`

	// AvailableBytes is the amount of filesystem space available for use, in bytes.
	// AvailableBytes 表示文件系统当前可用容量，单位为字节。
	AvailableBytes uint64 `json:"available_bytes"`

	// UsagePercent is the percentage of filesystem capacity currently used.
	// UsagePercent 表示当前文件系统空间使用率。
	UsagePercent float64 `json:"usage_percent"`

	// CollectedAt records when capacity was actually refreshed. Cached values
	// retain their original timestamp in later snapshots.
	CollectedAt time.Time `json:"collected_at"`

	// CollectionMode is passive for normal sampling and active for an explicit
	// user-approved refresh that may wake a sleeping disk.
	CollectionMode string `json:"collection_mode"`
}

// DiskDeviceInfo contains hardware identity and an optional non-waking ATA
// power-state observation. It does not include SMART data, temperature,
// spindle speed, or head/load counters.
type DiskDeviceInfo struct {
	// Name is the Linux leaf block-device name, without the /dev prefix.
	Name string `json:"name"`

	// Model and Vendor come from sysfs and do not require media access.
	Model  string `json:"model,omitempty"`
	Vendor string `json:"vendor,omitempty"`

	// Rotational reports the kernel queue's rotational attribute.
	Rotational bool `json:"rotational"`

	// PowerState is active, standby, sleeping, or unknown. An empty value means
	// automatic power-state checks are disabled or unsafe for this transport.
	PowerState string `json:"power_state,omitempty"`

	// PowerStateCollectedAt is the time of the latest query attempt. Unknown is
	// still timestamped so the Dashboard never presents it as a fresh success.
	PowerStateCollectedAt *time.Time `json:"power_state_collected_at,omitempty"`
}

// NetworkMetrics represents traffic counters and calculated transfer rates for one network interface.
// NetworkMetrics 表示一个网络接口的累计流量计数和计算得到的传输速率。
// RXBytes 和 TXBytes 是累计计数器；每秒速率将在 Agent 中根据相邻两次采样的计数差和时间差计算。
type NetworkMetrics struct {
	// Interface is the operating-system name of the network interface.
	// Interface 表示操作系统中的网络接口名称，例如 enp59s0 或 tailscale0。
	Interface string `json:"interface"`

	// RXBytes is the cumulative number of bytes received by this interface.
	// RXBytes 表示该接口自计数器开始以来累计接收的字节数。
	RXBytes uint64 `json:"rx_bytes"`

	// TXBytes is the cumulative number of bytes transmitted by this interface.
	// TXBytes 表示该接口自计数器开始以来累计发送的字节数。
	TXBytes uint64 `json:"tx_bytes"`

	// RXBytesPerSecond is the receive rate calculated from consecutive RXBytes samples.
	// RXBytesPerSecond 表示根据相邻两次 RXBytes 计数差和时间差计算出的接收速率。
	RXBytesPerSecond float64 `json:"rx_bytes_per_second"`

	// TXBytesPerSecond is the transmit rate calculated from consecutive TXBytes samples.
	// TXBytesPerSecond 表示根据相邻两次 TXBytes 计数差和时间差计算出的发送速率。
	TXBytesPerSecond float64 `json:"tx_bytes_per_second"`
}

// MetricEnvelope represents one complete metrics snapshot sent from an Agent to the Server.
// MetricEnvelope 表示 Agent 发送给 Server 的一次完整指标快照。
// Timestamp 是 Agent 的实际采样时间；Server 接收时间 received_at 不属于该传输结构。
type MetricEnvelope struct {
	// SchemaVersion identifies the version of the metrics JSON protocol.
	// SchemaVersion 表示指标 JSON 协议版本，Phase 1 当前使用版本 1。
	SchemaVersion int `json:"schema_version"`

	// SamplingMode and SamplingIntervalSeconds describe the cadence that
	// produced this snapshot so the Server can derive correct liveness limits.
	SamplingMode            string `json:"sampling_mode"`
	SamplingIntervalSeconds uint64 `json:"sampling_interval_seconds"`

	// RefreshRequestID correlates an active disk snapshot with the explicit
	// user request that authorized it.
	RefreshRequestID string `json:"refresh_request_id,omitempty"`
	RefreshError     string `json:"refresh_error,omitempty"`

	// Device identifies the device that produced this metrics snapshot.
	// Device 表示产生本次指标快照的设备身份。
	Device DeviceInfo `json:"device"`

	// Timestamp is the time at which the Agent collected this sample.
	// Timestamp 表示 Agent 实际采集本次指标的时间。
	Timestamp time.Time `json:"timestamp"`

	// System contains operating-system-level metrics.
	// System 包含操作系统级指标。
	System SystemMetrics `json:"system"`

	// CPU contains CPU usage and sensor metrics.
	// CPU 包含 CPU 使用率及相关传感器指标。
	CPU CPUMetrics `json:"cpu"`

	// Memory contains physical memory usage metrics.
	// Memory 包含物理内存使用指标。
	Memory MemoryMetrics `json:"memory"`

	// Disks contains metrics for the mounted filesystems discovered on the device.
	// Disks 包含设备上各个已监控文件系统的指标。
	Disks []DiskMetrics `json:"disks"`

	// Networks contains metrics for the monitored network interfaces.
	// Networks 包含设备上各个被监控网络接口的指标。
	Networks []NetworkMetrics `json:"networks"`
}

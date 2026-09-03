/*
Package agent coordinates Linux metric collection for homelab-agent.

本文件负责组合 CPU、内存、文件系统、网络和低频磁盘电源状态采集，并维护采集缓存。
本文件不负责 HTTP 上报、Server 状态或 Dashboard 展示。

数据链路：Linux collectors -> Collector cache -> metrics.MetricEnvelope
*/
package agent

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

// Options controls optional collectors and their hardware-facing read cadence.
// Options 控制可选采集器，以及可能接触硬件接口的最低采集间隔。
type Options struct {
	// Disk and Network enable filesystem and network collection.
	Disk    bool
	Network bool

	// PowerState explicitly enables non-waking ATA power-mode queries for HDDs.
	// It remains independent from Disk so deployments must opt in deliberately.
	PowerState bool

	// SensorInterval limits CPU frequency and temperature reads.
	SensorInterval time.Duration

	// DiskInterval limits statfs refreshes for non-rotational filesystems.
	DiskInterval time.Duration

	// PowerInterval limits ATA CHECK POWER MODE queries when PowerState is enabled.
	PowerInterval time.Duration
}

// Collector combines Linux metric sources and caches slower hardware-facing data.
// Collector 组合 Linux 指标来源，并缓存采集频率较低的硬件相关数据。
type Collector struct {
	options           Options
	previousCPU       *cpuTimes
	previousNetwork   map[string]networkCounter
	previousAt        time.Time
	hostname          string
	logicalCores      int
	frequency         *float64
	temperature       *float64
	sensorCollectedAt *time.Time
	lastSensors       time.Time
	disks             []metrics.DiskMetrics
	diskCache         map[string]metrics.DiskMetrics
	lastDisks         time.Time
	powerStates       map[string]diskPowerSample
	lastPowerStates   time.Time
}

// NewCollector creates a Collector with safe defaults for omitted intervals.
// NewCollector 创建采集器，并为未配置的周期应用低功耗默认值。
func NewCollector(options Options) *Collector {
	if options.SensorInterval <= 0 {
		options.SensorInterval = time.Minute
	}
	if options.DiskInterval <= 0 {
		options.DiskInterval = 5 * time.Minute
	}
	if options.PowerInterval <= 0 {
		options.PowerInterval = time.Minute
	}
	return &Collector{options: options, previousNetwork: make(map[string]networkCounter), diskCache: make(map[string]metrics.DiskMetrics), powerStates: make(map[string]diskPowerSample), logicalCores: runtime.NumCPU()}
}

// Collect returns one normal sample without authorizing a disk wake-up.
// Collect 返回一次常规采样，不授予唤醒机械硬盘的权限。
func (c *Collector) Collect(device metrics.DeviceInfo) (metrics.MetricEnvelope, error) {
	return c.collect(device, false)
}

// CollectWithDiskRefresh performs an explicit user-requested disk refresh.
// Unlike normal collection, this operation may wake rotational disks.
func (c *Collector) CollectWithDiskRefresh(device metrics.DeviceInfo) (metrics.MetricEnvelope, error) {
	return c.collect(device, true)
}

func (c *Collector) collect(device metrics.DeviceInfo, allowDiskWake bool) (metrics.MetricEnvelope, error) {
	if c.hostname == "" {
		hostname, err := os.Hostname()
		if err != nil {
			return metrics.MetricEnvelope{}, fmt.Errorf("hostname: %w", err)
		}
		c.hostname = hostname
	}
	device.Hostname = c.hostname
	system, err := collectSystem()
	if err != nil {
		return metrics.MetricEnvelope{}, err
	}
	now := time.Now()
	if c.lastSensors.IsZero() || now.Sub(c.lastSensors) >= c.options.SensorInterval {
		c.frequency, c.temperature = cpuFrequency(), cpuTemperature()
		c.lastSensors = now
		collectedAt := now
		c.sensorCollectedAt = &collectedAt
	}
	cpu, currentCPU, err := collectCPU(c.previousCPU, c.logicalCores, c.frequency, c.temperature, c.sensorCollectedAt)
	if err != nil {
		return metrics.MetricEnvelope{}, err
	}
	c.previousCPU = &currentCPU
	memory, err := collectMemory()
	if err != nil {
		return metrics.MetricEnvelope{}, err
	}
	var disks []metrics.DiskMetrics
	if c.options.Disk {
		refreshNonRotational := c.lastDisks.IsZero() || now.Sub(c.lastDisks) >= c.options.DiskInterval
		refreshed, present := collectDisks(refreshNonRotational, allowDiskWake, now)
		if c.options.PowerState {
			c.updateDiskPowerStates(refreshed, now)
		}
		c.mergeDisks(refreshed, present)
		if refreshNonRotational || allowDiskWake {
			c.lastDisks = now
		}
		disks = c.disks
	}
	var networks []metrics.NetworkMetrics
	if c.options.Network {
		networks, c.previousNetwork = collectNetworks(c.previousNetwork, now.Sub(c.previousAt))
	}
	c.previousAt = now
	return metrics.MetricEnvelope{SchemaVersion: 1, Device: device, Timestamp: now, System: system, CPU: cpu, Memory: memory, Disks: nonNilDisks(disks), Networks: nonNilNetworks(networks)}, nil
}

func (c *Collector) mergeDisks(refreshed []metrics.DiskMetrics, present map[string]bool) {
	for mountPoint := range c.diskCache {
		if !present[mountPoint] {
			delete(c.diskCache, mountPoint)
		}
	}
	for _, disk := range refreshed {
		if cached, ok := c.diskCache[disk.MountPoint]; ok && !disk.CapacityAvailable && (cached.CapacityAvailable || cached.TotalBytes > 0) {
			disk.TotalBytes, disk.UsedBytes = cached.TotalBytes, cached.UsedBytes
			disk.AvailableBytes, disk.UsagePercent = cached.AvailableBytes, cached.UsagePercent
			disk.CapacityAvailable, disk.CollectedAt, disk.CollectionMode = true, cached.CollectedAt, cached.CollectionMode
		}
		c.diskCache[disk.MountPoint] = disk
	}
	c.disks = c.disks[:0]
	for _, disk := range c.diskCache {
		c.disks = append(c.disks, disk)
	}
	sort.Slice(c.disks, func(i, j int) bool { return c.disks[i].MountPoint < c.disks[j].MountPoint })
}

func nonNilDisks(v []metrics.DiskMetrics) []metrics.DiskMetrics {
	if v == nil {
		return []metrics.DiskMetrics{}
	}
	return v
}
func nonNilNetworks(v []metrics.NetworkMetrics) []metrics.NetworkMetrics {
	if v == nil {
		return []metrics.NetworkMetrics{}
	}
	return v
}

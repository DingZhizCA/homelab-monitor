// Package agent collects Linux metrics without invoking external commands.
package agent

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

type Options struct {
	Disk, Network  bool
	SensorInterval time.Duration
	DiskInterval   time.Duration
}

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
}

func NewCollector(options Options) *Collector {
	if options.SensorInterval <= 0 {
		options.SensorInterval = time.Minute
	}
	if options.DiskInterval <= 0 {
		options.DiskInterval = 5 * time.Minute
	}
	return &Collector{options: options, previousNetwork: make(map[string]networkCounter), diskCache: make(map[string]metrics.DiskMetrics), logicalCores: runtime.NumCPU()}
}

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

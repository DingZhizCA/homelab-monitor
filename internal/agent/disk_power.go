/*
Package agent implements optional, low-frequency HDD power-state observation.

本文件负责在明确启用后，通过 ATA CHECK POWER MODE 查询 SATA 直连机械盘状态，
并把结果缓存后附加到文件系统的底层设备信息。本文件不读取 SMART、温度、磁头计数
或盘片数据，也不改变磁盘电源状态。

数据链路：sysfs transport check -> hdparm -C -> disk power cache -> DiskDeviceInfo
*/
package agent

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

type diskPowerSample struct {
	state       string
	collectedAt time.Time
}

// updateDiskPowerStates refreshes each physical HDD at most once per configured
// interval. Query failures become unknown data and never fail the main sample.
func (c *Collector) updateDiskPowerStates(disks []metrics.DiskMetrics, now time.Time) {
	refresh := c.lastPowerStates.IsZero() || now.Sub(c.lastPowerStates) >= c.options.PowerInterval
	seen := make(map[string]bool)
	for i := range disks {
		for j := range disks[i].PhysicalDevices {
			device := &disks[i].PhysicalDevices[j]
			if !device.Rotational {
				continue
			}
			if refresh && !seen[device.Name] && safeATATransport(device.Name) {
				seen[device.Name] = true
				c.powerStates[device.Name] = diskPowerSample{state: queryATAPowerState(device.Name), collectedAt: now}
			}
			if sample, ok := c.powerStates[device.Name]; ok {
				device.PowerState = sample.state
				collectedAt := sample.collectedAt
				device.PowerStateCollectedAt = &collectedAt
			}
		}
	}
	if refresh {
		c.lastPowerStates = now
	}
}

// safeATATransport limits automatic queries to kernel paths that are visibly
// attached through ATA. USB/SAS/virtual bridges remain unqueried because their
// handling of ATA power commands cannot be inferred safely.
func safeATATransport(name string) bool {
	path, err := filepath.EvalSymlinks(filepath.Join("/sys/class/block", name, "device"))
	return err == nil && strings.Contains(path, "/ata")
}

func queryATAPowerState(name string) string {
	// hdparm -C only issues ATA CHECK POWER MODE. Tool, timeout, transport,
	// and permission failures mean unavailable data, not a failed main sample.
	path, err := exec.LookPath("hdparm")
	if err != nil {
		return "unknown"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, path, "-C", filepath.Join("/dev", name)).CombinedOutput()
	if err != nil {
		return "unknown"
	}
	return parseATAPowerState(string(output))
}

func parseATAPowerState(output string) string {
	value := strings.ToLower(output)
	switch {
	case strings.Contains(value, "standby"):
		return "standby"
	case strings.Contains(value, "sleeping"):
		return "sleeping"
	case strings.Contains(value, "active/idle"), strings.Contains(value, "active"):
		return "active"
	default:
		return "unknown"
	}
}

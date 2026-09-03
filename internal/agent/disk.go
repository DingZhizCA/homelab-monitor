/*
Package agent implements low-power Linux filesystem capacity discovery.

本文件负责解析挂载点、解析块设备拓扑并按介质类型决定是否调用 statfs。
本文件不读取 SMART、硬盘温度或磁头数据，也不查询 HDD 电源模式。

数据链路：mountinfo and sysfs -> safe statfs decision -> DiskMetrics -> Collector
*/
package agent

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

var ignoredFilesystems = map[string]bool{
	"proc": true, "sysfs": true, "devtmpfs": true, "devpts": true, "tmpfs": true,
	"cgroup": true, "cgroup2": true, "securityfs": true, "debugfs": true,
	"tracefs": true, "configfs": true, "pstore": true, "mqueue": true,
	"hugetlbfs": true, "fusectl": true, "binfmt_misc": true, "autofs": true,
	"squashfs": true,
}

// collectDisks returns refreshed values plus all currently eligible mounts.
// Passive mode refreshes an HDD only while business I/O is in flight. Active
// mode is an explicit user-approved operation that may wake sleeping disks.
func collectDisks(refreshNonRotational, allowWake bool, now time.Time) ([]metrics.DiskMetrics, map[string]bool) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, map[string]bool{}
	}
	defer f.Close()
	var result []metrics.DiskMetrics
	present := make(map[string]bool)
	s := bufio.NewScanner(f)
	for s.Scan() {
		before, after, ok := strings.Cut(s.Text(), " - ")
		if !ok {
			continue
		}
		left, right := strings.Fields(before), strings.Fields(after)
		if len(left) < 5 || len(right) < 2 || ignoredFilesystems[right[0]] {
			continue
		}
		mountPoint := unescapeMount(left[4])
		rotational, leaves, known := blockMedia(left[2])
		if !known {
			continue
		}
		present[mountPoint] = true
		disk := metrics.DiskMetrics{
			Device: unescapeMount(right[1]), MountPoint: mountPoint, Filesystem: right[0],
			MediaType: mediaType(rotational), PhysicalDevices: diskDeviceInfo(leaves),
		}
		shouldRefresh := shouldRefreshDisk(rotational, refreshNonRotational, allowWake, anyIOInFlight(leaves))
		if !shouldRefresh {
			result = append(result, disk)
			continue
		}
		var stat syscall.Statfs_t
		if syscall.Statfs(mountPoint, &stat) != nil || stat.Blocks == 0 {
			continue
		}
		blockSize := uint64(stat.Bsize)
		total, free, available := stat.Blocks*blockSize, stat.Bfree*blockSize, stat.Bavail*blockSize
		used, usage := total-free, 0.0
		if denominator := used + available; denominator > 0 {
			usage = float64(used) * 100 / float64(denominator)
		}
		mode := "passive"
		if allowWake {
			mode = "active"
		}
		disk.TotalBytes, disk.UsedBytes, disk.AvailableBytes, disk.UsagePercent = total, used, available, usage
		disk.CapacityAvailable, disk.CollectedAt, disk.CollectionMode = true, now, mode
		result = append(result, disk)
	}
	return result, present
}

func mediaType(rotational bool) string {
	if rotational {
		return "hdd"
	}
	return "ssd"
}

func diskDeviceInfo(leaves []string) []metrics.DiskDeviceInfo {
	result := make([]metrics.DiskDeviceInfo, 0, len(leaves))
	seen := make(map[string]bool)
	for _, leaf := range leaves {
		name := filepath.Base(leaf)
		if seen[name] {
			continue
		}
		seen[name] = true
		result = append(result, metrics.DiskDeviceInfo{
			Name: name, Model: readSysfsText(filepath.Join(leaf, "device", "model")),
			Vendor: readSysfsText(filepath.Join(leaf, "device", "vendor")), Rotational: readSysfsText(filepath.Join(leaf, "queue", "rotational")) == "1",
		})
	}
	return result
}

func readSysfsText(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func shouldRefreshDisk(rotational, refreshNonRotational, allowWake, ioInFlight bool) bool {
	if rotational {
		return allowWake || ioInFlight
	}
	return refreshNonRotational || allowWake
}

// blockMedia resolves mapper/RAID stacks down to physical leaves using only
// sysfs metadata. A mixed-media stack is treated as rotational.
func blockMedia(majorMinor string) (bool, []string, bool) {
	path, err := filepath.EvalSymlinks(filepath.Join("/sys/dev/block", majorMinor))
	if err != nil {
		return false, nil, false
	}
	return classifyBlock(path, map[string]bool{})
}

func classifyBlock(path string, seen map[string]bool) (bool, []string, bool) {
	if seen[path] {
		return false, nil, false
	}
	seen[path] = true
	slaves, err := os.ReadDir(filepath.Join(path, "slaves"))
	if err == nil && len(slaves) > 0 {
		rotational, leaves := false, []string{}
		for _, slave := range slaves {
			target, e := filepath.EvalSymlinks(filepath.Join(path, "slaves", slave.Name()))
			if e != nil {
				return false, nil, false
			}
			r, l, ok := classifyBlock(target, seen)
			if !ok {
				return false, nil, false
			}
			rotational = rotational || r
			leaves = append(leaves, l...)
		}
		return rotational, leaves, true
	}
	for candidate := path; strings.HasPrefix(candidate, "/sys/"); candidate = filepath.Dir(candidate) {
		data, err := os.ReadFile(filepath.Join(candidate, "queue", "rotational"))
		if err == nil {
			value, e := strconv.Atoi(strings.TrimSpace(string(data)))
			if e != nil || (value != 0 && value != 1) {
				return false, nil, false
			}
			return value == 1, []string{candidate}, true
		}
		if candidate == "/sys" {
			break
		}
	}
	return false, nil, false
}

func anyIOInFlight(leaves []string) bool {
	for _, leaf := range leaves {
		data, err := os.ReadFile(filepath.Join(leaf, "stat"))
		if err != nil {
			continue
		}
		if ioInFlight(data) {
			return true
		}
	}
	return false
}

func ioInFlight(data []byte) bool {
	fields := strings.Fields(string(data))
	if len(fields) < 9 {
		return false
	}
	value, err := strconv.ParseUint(fields[8], 10, 64)
	return err == nil && value > 0
}

func unescapeMount(value string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(value)
}

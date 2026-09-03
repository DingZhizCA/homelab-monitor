/*
Package agent implements Linux memory collection.

本文件负责解析 /proc/meminfo，并使用 MemAvailable 语义计算可用量和使用率。
本文件不负责采样调度、历史记录或内存压力诊断。

数据链路：/proc/meminfo -> MemoryMetrics -> Collector
*/
package agent

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

func collectMemory() (metrics.MemoryMetrics, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return metrics.MemoryMetrics{}, fmt.Errorf("read memory: %w", err)
	}
	defer f.Close()
	values := map[string]uint64{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		n, e := strconv.ParseUint(fields[1], 10, 64)
		if e == nil && n <= ^uint64(0)/1024 {
			values[strings.TrimSuffix(fields[0], ":")] = n * 1024
		}
	}
	if err := scanner.Err(); err != nil {
		return metrics.MemoryMetrics{}, fmt.Errorf("read memory: %w", err)
	}
	total := values["MemTotal"]
	if total == 0 {
		return metrics.MemoryMetrics{}, fmt.Errorf("memory: MemTotal missing")
	}
	available, ok := values["MemAvailable"]
	if !ok {
		available = values["MemFree"] + values["Buffers"] + values["Cached"] + values["SReclaimable"]
		if shmem := values["Shmem"]; available > shmem {
			available -= shmem
		} else {
			available = 0
		}
	}
	if available > total {
		available = total
	}
	used := total - available
	return metrics.MemoryMetrics{TotalBytes: total, UsedBytes: used, AvailableBytes: available, UsagePercent: float64(used) * 100 / float64(total)}, nil
}

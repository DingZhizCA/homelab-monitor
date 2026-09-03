package agent

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

func collectSystem() (metrics.SystemMetrics, error) {
	uptimeData, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return metrics.SystemMetrics{}, fmt.Errorf("read uptime: %w", err)
	}
	uptime, err := strconv.ParseFloat(strings.Fields(string(uptimeData))[0], 64)
	if err != nil || uptime < 0 {
		return metrics.SystemMetrics{}, fmt.Errorf("parse uptime")
	}
	loadData, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return metrics.SystemMetrics{}, fmt.Errorf("read load average: %w", err)
	}
	fields := strings.Fields(string(loadData))
	if len(fields) < 3 {
		return metrics.SystemMetrics{}, fmt.Errorf("parse load average: too few fields")
	}
	loads := [3]float64{}
	for i := range loads {
		loads[i], err = strconv.ParseFloat(fields[i], 64)
		if err != nil {
			return metrics.SystemMetrics{}, fmt.Errorf("parse load average: %w", err)
		}
	}
	return metrics.SystemMetrics{UptimeSeconds: uint64(uptime), Load1: loads[0], Load5: loads[1], Load15: loads[2]}, nil
}

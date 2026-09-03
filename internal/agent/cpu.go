/*
Package agent implements Linux CPU usage and sensor collection.

本文件负责从 /proc 计算 CPU 时间差，并从 /sys 或 /proc 获取频率和温度。
本文件不决定采样周期，也不负责缓存、上报或 Dashboard 展示。

数据链路：/proc and /sys -> CPUMetrics -> Collector
*/
package agent

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

type cpuTimes struct{ total, idle uint64 }

func collectCPU(previous *cpuTimes, logicalCores int, frequency, temperature *float64, sensorCollectedAt *time.Time) (metrics.CPUMetrics, cpuTimes, error) {
	current, err := readCPUTimes()
	if err != nil {
		return metrics.CPUMetrics{}, cpuTimes{}, err
	}
	usage := 0.0
	if previous != nil && current.total > previous.total && current.idle >= previous.idle {
		total, idle := current.total-previous.total, current.idle-previous.idle
		if idle <= total {
			usage = float64(total-idle) * 100 / float64(total)
		}
	}
	return metrics.CPUMetrics{UsagePercent: usage, LogicalCores: logicalCores, FrequencyMHz: frequency, TemperatureCelsius: temperature, SensorCollectedAt: sensorCollectedAt}, current, nil
}

func readCPUTimes() (cpuTimes, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return cpuTimes{}, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	if !s.Scan() {
		return cpuTimes{}, s.Err()
	}
	fields := strings.Fields(s.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuTimes{}, os.ErrInvalid
	}
	var result cpuTimes
	for i, field := range fields[1:] {
		n, e := strconv.ParseUint(field, 10, 64)
		if e != nil {
			return cpuTimes{}, e
		}
		result.total += n
		if i == 3 || i == 4 {
			result.idle += n
		}
	}
	return result, nil
}

func cpuFrequency() *float64 {
	paths, _ := filepath.Glob("/sys/devices/system/cpu/cpu[0-9]*/cpufreq/scaling_cur_freq")
	var sum float64
	var count int
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
		if err == nil && value > 0 {
			sum += value / 1000
			count++
		}
	}
	if count > 0 {
		value := sum / float64(count)
		return &value
	}
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == "cpu MHz" {
			value, e := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
			if e == nil && value > 0 {
				sum += value
				count++
			}
		}
	}
	if count == 0 {
		return nil
	}
	value := sum / float64(count)
	return &value
}

func cpuTemperature() *float64 {
	hwmons, _ := filepath.Glob("/sys/class/hwmon/hwmon*")
	priorities := []string{"coretemp", "k10temp", "zenpower", "cpu_thermal"}
	for _, wanted := range priorities {
		for _, dir := range hwmons {
			name, err := os.ReadFile(filepath.Join(dir, "name"))
			if err != nil || strings.TrimSpace(string(name)) != wanted {
				continue
			}
			if value := temperatureIn(dir); value != nil {
				return value
			}
		}
	}
	return nil
}

func temperatureIn(dir string) *float64 {
	paths, _ := filepath.Glob(filepath.Join(dir, "temp*_input"))
	var best *float64
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		raw, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
		value := raw / 1000
		if err == nil && value >= -20 && value <= 150 && (best == nil || value > *best) {
			v := value
			best = &v
		}
	}
	return best
}

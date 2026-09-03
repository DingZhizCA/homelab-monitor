/*
Command homelab-agent runs the collection and reporting loop.

本文件负责加载配置、组装 Collector 与 Sender、根据 Server 返回的页面活跃状态切换
采样周期，并处理退出信号。本文件不实现具体 Linux 指标算法或 Server 存储。

数据链路：agent.yaml -> Collector -> MetricEnvelope -> HTTP Server
*/
package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"syscall"
	"time"

	agentpkg "github.com/DingZhizCA/homelab-monitor/internal/agent"
	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

const errorLogInterval = time.Minute

type errorLogLimiter struct {
	lastCollect  time.Time
	lastValidate time.Time
	lastSend     time.Time
}

func main() {
	configPath := flag.String("config", "/etc/homelab-monitor/agent.yaml", "path to agent YAML configuration")
	flag.Parse()
	config, err := agentpkg.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("component=agent level=ERROR msg=%q", err)
	}
	collector := agentpkg.NewCollector(agentpkg.Options{
		Disk:           enabled(config.Collectors.Disk),
		Network:        enabled(config.Collectors.Network),
		PowerState:     config.Collectors.PowerState != nil && *config.Collectors.PowerState,
		SensorInterval: config.Collectors.SensorInterval.Duration,
		DiskInterval:   config.Collectors.DiskInterval.Duration,
		PowerInterval:  config.Collectors.PowerInterval.Duration,
	})
	sender := agentpkg.NewSender(config.Server.URL, config.Server.Token)
	device := metrics.DeviceInfo{ID: config.Agent.DeviceID, DisplayName: config.Agent.DisplayName}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	mode := "idle"
	interval := config.Monitoring.IdleInterval.Duration
	var activeUntil time.Time
	log.Printf("component=agent level=INFO device=%q mode=%q interval=%q msg=%q", device.ID, mode, interval, "started")
	limiter := &errorLogLimiter{}
	for {
		decision := sample(ctx, collector, sender, device, limiter, mode, interval)
		requestedMode := decision.SamplingMode
		if requestedMode == "active" {
			activeUntil = time.Now().Add(30 * time.Minute)
			if decision.ActiveUntil != nil {
				activeUntil = *decision.ActiveUntil
			}
		}
		if requestedMode == "" && mode == "active" && !activeUntil.IsZero() && time.Now().After(activeUntil) {
			requestedMode = "idle"
		}
		if requestedMode != mode && (requestedMode == "active" || requestedMode == "idle") {
			mode = requestedMode
			if mode == "active" {
				interval = config.Monitoring.ActiveInterval.Duration
			} else {
				interval = config.Monitoring.IdleInterval.Duration
			}
			log.Printf("component=agent level=INFO device=%q mode=%q interval=%q msg=%q", device.ID, mode, interval, "sampling mode changed")
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			log.Print("component=agent level=INFO msg=stopped")
			return
		case <-timer.C:
		}
	}
}

func sample(ctx context.Context, collector *agentpkg.Collector, sender *agentpkg.Sender, device metrics.DeviceInfo, limiter *errorLogLimiter, mode string, interval time.Duration) agentpkg.SendResult {
	snapshot, err := collector.Collect(device)
	if err != nil {
		if time.Since(limiter.lastCollect) >= errorLogInterval {
			log.Printf("component=agent level=WARN device=%q msg=%q", device.ID, err)
			limiter.lastCollect = time.Now()
		}
		return agentpkg.SendResult{}
	}
	snapshot.SamplingMode = mode
	snapshot.SamplingIntervalSeconds = uint64(interval / time.Second)
	if err := snapshot.Validate(); err != nil {
		if time.Since(limiter.lastValidate) >= errorLogInterval {
			log.Printf("component=agent level=ERROR device=%q msg=%q", device.ID, err)
			limiter.lastValidate = time.Now()
		}
		return agentpkg.SendResult{}
	}
	result, err := sender.Send(ctx, snapshot)
	if err != nil {
		if time.Since(limiter.lastSend) >= errorLogInterval {
			log.Printf("component=agent level=WARN device=%q msg=%q", device.ID, err)
			limiter.lastSend = time.Now()
		}
		return agentpkg.SendResult{}
	}
	if result.RefreshDisk {
		activeSnapshot, err := collector.CollectWithDiskRefresh(device)
		if err != nil {
			log.Printf("component=agent level=WARN device=%q msg=%q", device.ID, err)
			return result
		}
		activeSnapshot.SamplingMode = mode
		activeSnapshot.SamplingIntervalSeconds = uint64(interval / time.Second)
		activeSnapshot.RefreshRequestID = result.RefreshRequestID
		if !hasActiveDisk(activeSnapshot) {
			activeSnapshot.RefreshError = "no eligible disk metrics were refreshed"
		}
		if err := activeSnapshot.Validate(); err != nil {
			log.Printf("component=agent level=ERROR device=%q msg=%q", device.ID, err)
			return result
		}
		if _, err := sender.Send(ctx, activeSnapshot); err != nil {
			log.Printf("component=agent level=WARN device=%q msg=%q", device.ID, err)
		}
	}
	return result
}

func hasActiveDisk(snapshot metrics.MetricEnvelope) bool {
	for _, disk := range snapshot.Disks {
		if disk.CollectionMode == "active" {
			return true
		}
	}
	return false
}

func enabled(value *bool) bool { return value == nil || *value }

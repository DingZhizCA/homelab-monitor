/*
Command homelab-agent runs the collection and reporting loop.

本文件负责加载配置、组装 Collector 与单次/批量 Sender、按页面活跃状态切换指标采样周期，
并在显式启用时启动独立的插件控制轮询。本文件不实现具体指标算法或 Server 存储。

数据链路：agent.yaml -> Collector -> MetricEnvelope -> HTTP Server；plugins -> HTTPS control plane
*/
package main

import (
	"context"
	"flag"
	"fmt"
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

type metricsReporter interface {
	Send(context.Context, metrics.MetricEnvelope) (agentpkg.SendResult, error)
}

// batchReporter retains unacknowledged snapshots in the explicitly bounded
// durable queue. A successful response only removes the exact pending batch.
type batchReporter struct {
	queue  *agentpkg.BatchQueue
	sender *agentpkg.Sender
}

func (r *batchReporter) Send(ctx context.Context, snapshot metrics.MetricEnvelope) (agentpkg.SendResult, error) {
	if err := r.queue.Append(snapshot); err != nil {
		return agentpkg.SendResult{}, fmt.Errorf("queue metrics snapshot: %w", err)
	}
	result, sent, err := r.queue.SendPending(ctx, r.sender)
	if err != nil {
		return agentpkg.SendResult{}, fmt.Errorf("send pending metrics batch: %w", err)
	}
	if !sent {
		return agentpkg.SendResult{}, nil
	}
	return result, nil
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
	var reporter metricsReporter = sender
	if config.MetricsBatch.Enabled {
		queue, err := agentpkg.OpenBatchQueue(
			config.MetricsBatch.StateDirectory,
			config.Agent.DeviceID,
			config.MetricsBatch.MaxQueuedSnapshots,
			config.MetricsBatch.MaxQueuedBytes,
			config.MetricsBatch.MaxBatchSnapshots,
			config.MetricsBatch.MaxBodyBytes,
		)
		if err != nil {
			log.Fatalf("component=agent level=ERROR msg=%q", err)
		}
		defer func() {
			if err := queue.Close(); err != nil {
				log.Printf("component=agent level=ERROR msg=%q", err)
			}
		}()
		reporter = &batchReporter{queue: queue, sender: sender}
	}
	device := metrics.DeviceInfo{ID: config.Agent.DeviceID, DisplayName: config.Agent.DisplayName}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var pluginRuntime *agentpkg.PluginRuntime
	if config.Plugins.Enabled {
		pluginRuntime, err = agentpkg.NewPluginRuntime(agentpkg.PluginRuntimeConfig{
			ServerURL: config.Server.URL, DeviceID: config.Agent.DeviceID, DeviceToken: config.Server.Token,
			PublisherKeyPath: config.Plugins.PublisherKeyPath, PermissionFile: config.Plugins.PermissionFile,
			StagingDirectory: config.Plugins.StagingDirectory, RuntimeDirectory: config.Plugins.RuntimeDirectory,
			HostProtocolMajor: config.Plugins.HostProtocolMajor, MaxArtifactBytes: config.Plugins.MaxArtifactBytes,
			OperationTimeout:       config.Plugins.OperationTimeout.Duration,
			MaxConsecutiveFailures: config.Plugins.MaxConsecutiveFailures,
			InitialFailureBackoff:  config.Plugins.InitialFailureBackoff.Duration,
			MaxFailureBackoff:      config.Plugins.MaxFailureBackoff.Duration,
		})
		if err != nil {
			log.Fatalf("component=agent level=ERROR msg=%q", err)
		}
		go pluginControlLoop(ctx, pluginRuntime, config.Plugins.PollInterval.Duration, config.Plugins.OperationTimeout.Duration)
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), config.Plugins.OperationTimeout.Duration)
			defer cancel()
			if err := pluginRuntime.Close(shutdownCtx); err != nil {
				log.Printf("component=agent level=ERROR msg=%q", err)
			}
		}()
	}
	mode := "idle"
	interval := config.Monitoring.IdleInterval.Duration
	var activeUntil time.Time
	log.Printf("component=agent level=INFO device=%q mode=%q interval=%q msg=%q", device.ID, mode, interval, "started")
	limiter := &errorLogLimiter{}
	for {
		decision := sample(ctx, collector, reporter, device, limiter, mode, interval)
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

type pluginControlRunner interface {
	ReconcileOnce(context.Context) error
}

func pluginControlLoop(ctx context.Context, runtime pluginControlRunner, pollInterval, operationTimeout time.Duration) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	var lastError time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		operationCtx, cancel := context.WithTimeout(ctx, operationTimeout)
		err := runtime.ReconcileOnce(operationCtx)
		cancel()
		if err != nil && (lastError.IsZero() || time.Since(lastError) >= errorLogInterval) {
			log.Printf("component=agent-plugin level=WARN msg=%q", err)
			lastError = time.Now()
		}
		timer.Reset(pollInterval)
	}
}

func sample(ctx context.Context, collector *agentpkg.Collector, reporter metricsReporter, device metrics.DeviceInfo, limiter *errorLogLimiter, mode string, interval time.Duration) agentpkg.SendResult {
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
	result, err := reporter.Send(ctx, snapshot)
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
		if _, err := reporter.Send(ctx, activeSnapshot); err != nil {
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

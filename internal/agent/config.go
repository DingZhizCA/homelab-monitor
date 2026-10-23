/*
Package agent defines and validates homelab-agent configuration.

本文件负责解析 YAML、补齐兼容采样默认值，并验证采集、可选批量上报与插件控制参数。
本文件不启动采集器、不访问硬件，也不向 Server 发送数据。

数据链路：agent.yaml -> Config -> cmd/agent -> Collector and Sender
*/
package agent

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration wraps time.Duration with YAML text parsing such as "5s" or "1m".
// Duration 让配置文件使用人类可读的时间字符串，而不是容易混淆的整数单位。
type Duration struct{ time.Duration }

// UnmarshalYAML parses a YAML scalar using Go's duration syntax.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	value, err := time.ParseDuration(node.Value)
	if err != nil {
		return err
	}
	d.Duration = value
	return nil
}

// Config is the validated runtime configuration shared by Agent components.
// Config 是 Agent 各组件共用的、完成默认值处理和校验后的运行配置。
type Config struct {
	Server struct {
		URL   string `yaml:"url"`
		Token string `yaml:"token"`
	} `yaml:"server"`
	Agent struct {
		DeviceID    string   `yaml:"device_id"`
		DisplayName string   `yaml:"display_name"`
		Interval    Duration `yaml:"interval"`
	} `yaml:"agent"`
	Monitoring struct {
		IdleInterval   Duration `yaml:"idle_interval"`
		ActiveInterval Duration `yaml:"active_interval"`
	} `yaml:"monitoring"`
	MetricsBatch struct {
		Enabled            bool   `yaml:"enabled"`
		StateDirectory     string `yaml:"state_directory"`
		MaxQueuedSnapshots int    `yaml:"max_queued_snapshots"`
		MaxQueuedBytes     int64  `yaml:"max_queued_bytes"`
		MaxBatchSnapshots  int    `yaml:"max_batch_snapshots"`
		MaxBodyBytes       int64  `yaml:"max_body_bytes"`
	} `yaml:"metrics_batch"`
	PluginMetrics struct {
		Enabled               bool     `yaml:"enabled"`
		StateDirectory        string   `yaml:"state_directory"`
		TickInterval          Duration `yaml:"tick_interval"`
		TickTimeout           Duration `yaml:"tick_timeout"`
		MaxPolicies           int      `yaml:"max_policies"`
		MaxTasksPerRun        int      `yaml:"max_tasks_per_run"`
		MaxConcurrent         int      `yaml:"max_concurrent"`
		MaxQueuedObservations int      `yaml:"max_queued_observations"`
		MaxQueuedSamples      int      `yaml:"max_queued_samples"`
		MaxQueuedBytes        int64    `yaml:"max_queued_bytes"`
		MaxBatchObservations  int      `yaml:"max_batch_observations"`
		MaxBatchSamples       int      `yaml:"max_batch_samples"`
		MaxBodyBytes          int64    `yaml:"max_body_bytes"`
	} `yaml:"plugin_metrics"`
	Collectors struct {
		CPU     *bool `yaml:"cpu"`
		Memory  *bool `yaml:"memory"`
		Disk    *bool `yaml:"disk"`
		Network *bool `yaml:"network"`
		// PowerState is opt-in because ATA commands cannot be assumed safe for
		// every USB bridge, RAID controller, or virtual disk.
		PowerState     *bool    `yaml:"power_state"`
		SensorInterval Duration `yaml:"sensor_interval"`
		DiskInterval   Duration `yaml:"disk_interval"`
		// PowerInterval is the minimum time between power-state query attempts.
		PowerInterval Duration `yaml:"power_state_interval"`
	} `yaml:"collectors"`
	Plugins struct {
		Enabled                bool     `yaml:"enabled"`
		PublisherKeyPath       string   `yaml:"publisher_key_path"`
		PermissionFile         string   `yaml:"permission_file"`
		StagingDirectory       string   `yaml:"staging_directory"`
		RuntimeDirectory       string   `yaml:"runtime_directory"`
		StateDirectory         string   `yaml:"state_directory"`
		LauncherPath           string   `yaml:"launcher_path"`
		HostProtocolMajor      uint16   `yaml:"host_protocol_major"`
		MaxOpenFiles           uint64   `yaml:"max_open_files"`
		MaxProcesses           uint64   `yaml:"max_processes"`
		MaxFileBytes           uint64   `yaml:"max_file_bytes"`
		MaxAddressSpaceBytes   uint64   `yaml:"max_address_space_bytes"`
		MaxCPUSeconds          uint64   `yaml:"max_cpu_seconds"`
		MaxArtifactBytes       int64    `yaml:"max_artifact_bytes"`
		MaxStagingBytes        int64    `yaml:"max_staging_bytes"`
		MaxStagedPackages      int      `yaml:"max_staged_packages"`
		MaxStagingScanEntries  int      `yaml:"max_staging_scan_entries"`
		StagingCleanupInterval Duration `yaml:"staging_cleanup_interval"`
		StagingOrphanGrace     Duration `yaml:"staging_orphan_grace"`
		StagingCleanupLimit    int      `yaml:"staging_cleanup_delete_limit"`
		StagingCleanupTimeout  Duration `yaml:"staging_cleanup_timeout"`
		MaxStateEntries        int      `yaml:"max_state_entries"`
		MaxStateBytes          int64    `yaml:"max_state_bytes"`
		PollInterval           Duration `yaml:"poll_interval"`
		OperationTimeout       Duration `yaml:"operation_timeout"`
		MaxConsecutiveFailures uint32   `yaml:"max_consecutive_failures"`
		InitialFailureBackoff  Duration `yaml:"initial_failure_backoff"`
		MaxFailureBackoff      Duration `yaml:"max_failure_backoff"`
	} `yaml:"plugins"`
}

// LoadConfig decodes a YAML file, applies defaults, and validates the result.
// LoadConfig 读取 YAML 配置、应用默认值，并在返回前完成一致性校验。
func LoadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config: %w", err)
	}
	defer f.Close()
	var config Config
	decoder := yaml.NewDecoder(f)
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if config.Agent.Interval.Duration == 0 {
		config.Agent.Interval.Duration = 5 * time.Second
	}
	if config.Monitoring.ActiveInterval.Duration == 0 {
		config.Monitoring.ActiveInterval.Duration = config.Agent.Interval.Duration
	}
	if config.Monitoring.IdleInterval.Duration == 0 {
		config.Monitoring.IdleInterval.Duration = time.Minute
	}
	if config.Collectors.SensorInterval.Duration == 0 {
		config.Collectors.SensorInterval.Duration = time.Minute
	}
	if config.Collectors.DiskInterval.Duration == 0 {
		config.Collectors.DiskInterval.Duration = 5 * time.Minute
	}
	if config.Collectors.PowerInterval.Duration == 0 {
		config.Collectors.PowerInterval.Duration = time.Minute
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

// Validate checks required identity, endpoint, collector, and cadence invariants.
// Validate 校验设备身份、Server 地址、必需采集器和采样周期约束。
func (c Config) Validate() error {
	parsed, err := url.Parse(c.Server.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("server.url must be an absolute HTTP(S) URL")
	}
	if c.Server.Token != "" && parsed.Scheme != "https" {
		return fmt.Errorf("server.url must use HTTPS when server.token is configured")
	}
	if strings.TrimSpace(c.Agent.DeviceID) == "" || strings.TrimSpace(c.Agent.DeviceID) != c.Agent.DeviceID {
		return fmt.Errorf("agent.device_id is required and must not have surrounding whitespace")
	}
	if strings.TrimSpace(c.Agent.DisplayName) == "" {
		return fmt.Errorf("agent.display_name is required")
	}
	if c.Agent.Interval.Duration <= 0 {
		return fmt.Errorf("agent.interval must be greater than zero")
	}
	if c.Monitoring.ActiveInterval.Duration < time.Second || c.Monitoring.IdleInterval.Duration < c.Monitoring.ActiveInterval.Duration {
		return fmt.Errorf("monitoring intervals must be positive and idle_interval must not be shorter than active_interval")
	}
	if c.MetricsBatch.Enabled {
		if parsed.Scheme != "https" || strings.TrimSpace(c.Server.Token) == "" {
			return fmt.Errorf("metrics_batch.enabled requires HTTPS and a device token")
		}
		if !filepath.IsAbs(c.MetricsBatch.StateDirectory) {
			return fmt.Errorf("metrics_batch.state_directory must be an absolute path")
		}
		if c.MetricsBatch.MaxQueuedSnapshots < 1 || c.MetricsBatch.MaxQueuedBytes < 1 || c.MetricsBatch.MaxBatchSnapshots < 1 || c.MetricsBatch.MaxBodyBytes < 1 {
			return fmt.Errorf("metrics_batch requires an absolute state directory and explicit positive queue count, queue byte, batch count, and body limits")
		}
		if c.MetricsBatch.MaxBatchSnapshots > c.MetricsBatch.MaxQueuedSnapshots {
			return fmt.Errorf("metrics_batch.max_batch_snapshots cannot exceed max_queued_snapshots")
		}
	}
	if c.PluginMetrics.Enabled {
		if !c.Plugins.Enabled {
			return fmt.Errorf("plugin_metrics.enabled requires plugins.enabled")
		}
		if !filepath.IsAbs(c.PluginMetrics.StateDirectory) {
			return fmt.Errorf("plugin_metrics.state_directory must be an absolute path")
		}
		if c.PluginMetrics.TickInterval.Duration <= 0 || c.PluginMetrics.TickTimeout.Duration <= 0 || c.PluginMetrics.MaxPolicies < 1 || c.PluginMetrics.MaxTasksPerRun < 1 || c.PluginMetrics.MaxTasksPerRun > c.PluginMetrics.MaxPolicies || c.PluginMetrics.MaxConcurrent < 1 || c.PluginMetrics.MaxConcurrent > c.PluginMetrics.MaxTasksPerRun || c.PluginMetrics.MaxQueuedObservations < 1 || c.PluginMetrics.MaxQueuedSamples < 1 || c.PluginMetrics.MaxQueuedBytes < 1 || c.PluginMetrics.MaxBatchObservations < 1 || c.PluginMetrics.MaxBatchObservations > c.PluginMetrics.MaxQueuedObservations || c.PluginMetrics.MaxBatchSamples < 1 || c.PluginMetrics.MaxBatchSamples > c.PluginMetrics.MaxQueuedSamples || c.PluginMetrics.MaxBodyBytes < 1 {
			return fmt.Errorf("plugin_metrics requires explicit positive scheduling, policy, concurrency, queue, batch, and body limits")
		}
	}
	if !enabled(c.Collectors.CPU) || !enabled(c.Collectors.Memory) {
		return fmt.Errorf("CPU and memory collectors are required in phase 1")
	}
	if c.Collectors.SensorInterval.Duration < c.Monitoring.ActiveInterval.Duration {
		return fmt.Errorf("collectors.sensor_interval must not be shorter than monitoring.active_interval")
	}
	if c.Collectors.DiskInterval.Duration < c.Monitoring.ActiveInterval.Duration {
		return fmt.Errorf("collectors.disk_interval must not be shorter than monitoring.active_interval")
	}
	if c.Collectors.PowerInterval.Duration < c.Monitoring.ActiveInterval.Duration {
		return fmt.Errorf("collectors.power_state_interval must not be shorter than monitoring.active_interval")
	}
	if c.Plugins.Enabled {
		if parsed.Scheme != "https" || strings.TrimSpace(c.Server.Token) == "" {
			return fmt.Errorf("plugins.enabled requires HTTPS and a device token")
		}
		for _, entry := range []struct{ name, path string }{
			{"publisher_key_path", c.Plugins.PublisherKeyPath},
			{"permission_file", c.Plugins.PermissionFile},
			{"staging_directory", c.Plugins.StagingDirectory},
			{"runtime_directory", c.Plugins.RuntimeDirectory},
			{"state_directory", c.Plugins.StateDirectory},
			{"launcher_path", c.Plugins.LauncherPath},
		} {
			if !filepath.IsAbs(entry.path) {
				return fmt.Errorf("plugins.%s must be an absolute path", entry.name)
			}
		}
		if c.Plugins.HostProtocolMajor == 0 || c.Plugins.MaxOpenFiles < 1 || c.Plugins.MaxProcesses < 1 || c.Plugins.MaxFileBytes < 1 || c.Plugins.MaxAddressSpaceBytes < 1 || c.Plugins.MaxCPUSeconds < 1 || c.Plugins.MaxArtifactBytes < 1 || c.Plugins.MaxStagingBytes < c.Plugins.MaxArtifactBytes || c.Plugins.MaxStagedPackages < 1 || c.Plugins.MaxStagingScanEntries < 1 || c.Plugins.MaxStagedPackages > (c.Plugins.MaxStagingScanEntries-1)/8 || c.Plugins.StagingCleanupInterval.Duration <= 0 || c.Plugins.StagingOrphanGrace.Duration <= 0 || c.Plugins.StagingCleanupLimit < 1 || c.Plugins.StagingCleanupTimeout.Duration <= 0 || c.Plugins.MaxStateEntries < 1 || c.Plugins.MaxStateBytes < 1 || c.Plugins.PollInterval.Duration <= 0 || c.Plugins.OperationTimeout.Duration <= 0 {
			return fmt.Errorf("plugins requires explicit protocol, process, staging capacity and cleanup, local state, poll interval, and operation limits")
		}
		if c.Plugins.MaxConsecutiveFailures == 0 || c.Plugins.InitialFailureBackoff.Duration <= 0 || c.Plugins.MaxFailureBackoff.Duration < c.Plugins.InitialFailureBackoff.Duration {
			return fmt.Errorf("plugins requires an explicit positive failure limit and ordered failure backoffs")
		}
	}
	return nil
}

func enabled(value *bool) bool { return value == nil || *value }

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/testutil"
)

func TestLoadConfigDefaults(t *testing.T) {
	path := filepath.Join(testutil.TempDir(t), "agent.yaml")
	data := []byte("server:\n  url: http://127.0.0.1:8080\nagent:\n  device_id: n1\n  display_name: N1\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Monitoring.IdleInterval.Duration != time.Minute || config.Monitoring.ActiveInterval.Duration != 5*time.Second {
		t.Fatalf("unexpected intervals: %+v", config.Monitoring)
	}
	if config.Plugins.Enabled {
		t.Fatal("plugin runtime must remain disabled when not explicitly configured")
	}
	if config.MetricsBatch.Enabled {
		t.Fatal("metrics batching must remain disabled when not explicitly configured")
	}
}

func TestExampleAgentConfigLoadsWithPluginControlDisabled(t *testing.T) {
	config, err := LoadConfig(filepath.Join("..", "..", "configs", "agent.example.yaml"))
	if err != nil {
		t.Fatalf("load example Agent config: %v", err)
	}
	if config.Plugins.Enabled {
		t.Fatal("example config must leave plugin control disabled")
	}
	if config.MetricsBatch.Enabled {
		t.Fatal("example config must leave metrics batching disabled")
	}
}

func TestLoadConfigRequiresExplicitSecureMetricsBatchSettings(t *testing.T) {
	directory := testutil.TempDir(t)
	base := "server:\n  url: https://server.example\n  token: device-token\nagent:\n  device_id: n1\n  display_name: N1\nmetrics_batch:\n  enabled: true\n  state_directory: " + filepath.Join(directory, "metrics-queue") + "\n  max_queued_snapshots: 8\n  max_queued_bytes: 65536\n  max_batch_snapshots: 4\n  max_body_bytes: 32768\n"
	path := filepath.Join(directory, "agent.yaml")
	if err := os.WriteFile(path, []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err != nil {
		t.Fatalf("complete explicit metrics batch settings rejected: %v", err)
	}
	for _, invalid := range []string{
		strings.Replace(base, "https://server.example", "http://server.example", 1),
		strings.Replace(base, "  token: device-token\n", "", 1),
		strings.Replace(base, "  state_directory: "+filepath.Join(directory, "metrics-queue")+"\n", "", 1),
		strings.Replace(base, "  state_directory: "+filepath.Join(directory, "metrics-queue")+"\n", "  state_directory: relative\n", 1),
		strings.Replace(base, "  max_queued_snapshots: 8\n", "", 1),
		strings.Replace(base, "  max_queued_bytes: 65536\n", "", 1),
		strings.Replace(base, "  max_batch_snapshots: 4\n", "", 1),
		strings.Replace(base, "  max_body_bytes: 32768\n", "", 1),
		strings.Replace(base, "  max_batch_snapshots: 4\n", "  max_batch_snapshots: 9\n", 1),
	} {
		if err := os.WriteFile(path, []byte(invalid), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil {
			t.Fatal("incomplete or insecure metrics batch settings were accepted")
		}
	}
}

func TestLoadConfigRequiresExplicitSecurePluginRuntimeSettings(t *testing.T) {
	directory := testutil.TempDir(t)
	base := "server:\n  url: https://server.example\n  token: device-token\nagent:\n  device_id: n1\n  display_name: N1\nplugins:\n  enabled: true\n  publisher_key_path: " + filepath.Join(directory, "publisher.pub") + "\n  permission_file: " + filepath.Join(directory, "grants.json") + "\n  staging_directory: " + filepath.Join(directory, "staging") + "\n  runtime_directory: " + filepath.Join(directory, "runtime") + "\n  state_directory: " + filepath.Join(directory, "state") + "\n  launcher_path: " + filepath.Join(directory, "plugin-launcher") + "\n  host_protocol_major: 1\n  max_open_files: 64\n  max_processes: 32\n  max_file_bytes: 1048576\n  max_address_space_bytes: 1073741824\n  max_cpu_seconds: 30\n  max_artifact_bytes: 1048576\n  max_staging_bytes: 16777216\n  max_staged_packages: 16\n  max_staging_scan_entries: 256\n  staging_cleanup_interval: 1h\n  staging_orphan_grace: 24h\n  staging_cleanup_delete_limit: 4\n  staging_cleanup_timeout: 30s\n  max_state_entries: 16\n  max_state_bytes: 1048576\n  poll_interval: 1m\n  operation_timeout: 30s\n  max_consecutive_failures: 5\n  initial_failure_backoff: 1s\n  max_failure_backoff: 5m\n"
	path := filepath.Join(directory, "agent.yaml")
	if err := os.WriteFile(path, []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err != nil {
		t.Fatalf("complete explicit plugin settings rejected: %v", err)
	}
	for _, invalid := range []string{
		"server:\n  url: http://server.example\n  token: device-token\n" + strings.TrimPrefix(base, "server:\n  url: https://server.example\n  token: device-token\n"),
		strings.Replace(base, "  poll_interval: 1m\n", "", 1),
		strings.Replace(base, "  max_consecutive_failures: 5\n", "", 1),
		strings.Replace(base, "  initial_failure_backoff: 1s\n", "", 1),
		strings.Replace(base, "  max_failure_backoff: 5m\n", "", 1),
		strings.Replace(base, "  launcher_path: "+filepath.Join(directory, "plugin-launcher")+"\n", "", 1),
		strings.Replace(base, "  max_open_files: 64\n", "", 1),
		strings.Replace(base, "  max_processes: 32\n", "", 1),
		strings.Replace(base, "  max_file_bytes: 1048576\n", "", 1),
		strings.Replace(base, "  max_address_space_bytes: 1073741824\n", "", 1),
		strings.Replace(base, "  max_cpu_seconds: 30\n", "", 1),
		strings.Replace(base, "  max_staging_bytes: 16777216\n", "", 1),
		strings.Replace(base, "  max_staged_packages: 16\n", "", 1),
		strings.Replace(base, "  max_staging_scan_entries: 256\n", "", 1),
		strings.Replace(base, "  staging_cleanup_interval: 1h\n", "", 1),
		strings.Replace(base, "  staging_orphan_grace: 24h\n", "", 1),
		strings.Replace(base, "  staging_cleanup_delete_limit: 4\n", "", 1),
		strings.Replace(base, "  staging_cleanup_timeout: 30s\n", "", 1),
		strings.Replace(base, "  state_directory: "+filepath.Join(directory, "state")+"\n", "", 1),
		strings.Replace(base, "  max_state_entries: 16\n", "", 1),
		strings.Replace(base, "  max_state_bytes: 1048576\n", "", 1),
		strings.Replace(base, "  publisher_key_path: "+filepath.Join(directory, "publisher.pub")+"\n", "  publisher_key_path: relative.pub\n", 1),
		strings.Replace(base, "  state_directory: "+filepath.Join(directory, "state")+"\n", "  state_directory: relative-state\n", 1),
	} {
		if err := os.WriteFile(path, []byte(invalid), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil {
			t.Fatal("incomplete or insecure plugin settings were accepted")
		}
	}
}

func TestLoadConfigRequiresBoundedPluginMetricsWithPluginRuntime(t *testing.T) {
	directory := testutil.TempDir(t)
	base := "server:\n  url: https://server.example\n  token: device-token\nagent:\n  device_id: n1\n  display_name: N1\nplugin_metrics:\n  enabled: true\n  state_directory: " + filepath.Join(directory, "plugin-metrics") + "\n  tick_interval: 10s\n  tick_timeout: 30s\n  max_policies: 16\n  max_tasks_per_run: 4\n  max_concurrent: 2\n  max_queued_observations: 32\n  max_queued_samples: 128\n  max_queued_bytes: 1048576\n  max_batch_observations: 8\n  max_batch_samples: 32\n  max_body_bytes: 262144\nplugins:\n  enabled: false\n"
	path := filepath.Join(directory, "agent.yaml")
	if err := os.WriteFile(path, []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("plugin metrics without plugin runtime were accepted")
	}
	// The full plugin-runtime fixture is covered separately; this verifies that
	// collection-specific limits are all required once collection is selected.
	config := Config{}
	config.Server.URL = "https://server.example"
	config.Server.Token = "token"
	config.Agent.DeviceID = "n1"
	config.Agent.DisplayName = "N1"
	config.Agent.Interval.Duration = time.Second
	config.Monitoring.ActiveInterval.Duration = time.Second
	config.Monitoring.IdleInterval.Duration = time.Minute
	config.Collectors.CPU = boolPointer(true)
	config.Collectors.Memory = boolPointer(true)
	config.Collectors.SensorInterval.Duration = time.Minute
	config.Collectors.DiskInterval.Duration = time.Minute
	config.Collectors.PowerInterval.Duration = time.Minute
	config.Plugins.Enabled = true
	config.PluginMetrics.Enabled = true
	config.PluginMetrics.StateDirectory = filepath.Join(directory, "plugin-metrics")
	config.PluginMetrics.TickInterval.Duration = time.Second
	config.PluginMetrics.TickTimeout.Duration = time.Second
	config.PluginMetrics.MaxPolicies = 4
	config.PluginMetrics.MaxTasksPerRun = 2
	config.PluginMetrics.MaxConcurrent = 1
	config.PluginMetrics.MaxQueuedObservations = 4
	config.PluginMetrics.MaxQueuedSamples = 8
	config.PluginMetrics.MaxQueuedBytes = 4096
	config.PluginMetrics.MaxBatchObservations = 2
	config.PluginMetrics.MaxBatchSamples = 4
	config.PluginMetrics.MaxBodyBytes = 2048
	// Validation proceeds into the independently tested plugin settings only
	// after accepting this complete collection block.
	if err := config.Validate(); err == nil || strings.Contains(err.Error(), "plugin_metrics") {
		t.Fatalf("complete plugin metrics block error=%v", err)
	}
	config.PluginMetrics.MaxConcurrent = 3
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "plugin_metrics") {
		t.Fatalf("invalid plugin metrics concurrency error=%v", err)
	}
}

func boolPointer(value bool) *bool { return &value }

func TestLoadConfigRejectsDisabledRequiredCollector(t *testing.T) {
	path := filepath.Join(testutil.TempDir(t), "agent.yaml")
	data := []byte("server:\n  url: http://127.0.0.1:8080\nagent:\n  device_id: n1\n  display_name: N1\ncollectors:\n  cpu: false\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("disabled CPU collector was accepted")
	}
}

func TestLoadConfigRequiresHTTPSForDeviceToken(t *testing.T) {
	path := filepath.Join(testutil.TempDir(t), "agent.yaml")
	data := []byte("server:\n  url: http://127.0.0.1:8080\n  token: device-secret\nagent:\n  device_id: n1\n  display_name: N1\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("HTTP endpoint with a device token was accepted")
	}
}

func TestLoadConfigRejectsUntrimmedDeviceIdentity(t *testing.T) {
	path := filepath.Join(testutil.TempDir(t), "agent.yaml")
	data := []byte("server:\n  url: http://127.0.0.1:8080\nagent:\n  device_id: ' n1 '\n  display_name: N1\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("Agent config accepted a device ID with surrounding whitespace")
	}
}

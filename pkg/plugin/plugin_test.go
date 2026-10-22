/*
Package plugin_test verifies the public SDK against the internal gRPC host.
*/
package plugin_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/plugins"
	pluginSDK "github.com/DingZhizCA/homelab-monitor/pkg/plugin"
)

type sdkTestPlugin struct{}

func (sdkTestPlugin) Handshake(_ context.Context, identity pluginSDK.HostIdentity) (pluginSDK.HostCapabilities, error) {
	return pluginSDK.HostCapabilities{ProtocolMajor: identity.ProtocolMajor, HostProtocolRange: pluginSDK.ProtocolRange{Min: 1, Max: 1}}, nil
}

func (sdkTestPlugin) Describe(context.Context) ([]pluginSDK.CollectorDescriptor, error) {
	return []pluginSDK.CollectorDescriptor{{ID: "system", Metrics: []pluginSDK.MetricDescriptor{{ID: "load", Type: pluginSDK.MetricGauge, Unit: "count"}}}}, nil
}

func (sdkTestPlugin) ValidateConfig(_ context.Context, _ string, config json.RawMessage) error {
	if !json.Valid(config) {
		return errors.New("invalid config")
	}
	return nil
}

func (sdkTestPlugin) Collect(context.Context, pluginSDK.CollectRequest) (pluginSDK.CollectResult, error) {
	return pluginSDK.CollectResult{
		ObservedAt: time.Now(),
		Samples:    []pluginSDK.MetricSample{{MetricID: "load", Value: json.RawMessage(`0.5`), Quality: pluginSDK.QualityValid}},
	}, nil
}

func (sdkTestPlugin) Health(context.Context) (pluginSDK.HealthStatus, error) {
	return pluginSDK.HealthStatus{Healthy: true}, nil
}

func (sdkTestPlugin) Shutdown(context.Context) error { return nil }

func TestServeUnixImplementsThePluginContractAndStopsOnContext(t *testing.T) {
	runtimeDirectory := t.TempDir()
	if err := os.Chmod(runtimeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(runtimeDirectory, "plugin.sock")
	serverContext, cancelServer := context.WithCancel(context.Background())
	serverResult := make(chan error, 1)
	go func() { serverResult <- pluginSDK.ServeUnix(serverContext, socketPath, sdkTestPlugin{}) }()

	callContext, cancelCall := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelCall()
	host, err := plugins.NewGRPCHost(socketPath)
	if err != nil {
		cancelServer()
		t.Fatal(err)
	}
	defer host.Shutdown(context.Background())
	if _, err := host.Handshake(callContext, plugins.HostIdentity{PluginID: "sdk-test", Version: "1.0.0", ProtocolMajor: 1}); err != nil {
		cancelServer()
		t.Fatalf("plugin handshake failed: %v", err)
	}
	collectors, err := host.Describe(callContext)
	if err != nil || len(collectors) != 1 || collectors[0].ID != "system" {
		cancelServer()
		t.Fatalf("plugin description=%+v err=%v", collectors, err)
	}
	if err := host.ValidateConfig(callContext, "system", json.RawMessage(`{}`)); err != nil {
		cancelServer()
		t.Fatalf("plugin config validation failed: %v", err)
	}
	result, err := host.Collect(callContext, plugins.CollectRequest{CollectorID: "system", Config: json.RawMessage(`{}`)})
	if err != nil || len(result.Samples) != 1 || result.Samples[0].MetricID != "load" {
		cancelServer()
		t.Fatalf("plugin collection=%+v err=%v", result, err)
	}
	if err := host.Shutdown(callContext); err != nil {
		t.Fatalf("plugin shutdown RPC failed: %v", err)
	}
	cancelServer()
	if err := <-serverResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("ServeUnix returned %v, want context canceled", err)
	}
	if _, err := os.Lstat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("plugin socket was not removed: %v", err)
	}
}

func TestServeUnixRequiresPrivateRuntimeDirectory(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(parent, "plugin.sock")
	if err := pluginSDK.ServeUnix(context.Background(), socketPath, sdkTestPlugin{}); err == nil {
		t.Fatal("ServeUnix accepted a directory accessible to group or other users")
	}
}

func TestHostSupportsFeature(t *testing.T) {
	identity := pluginSDK.HostIdentity{HostFeatures: []string{pluginSDK.HostFeatureMetricSampleLabels}}
	if !pluginSDK.HostSupportsFeature(identity, pluginSDK.HostFeatureMetricSampleLabels) {
		t.Fatal("advertised host feature was not detected")
	}
	if pluginSDK.HostSupportsFeature(identity, "future-feature") {
		t.Fatal("unadvertised host feature was detected")
	}
}

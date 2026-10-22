/*
Package plugins tests the gRPC transport boundary, contract validation, and hostile inputs.
*/
package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	pluginv1 "github.com/DingZhizCA/homelab-monitor/internal/plugins/rpc/v1"
	"github.com/DingZhizCA/homelab-monitor/internal/testutil"
	"google.golang.org/grpc"
)

type testPlugin struct {
	minHostMajor  uint16
	maxHostMajor  uint16
	blockCollect  bool
	collectStart  chan struct{}
	healthMessage string
	validateErr   error
	validateCalls *atomic.Int32
	requireLabels bool
}

type mutableDescriptorPlugin struct {
	testPlugin
	descriptors []CollectorDescriptor
}

func (p mutableDescriptorPlugin) Describe(context.Context) ([]CollectorDescriptor, error) {
	return p.descriptors, nil
}

func (p mutableDescriptorPlugin) Collect(context.Context, CollectRequest) (CollectResult, error) {
	return CollectResult{
		ObservedAt: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC),
		Samples:    []MetricSample{{MetricID: "network.rx_bytes", Value: json.RawMessage("123"), Quality: QualityValid, Labels: map[string]string{"interface": "eth0"}}},
	}, nil
}

func (p testPlugin) Handshake(_ context.Context, identity HostIdentity) (HostCapabilities, error) {
	if p.requireLabels && !slices.Contains(identity.HostFeatures, HostFeatureMetricSampleLabels) {
		return HostCapabilities{}, &RPCError{Code: RPCIncompatibleProtocol, Message: "Host lacks labeled metric support"}
	}
	return HostCapabilities{
		ProtocolMajor:     identity.ProtocolMajor,
		HostProtocolRange: ProtocolRange{Min: p.minHostMajor, Max: p.maxHostMajor},
	}, nil
}

func (testPlugin) Describe(context.Context) ([]CollectorDescriptor, error) {
	return []CollectorDescriptor{{
		ID: "network.flow",
		Metrics: []MetricDescriptor{{
			ID: "network.rx_bytes", Type: MetricCounter, Unit: "byte", LabelKeys: []string{"interface"},
		}},
	}}, nil
}

func (p testPlugin) ValidateConfig(_ context.Context, collectorID string, config json.RawMessage) error {
	if !json.Valid(config) {
		return &RPCError{Code: RPCInvalidArgument, Message: "invalid collector config"}
	}
	if p.validateCalls != nil {
		p.validateCalls.Add(1)
	}
	if p.validateErr != nil {
		return p.validateErr
	}
	return nil
}

func (p testPlugin) Collect(ctx context.Context, _ CollectRequest) (CollectResult, error) {
	if p.blockCollect {
		if p.collectStart != nil {
			select {
			case p.collectStart <- struct{}{}:
			default:
			}
		}
		<-ctx.Done()
		return CollectResult{}, ctx.Err()
	}
	return CollectResult{
		ObservedAt: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC),
		Samples:    []MetricSample{{MetricID: "network.rx_bytes", Value: json.RawMessage("123"), Quality: QualityValid, Labels: map[string]string{"interface": "eth0"}}},
	}, nil
}

func (p testPlugin) Health(context.Context) (HealthStatus, error) {
	message := p.healthMessage
	if message == "" {
		message = "ready"
	}
	return HealthStatus{Healthy: true, Message: message}, nil
}

func (testPlugin) Shutdown(context.Context) error { return nil }

func startTestPlugin(t *testing.T, implementation PluginContract) string {
	t.Helper()
	socketPath := filepath.Join(testutil.TempDir(t), "plugin.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	if err := RegisterGRPCPlugin(server, implementation); err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return socketPath
}

func TestGRPCHostUsesUnixSocketAndMapsPluginContract(t *testing.T) {
	host, err := NewGRPCHost(startTestPlugin(t, testPlugin{minHostMajor: 1, maxHostMajor: 1}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := host.Handshake(ctx, HostIdentity{PluginID: "network", Version: "1.0.0", ProtocolMajor: 1}); err != nil {
		t.Fatal(err)
	}
	collectors, err := host.Describe(ctx)
	if err != nil || len(collectors) != 1 {
		t.Fatalf("Describe() = %d collectors, %v", len(collectors), err)
	}
	collectors[0].Metrics[0].LabelKeys[0] = "caller-mutation"
	if err := host.ValidateConfig(ctx, "network.flow", json.RawMessage(`{"interface":"eth0"}`)); err != nil {
		t.Fatal(err)
	}
	if err := host.ValidateConfig(ctx, "invalid", json.RawMessage(`{}`)); err == nil {
		t.Fatal("configuration validation for an undescribed collector was accepted")
	} else {
		var rpcErr *RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != RPCInvalidArgument {
			t.Fatalf("config error = %v", err)
		}
	}
	result, err := host.Collect(ctx, CollectRequest{CollectorID: "network.flow", Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(result.Samples[0].Value); got != "123" || result.Samples[0].Quality != QualityValid || result.Samples[0].Labels["interface"] != "eth0" {
		t.Fatalf("Collect() sample = %q (%q) labels=%v", got, result.Samples[0].Quality, result.Samples[0].Labels)
	}
	health, err := host.Health(ctx)
	if err != nil || !health.Healthy {
		t.Fatalf("Health() = %+v, %v", health, err)
	}
	if err := host.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Health(ctx); err == nil {
		t.Fatal("health call succeeded after shutdown")
	}
}

func TestGRPCServerDetachesCachedDescriptorFromPluginMemory(t *testing.T) {
	descriptors := []CollectorDescriptor{{
		ID:      "network.flow",
		Metrics: []MetricDescriptor{{ID: "network.rx_bytes", Type: MetricCounter, Unit: "byte", LabelKeys: []string{"interface"}}},
	}}
	implementation := mutableDescriptorPlugin{
		testPlugin:  testPlugin{minHostMajor: 1, maxHostMajor: 1},
		descriptors: descriptors,
	}
	host, err := NewGRPCHost(startTestPlugin(t, implementation))
	if err != nil {
		t.Fatal(err)
	}
	defer host.conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := host.Describe(ctx); err != nil {
		t.Fatal(err)
	}
	descriptors[0].Metrics[0].LabelKeys[0] = "mutated_after_describe"
	if _, err := host.Collect(ctx, CollectRequest{CollectorID: "network.flow", Config: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("server validation changed after plugin mutated descriptor memory: %v", err)
	}
}

func TestHandshakeNegotiatesAdditiveHostFeatures(t *testing.T) {
	host, err := NewGRPCHost(startTestPlugin(t, testPlugin{minHostMajor: 1, maxHostMajor: 1, requireLabels: true}))
	if err != nil {
		t.Fatal(err)
	}
	defer host.conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := host.client.Handshake(ctx, &pluginv1.HandshakeRequest{PluginId: "network", Version: "1.0.0", HostProtocolMajor: 1}); err == nil {
		t.Fatal("plugin requiring labeled samples accepted a Host that advertised no feature")
	}
	if _, err := host.Handshake(ctx, HostIdentity{PluginID: "network", Version: "1.0.0", ProtocolMajor: 1}); err != nil {
		t.Fatalf("plugin rejected Host's advertised label feature: %v", err)
	}
}

func TestValidateConfigRequiresCollectorDescriptionAtBothRPCBoundaries(t *testing.T) {
	var calls atomic.Int32
	host, err := NewGRPCHost(startTestPlugin(t, testPlugin{minHostMajor: 1, maxHostMajor: 1, validateCalls: &calls}))
	if err != nil {
		t.Fatal(err)
	}
	defer host.conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	config := json.RawMessage(`{}`)
	if err := host.ValidateConfig(ctx, "network.flow", config); err == nil {
		t.Fatal("client accepted validation before describing collectors")
	}
	if _, err := host.client.ValidateConfig(ctx, &pluginv1.ValidateConfigRequest{CollectorId: "network.flow", JsonConfig: config}); err == nil {
		t.Fatal("server accepted direct validation before describing collectors")
	}
	if calls.Load() != 0 {
		t.Fatal("plugin implementation was called before its collector was described")
	}
	if _, err := host.Describe(ctx); err != nil {
		t.Fatal(err)
	}
	if err := host.ValidateConfig(ctx, "network.flow", config); err != nil {
		t.Fatalf("described collector config was rejected: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("plugin ValidateConfig calls=%d, want 1 after description", calls.Load())
	}
}

func TestGRPCHostRejectsIncompatibleHandshakeAndNonAbsoluteSocket(t *testing.T) {
	if _, err := NewGRPCHost("relative/plugin.sock"); err == nil {
		t.Fatal("relative socket path was accepted")
	}

	host, err := NewGRPCHost(startTestPlugin(t, testPlugin{minHostMajor: 2, maxHostMajor: 2}))
	if err != nil {
		t.Fatal(err)
	}
	defer host.conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := host.Handshake(ctx, HostIdentity{PluginID: "network", Version: "1.0.0", ProtocolMajor: 1}); err == nil {
		t.Fatal("incompatible plugin protocol was accepted")
	} else {
		var rpcErr *RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != RPCIncompatibleProtocol {
			t.Fatalf("handshake error = %v", err)
		}
	}
}

func TestGRPCHostRejectsRequestsOverMessageLimit(t *testing.T) {
	host, err := NewGRPCHost(startTestPlugin(t, testPlugin{minHostMajor: 1, maxHostMajor: 1}))
	if err != nil {
		t.Fatal(err)
	}
	defer host.conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := host.Describe(ctx); err != nil {
		t.Fatal(err)
	}
	config := append([]byte{'"'}, bytes.Repeat([]byte{'x'}, maxPluginRPCMessageBytes)...)
	config = append(config, '"')
	if err := host.ValidateConfig(ctx, "network.flow", json.RawMessage(config)); err == nil {
		t.Fatal("request larger than the configured gRPC message limit was accepted")
	} else {
		var rpcErr *RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != RPCResourceExhausted {
			t.Fatalf("oversized request error = %v", err)
		}
	}
}

func TestGRPCHostPropagatesCollectionDeadline(t *testing.T) {
	host, err := NewGRPCHost(startTestPlugin(t, testPlugin{minHostMajor: 1, maxHostMajor: 1, blockCollect: true}))
	if err != nil {
		t.Fatal(err)
	}
	defer host.conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := host.Describe(ctx); err != nil {
		t.Fatal(err)
	}
	collectCtx, cancelCollect := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancelCollect()
	_, err = host.Collect(collectCtx, CollectRequest{CollectorID: "network.flow", Config: json.RawMessage(`{}`)})
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != RPCDeadlineExceeded {
		t.Fatalf("Collect() deadline error = %v", err)
	}
}

func TestGRPCHostRequiresDeadlineForEveryOperation(t *testing.T) {
	host, err := NewGRPCHost(startTestPlugin(t, testPlugin{minHostMajor: 1, maxHostMajor: 1}))
	if err != nil {
		t.Fatal(err)
	}
	defer host.conn.Close()

	operations := []struct {
		name string
		call func(context.Context) error
	}{
		{"Handshake", func(ctx context.Context) error {
			_, err := host.Handshake(ctx, HostIdentity{PluginID: "network", Version: "1.0.0", ProtocolMajor: 1})
			return err
		}},
		{"Describe", func(ctx context.Context) error { _, err := host.Describe(ctx); return err }},
		{"ValidateConfig", func(ctx context.Context) error {
			return host.ValidateConfig(ctx, "network.flow", json.RawMessage(`{}`))
		}},
		{"Collect", func(ctx context.Context) error {
			_, err := host.Collect(ctx, CollectRequest{CollectorID: "network.flow", Config: json.RawMessage(`{}`)})
			return err
		}},
		{"Health", func(ctx context.Context) error { _, err := host.Health(ctx); return err }},
		{"Shutdown", host.Shutdown},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			for _, ctx := range []context.Context{nil, context.Background()} {
				if err := operation.call(ctx); err == nil {
					t.Fatal("operation without a deadline was accepted")
				}
			}
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := host.Health(ctx); err != nil {
		t.Fatalf("host was closed after rejecting an unbounded shutdown: %v", err)
	}
}

func TestGRPCBoundaryBoundsUntrustedPluginDiagnostics(t *testing.T) {
	message := strings.Repeat("界", maxPluginDiagnosticBytes)
	implementation := testPlugin{
		minHostMajor: 1, maxHostMajor: 1, healthMessage: message,
		validateErr: &RPCError{Code: RPCInvalidArgument, Message: message},
	}
	host, err := NewGRPCHost(startTestPlugin(t, implementation))
	if err != nil {
		t.Fatal(err)
	}
	defer host.conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	health, err := host.Health(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(health.Message) > maxPluginDiagnosticBytes || !utf8.ValidString(health.Message) {
		t.Fatalf("plugin health diagnostic is not safely bounded: bytes=%d", len(health.Message))
	}
	if err := host.ValidateConfig(ctx, "network.flow", json.RawMessage(`{}`)); err == nil {
		t.Fatal("plugin validation error was not returned")
	} else {
		var rpcErr *RPCError
		if !errors.As(err, &rpcErr) || len(rpcErr.Message) > maxPluginDiagnosticBytes || !utf8.ValidString(rpcErr.Message) {
			t.Fatalf("plugin RPC diagnostic is not safely bounded: %v", err)
		}
	}
}

/*
Package plugins adapts the versioned Plugin RPC to the local Host contract.

This client only dials a Unix domain socket. Socket ownership, peer identity,
process limits, and access to the socket directory must be enforced by the
Supervisor and server-side runtime before this transport is trusted.
*/
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sync"

	pluginv1 "github.com/DingZhizCA/homelab-monitor/internal/plugins/rpc/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// GRPCHost implements PluginContract using the local versioned gRPC service.
type GRPCHost struct {
	conn   *grpc.ClientConn
	client pluginv1.PluginClient

	mu         sync.RWMutex
	collectors map[string]CollectorDescriptor
	closed     bool
}

// NewGRPCHost creates a lazy gRPC client bound to one absolute Unix socket path.
func NewGRPCHost(socketPath string) (*GRPCHost, error) {
	if !filepath.IsAbs(socketPath) {
		return nil, errors.New("plugin socket path must be absolute")
	}
	conn, err := grpc.NewClient(
		"passthrough:///plugin",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(maxPluginRPCMessageBytes),
			grpc.MaxCallSendMsgSize(maxPluginRPCMessageBytes),
		),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("create plugin RPC client: %w", err)
	}
	return &GRPCHost{conn: conn, client: pluginv1.NewPluginClient(conn), collectors: make(map[string]CollectorDescriptor)}, nil
}

// Handshake negotiates the plugin protocol before other host calls are used.
func (h *GRPCHost) Handshake(ctx context.Context, identity HostIdentity) (HostCapabilities, error) {
	if err := requirePluginDeadline(ctx); err != nil {
		return HostCapabilities{}, err
	}
	if err := h.checkOpen(); err != nil {
		return HostCapabilities{}, err
	}
	if identity.PluginID == "" || identity.Version == "" || identity.ProtocolMajor == 0 {
		return HostCapabilities{}, &RPCError{Code: RPCInvalidArgument, Message: "plugin identity and protocol major are required"}
	}
	response, err := h.client.Handshake(ctx, &pluginv1.HandshakeRequest{
		PluginId: identity.PluginID, Version: identity.Version, HostProtocolMajor: uint32(identity.ProtocolMajor),
		HostFeatures: []string{HostFeatureMetricSampleLabels},
	})
	if err != nil {
		return HostCapabilities{}, rpcError(err)
	}
	if response.MinHostProtocolMajor == 0 || response.MaxHostProtocolMajor > 65535 || response.MaxHostProtocolMajor < response.MinHostProtocolMajor ||
		response.PluginProtocolMajor != uint32(identity.ProtocolMajor) ||
		response.MinHostProtocolMajor > uint32(identity.ProtocolMajor) ||
		response.MaxHostProtocolMajor < uint32(identity.ProtocolMajor) {
		return HostCapabilities{}, &RPCError{Code: RPCIncompatibleProtocol, Message: "plugin does not accept the requested protocol major"}
	}
	return HostCapabilities{
		ProtocolMajor:     uint16(response.PluginProtocolMajor),
		HostProtocolRange: ProtocolRange{Min: uint16(response.MinHostProtocolMajor), Max: uint16(response.MaxHostProtocolMajor)},
	}, nil
}

// Describe fetches static capabilities and caches descriptors for result validation.
func (h *GRPCHost) Describe(ctx context.Context) ([]CollectorDescriptor, error) {
	if err := requirePluginDeadline(ctx); err != nil {
		return nil, err
	}
	if err := h.checkOpen(); err != nil {
		return nil, err
	}
	response, err := h.client.Describe(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, rpcError(err)
	}
	collectors := make([]CollectorDescriptor, 0, len(response.Collectors))
	byID := make(map[string]CollectorDescriptor, len(response.Collectors))
	for _, remote := range response.Collectors {
		collector, err := collectorFromProto(remote)
		if err != nil {
			return nil, err
		}
		if _, exists := byID[collector.ID]; exists {
			return nil, fmt.Errorf("plugin described collector %q more than once", collector.ID)
		}
		byID[collector.ID] = cloneCollectorDescriptor(collector)
		collectors = append(collectors, collector)
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, errors.New("plugin RPC client is closed")
	}
	h.collectors = byID
	h.mu.Unlock()
	return collectors, nil
}

func cloneCollectorDescriptor(collector CollectorDescriptor) CollectorDescriptor {
	cloned := CollectorDescriptor{ID: collector.ID, Metrics: make([]MetricDescriptor, len(collector.Metrics))}
	for index, metric := range collector.Metrics {
		cloned.Metrics[index] = metric
		cloned.Metrics[index].LabelKeys = append([]string(nil), metric.LabelKeys...)
	}
	return cloned
}

// ValidateConfig asks the plugin for a side-effect-free configuration check.
func (h *GRPCHost) ValidateConfig(ctx context.Context, collectorID string, config json.RawMessage) error {
	if err := requirePluginDeadline(ctx); err != nil {
		return err
	}
	h.mu.RLock()
	_, described := h.collectors[collectorID]
	closed := h.closed
	h.mu.RUnlock()
	if closed {
		return errors.New("plugin RPC client is closed")
	}
	if !described {
		return &RPCError{Code: RPCInvalidArgument, Message: "collector must be described before configuration validation"}
	}
	if !json.Valid(config) {
		return &RPCError{Code: RPCInvalidArgument, Message: "collector config must contain valid JSON"}
	}
	_, err := h.client.ValidateConfig(ctx, &pluginv1.ValidateConfigRequest{CollectorId: collectorID, JsonConfig: config})
	return rpcError(err)
}

// Collect returns a context-bounded result matching the collector's last description.
func (h *GRPCHost) Collect(ctx context.Context, request CollectRequest) (CollectResult, error) {
	if err := requirePluginDeadline(ctx); err != nil {
		return CollectResult{}, err
	}
	h.mu.RLock()
	descriptor, described := h.collectors[request.CollectorID]
	closed := h.closed
	h.mu.RUnlock()
	if closed {
		return CollectResult{}, errors.New("plugin RPC client is closed")
	}
	if !described {
		return CollectResult{}, &RPCError{Code: RPCInvalidArgument, Message: "collector must be described before collection"}
	}
	if !json.Valid(request.Config) {
		return CollectResult{}, &RPCError{Code: RPCInvalidArgument, Message: "collector config must contain valid JSON"}
	}
	response, err := h.client.Collect(ctx, &pluginv1.CollectRequest{CollectorId: request.CollectorID, JsonConfig: request.Config})
	if err != nil {
		return CollectResult{}, rpcError(err)
	}
	if response.ObservedAt == nil || response.ObservedAt.CheckValid() != nil {
		return CollectResult{}, &RPCError{Code: RPCInternal, Message: "plugin returned an invalid observation timestamp"}
	}
	result := CollectResult{ObservedAt: response.ObservedAt.AsTime(), Samples: make([]MetricSample, 0, len(response.Samples))}
	for _, sample := range response.Samples {
		result.Samples = append(result.Samples, MetricSample{
			MetricID: sample.MetricId,
			Value:    append(json.RawMessage(nil), sample.JsonValue...),
			Quality:  qualityFromProto(sample.Quality),
			Labels:   cloneMetricLabels(sample.Labels),
		})
	}
	if err := result.Validate(descriptor); err != nil {
		return CollectResult{}, &RPCError{Code: RPCInternal, Message: err.Error()}
	}
	return result, nil
}

// Health reports plugin-process health independently from monitored device health.
func (h *GRPCHost) Health(ctx context.Context) (HealthStatus, error) {
	if err := requirePluginDeadline(ctx); err != nil {
		return HealthStatus{}, err
	}
	if err := h.checkOpen(); err != nil {
		return HealthStatus{}, err
	}
	response, err := h.client.Health(ctx, &emptypb.Empty{})
	if err != nil {
		return HealthStatus{}, rpcError(err)
	}
	switch response.State {
	case pluginv1.HealthState_HEALTH_STATE_SERVING:
		return HealthStatus{Healthy: true, Message: boundedPluginDiagnostic(response.Message)}, nil
	case pluginv1.HealthState_HEALTH_STATE_DEGRADED, pluginv1.HealthState_HEALTH_STATE_NOT_SERVING:
		return HealthStatus{Healthy: false, Message: boundedPluginDiagnostic(response.Message)}, nil
	default:
		return HealthStatus{}, &RPCError{Code: RPCInternal, Message: "plugin returned an unknown health state"}
	}
}

// Shutdown requests a bounded plugin shutdown and then closes the local RPC connection.
func (h *GRPCHost) Shutdown(ctx context.Context) error {
	if err := requirePluginDeadline(ctx); err != nil {
		return err
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	h.mu.Unlock()
	_, callErr := h.client.Shutdown(ctx, &emptypb.Empty{})
	closeErr := h.conn.Close()
	if callErr != nil {
		return rpcError(callErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close plugin RPC connection: %w", closeErr)
	}
	return nil
}

func (h *GRPCHost) checkOpen() error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.closed {
		return errors.New("plugin RPC client is closed")
	}
	return nil
}

func collectorFromProto(remote *pluginv1.CollectorDescriptor) (CollectorDescriptor, error) {
	collector := CollectorDescriptor{ID: remote.CollectorId, Metrics: make([]MetricDescriptor, 0, len(remote.Metrics))}
	for _, metric := range remote.Metrics {
		collector.Metrics = append(collector.Metrics, MetricDescriptor{
			ID: metric.MetricId, Type: metricTypeFromProto(metric.Type), Unit: metric.Unit,
			LabelKeys: append([]string(nil), metric.LabelKeys...),
		})
	}
	if err := collector.Validate(); err != nil {
		return CollectorDescriptor{}, fmt.Errorf("invalid plugin descriptor: %w", err)
	}
	return collector, nil
}

func metricTypeFromProto(value pluginv1.MetricType) MetricType {
	switch value {
	case pluginv1.MetricType_METRIC_TYPE_COUNTER:
		return MetricCounter
	case pluginv1.MetricType_METRIC_TYPE_GAUGE:
		return MetricGauge
	case pluginv1.MetricType_METRIC_TYPE_STATE:
		return MetricState
	case pluginv1.MetricType_METRIC_TYPE_EVENT:
		return MetricEvent
	default:
		return ""
	}
}

func qualityFromProto(value pluginv1.DataQuality) DataQuality {
	switch value {
	case pluginv1.DataQuality_DATA_QUALITY_VALID:
		return QualityValid
	case pluginv1.DataQuality_DATA_QUALITY_PARTIAL:
		return QualityPartial
	case pluginv1.DataQuality_DATA_QUALITY_ESTIMATED:
		return QualityEstimated
	case pluginv1.DataQuality_DATA_QUALITY_MISSING:
		return QualityMissing
	case pluginv1.DataQuality_DATA_QUALITY_RESET:
		return QualityReset
	case pluginv1.DataQuality_DATA_QUALITY_STALE:
		return QualityStale
	default:
		return ""
	}
}

func rpcError(err error) error {
	if err == nil {
		return nil
	}
	if rpcErr := new(RPCError); errors.As(err, &rpcErr) {
		return &RPCError{Code: rpcErr.Code, Message: boundedPluginDiagnostic(rpcErr.Message)}
	}
	statusValue, ok := status.FromError(err)
	if !ok {
		return err
	}
	for _, detail := range statusValue.Details() {
		if pluginError, ok := detail.(*pluginv1.PluginErrorDetail); ok {
			return &RPCError{Code: rpcCodeFromProto(pluginError.Code), Message: boundedPluginDiagnostic(pluginError.Message)}
		}
	}
	return &RPCError{Code: rpcCodeFromGRPC(statusValue.Code()), Message: boundedPluginDiagnostic(statusValue.Message())}
}

func rpcCodeFromProto(value pluginv1.PluginErrorCode) RPCCode {
	switch value {
	case pluginv1.PluginErrorCode_PLUGIN_ERROR_INVALID_ARGUMENT:
		return RPCInvalidArgument
	case pluginv1.PluginErrorCode_PLUGIN_ERROR_PERMISSION_DENIED:
		return RPCPermissionDenied
	case pluginv1.PluginErrorCode_PLUGIN_ERROR_RESOURCE_EXHAUSTED:
		return RPCResourceExhausted
	case pluginv1.PluginErrorCode_PLUGIN_ERROR_DEADLINE_EXCEEDED:
		return RPCDeadlineExceeded
	case pluginv1.PluginErrorCode_PLUGIN_ERROR_UNAVAILABLE:
		return RPCUnavailable
	case pluginv1.PluginErrorCode_PLUGIN_ERROR_INCOMPATIBLE_PROTOCOL:
		return RPCIncompatibleProtocol
	default:
		return RPCInternal
	}
}

func rpcCodeFromGRPC(value codes.Code) RPCCode {
	switch value {
	case codes.InvalidArgument:
		return RPCInvalidArgument
	case codes.PermissionDenied, codes.Unauthenticated:
		return RPCPermissionDenied
	case codes.ResourceExhausted:
		return RPCResourceExhausted
	case codes.DeadlineExceeded:
		return RPCDeadlineExceeded
	case codes.Canceled:
		return RPCUnavailable
	case codes.FailedPrecondition:
		return RPCIncompatibleProtocol
	case codes.Unavailable:
		return RPCUnavailable
	default:
		return RPCInternal
	}
}

var _ PluginContract = (*GRPCHost)(nil)

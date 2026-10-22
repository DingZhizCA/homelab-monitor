/*
Package plugins adapts plugin implementations to the generated gRPC service.

Plugin packages implement the transport-independent PluginContract, then
register it with a gRPC server. Listener creation and Unix-socket permissions
remain the responsibility of the process runtime.
*/
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	pluginv1 "github.com/DingZhizCA/homelab-monitor/internal/plugins/rpc/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// RegisterGRPCPlugin registers a transport-independent implementation on a gRPC server.
func RegisterGRPCPlugin(registrar grpc.ServiceRegistrar, implementation PluginContract) error {
	if registrar == nil || implementation == nil {
		return errors.New("gRPC registrar and plugin implementation are required")
	}
	pluginv1.RegisterPluginServer(registrar, &grpcPluginServer{implementation: implementation, collectors: make(map[string]CollectorDescriptor)})
	return nil
}

type grpcPluginServer struct {
	pluginv1.UnimplementedPluginServer
	implementation PluginContract

	mu         sync.RWMutex
	collectors map[string]CollectorDescriptor
}

func (s *grpcPluginServer) Handshake(ctx context.Context, request *pluginv1.HandshakeRequest) (*pluginv1.HandshakeResponse, error) {
	if request.HostProtocolMajor > 65535 {
		return nil, toGRPCError(&RPCError{Code: RPCIncompatibleProtocol, Message: "host protocol major is out of range"})
	}
	identity := HostIdentity{
		PluginID: request.PluginId, Version: request.Version, ProtocolMajor: uint16(request.HostProtocolMajor),
		HostFeatures: append([]string(nil), request.HostFeatures...),
	}
	capabilities, err := s.implementation.Handshake(ctx, identity)
	if err != nil {
		return nil, toGRPCError(err)
	}
	if capabilities.HostProtocolRange.Min == 0 || capabilities.HostProtocolRange.Max < capabilities.HostProtocolRange.Min ||
		capabilities.ProtocolMajor == 0 || capabilities.ProtocolMajor != identity.ProtocolMajor ||
		!capabilities.HostProtocolRange.Supports(identity.ProtocolMajor) {
		return nil, toGRPCError(&RPCError{Code: RPCIncompatibleProtocol, Message: "plugin and host protocol versions are incompatible"})
	}
	return &pluginv1.HandshakeResponse{
		PluginProtocolMajor:  uint32(capabilities.ProtocolMajor),
		MinHostProtocolMajor: uint32(capabilities.HostProtocolRange.Min),
		MaxHostProtocolMajor: uint32(capabilities.HostProtocolRange.Max),
	}, nil
}

func (s *grpcPluginServer) Describe(ctx context.Context, _ *emptypb.Empty) (*pluginv1.DescribeResponse, error) {
	collectors, err := s.implementation.Describe(ctx)
	if err != nil {
		return nil, toGRPCError(err)
	}
	response := &pluginv1.DescribeResponse{Collectors: make([]*pluginv1.CollectorDescriptor, 0, len(collectors))}
	byID := make(map[string]CollectorDescriptor, len(collectors))
	for _, collector := range collectors {
		if err := collector.Validate(); err != nil {
			return nil, toGRPCError(&RPCError{Code: RPCInternal, Message: err.Error()})
		}
		if _, exists := byID[collector.ID]; exists {
			return nil, toGRPCError(&RPCError{Code: RPCInternal, Message: fmt.Sprintf("duplicate collector %q", collector.ID)})
		}
		byID[collector.ID] = cloneCollectorDescriptor(collector)
		remote := &pluginv1.CollectorDescriptor{CollectorId: collector.ID, Metrics: make([]*pluginv1.MetricDescriptor, 0, len(collector.Metrics))}
		for _, metric := range collector.Metrics {
			remote.Metrics = append(remote.Metrics, &pluginv1.MetricDescriptor{
				MetricId: metric.ID, Type: metricTypeToProto(metric.Type), Unit: metric.Unit,
				LabelKeys: append([]string(nil), metric.LabelKeys...),
			})
		}
		response.Collectors = append(response.Collectors, remote)
	}
	s.mu.Lock()
	s.collectors = byID
	s.mu.Unlock()
	return response, nil
}

func (s *grpcPluginServer) ValidateConfig(ctx context.Context, request *pluginv1.ValidateConfigRequest) (*emptypb.Empty, error) {
	s.mu.RLock()
	_, described := s.collectors[request.CollectorId]
	s.mu.RUnlock()
	if !described {
		return nil, toGRPCError(&RPCError{Code: RPCInvalidArgument, Message: "collector must be described before configuration validation"})
	}
	if !json.Valid(request.JsonConfig) {
		return nil, toGRPCError(&RPCError{Code: RPCInvalidArgument, Message: "collector config must contain valid JSON"})
	}
	if err := s.implementation.ValidateConfig(ctx, request.CollectorId, request.JsonConfig); err != nil {
		return nil, toGRPCError(err)
	}
	return &emptypb.Empty{}, nil
}

func (s *grpcPluginServer) Collect(ctx context.Context, request *pluginv1.CollectRequest) (*pluginv1.CollectResponse, error) {
	s.mu.RLock()
	descriptor, described := s.collectors[request.CollectorId]
	s.mu.RUnlock()
	if !described {
		return nil, toGRPCError(&RPCError{Code: RPCInvalidArgument, Message: "collector must be described before collection"})
	}
	if !json.Valid(request.JsonConfig) {
		return nil, toGRPCError(&RPCError{Code: RPCInvalidArgument, Message: "collector config must contain valid JSON"})
	}
	result, err := s.implementation.Collect(ctx, CollectRequest{CollectorID: request.CollectorId, Config: request.JsonConfig})
	if err != nil {
		return nil, toGRPCError(err)
	}
	if err := result.Validate(descriptor); err != nil {
		return nil, toGRPCError(&RPCError{Code: RPCInternal, Message: err.Error()})
	}
	response := &pluginv1.CollectResponse{ObservedAt: timestamppb.New(result.ObservedAt), Samples: make([]*pluginv1.MetricSample, 0, len(result.Samples))}
	for _, sample := range result.Samples {
		response.Samples = append(response.Samples, &pluginv1.MetricSample{
			MetricId: sample.MetricID, JsonValue: append([]byte(nil), sample.Value...),
			Quality: qualityToProto(sample.Quality), Labels: cloneMetricLabels(sample.Labels),
		})
	}
	return response, nil
}

func cloneMetricLabels(labels map[string]string) map[string]string {
	if labels == nil {
		return nil
	}
	cloned := make(map[string]string, len(labels))
	for key, value := range labels {
		cloned[key] = value
	}
	return cloned
}

func (s *grpcPluginServer) Health(ctx context.Context, _ *emptypb.Empty) (*pluginv1.HealthResponse, error) {
	health, err := s.implementation.Health(ctx)
	if err != nil {
		return nil, toGRPCError(err)
	}
	state := pluginv1.HealthState_HEALTH_STATE_NOT_SERVING
	if health.Healthy {
		state = pluginv1.HealthState_HEALTH_STATE_SERVING
	}
	return &pluginv1.HealthResponse{State: state, Message: boundedPluginDiagnostic(health.Message)}, nil
}

func (s *grpcPluginServer) Shutdown(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	if err := s.implementation.Shutdown(ctx); err != nil {
		return nil, toGRPCError(err)
	}
	return &emptypb.Empty{}, nil
}

func metricTypeToProto(value MetricType) pluginv1.MetricType {
	switch value {
	case MetricCounter:
		return pluginv1.MetricType_METRIC_TYPE_COUNTER
	case MetricGauge:
		return pluginv1.MetricType_METRIC_TYPE_GAUGE
	case MetricState:
		return pluginv1.MetricType_METRIC_TYPE_STATE
	case MetricEvent:
		return pluginv1.MetricType_METRIC_TYPE_EVENT
	default:
		return pluginv1.MetricType_METRIC_TYPE_UNSPECIFIED
	}
}

func qualityToProto(value DataQuality) pluginv1.DataQuality {
	switch value {
	case QualityValid:
		return pluginv1.DataQuality_DATA_QUALITY_VALID
	case QualityPartial:
		return pluginv1.DataQuality_DATA_QUALITY_PARTIAL
	case QualityEstimated:
		return pluginv1.DataQuality_DATA_QUALITY_ESTIMATED
	case QualityMissing:
		return pluginv1.DataQuality_DATA_QUALITY_MISSING
	case QualityReset:
		return pluginv1.DataQuality_DATA_QUALITY_RESET
	case QualityStale:
		return pluginv1.DataQuality_DATA_QUALITY_STALE
	default:
		return pluginv1.DataQuality_DATA_QUALITY_UNSPECIFIED
	}
}

func toGRPCError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "plugin operation exceeded its deadline")
	}
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, "plugin operation was canceled")
	}
	rpcErr := new(RPCError)
	if !errors.As(err, &rpcErr) {
		rpcErr = &RPCError{Code: RPCInternal, Message: err.Error()}
	}
	message := boundedPluginDiagnostic(rpcErr.Message)
	grpcStatus := status.New(grpcCodeFromRPC(rpcErr.Code), message)
	withDetails, detailErr := grpcStatus.WithDetails(&pluginv1.PluginErrorDetail{
		Code: pluginErrorCodeFromRPC(rpcErr.Code), Message: message,
	})
	if detailErr != nil {
		return grpcStatus.Err()
	}
	return withDetails.Err()
}

func grpcCodeFromRPC(value RPCCode) codes.Code {
	switch value {
	case RPCInvalidArgument:
		return codes.InvalidArgument
	case RPCPermissionDenied:
		return codes.PermissionDenied
	case RPCResourceExhausted:
		return codes.ResourceExhausted
	case RPCDeadlineExceeded:
		return codes.DeadlineExceeded
	case RPCUnavailable:
		return codes.Unavailable
	case RPCIncompatibleProtocol:
		return codes.FailedPrecondition
	default:
		return codes.Internal
	}
}

func pluginErrorCodeFromRPC(value RPCCode) pluginv1.PluginErrorCode {
	switch value {
	case RPCInvalidArgument:
		return pluginv1.PluginErrorCode_PLUGIN_ERROR_INVALID_ARGUMENT
	case RPCPermissionDenied:
		return pluginv1.PluginErrorCode_PLUGIN_ERROR_PERMISSION_DENIED
	case RPCResourceExhausted:
		return pluginv1.PluginErrorCode_PLUGIN_ERROR_RESOURCE_EXHAUSTED
	case RPCDeadlineExceeded:
		return pluginv1.PluginErrorCode_PLUGIN_ERROR_DEADLINE_EXCEEDED
	case RPCUnavailable:
		return pluginv1.PluginErrorCode_PLUGIN_ERROR_UNAVAILABLE
	case RPCIncompatibleProtocol:
		return pluginv1.PluginErrorCode_PLUGIN_ERROR_INCOMPATIBLE_PROTOCOL
	default:
		return pluginv1.PluginErrorCode_PLUGIN_ERROR_INTERNAL
	}
}

var _ pluginv1.PluginServer = (*grpcPluginServer)(nil)

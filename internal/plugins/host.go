/*
Package plugins defines the Plugin Host contract and local plugin lifecycle
boundaries. Contract types are independent of gRPC; grpc_host.go and
grpc_server.go adapt them to the versioned Unix-socket protocol. This package
does not schedule device sampling or access Server storage.
*/
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// maxPluginRPCMessageBytes makes the gRPC 4 MiB message boundary explicit on both peers.
// Revisit it with measured descriptor and collection payloads before widening it.
const maxPluginRPCMessageBytes = 4 << 20

const maxPluginDiagnosticBytes = 1024

// HostFeatureMetricSampleLabels identifies support for labeled metric series.
const HostFeatureMetricSampleLabels = "metric_sample_labels"

func boundedPluginDiagnostic(message string) string {
	message = strings.ToValidUTF8(message, "�")
	if len(message) <= maxPluginDiagnosticBytes {
		return message
	}
	end := maxPluginDiagnosticBytes
	for end > 0 && !utf8.RuneStart(message[end]) {
		end--
	}
	return message[:end]
}

// PluginContract is the transport-independent behavior of one plugin service.
// Plugin implementations and the Agent's RPC adapter use the same contract.
type PluginContract interface {
	Handshake(context.Context, HostIdentity) (HostCapabilities, error)
	Describe(context.Context) ([]CollectorDescriptor, error)
	ValidateConfig(context.Context, string, json.RawMessage) error
	Collect(context.Context, CollectRequest) (CollectResult, error)
	Health(context.Context) (HealthStatus, error)
	Shutdown(context.Context) error
}

// HostIdentity identifies the caller and protocol major during startup negotiation.
type HostIdentity struct {
	PluginID      string
	Version       string
	ProtocolMajor uint16
	HostFeatures  []string
}

// HostCapabilities reports the protocol major accepted by the plugin.
type HostCapabilities struct {
	ProtocolMajor     uint16
	HostProtocolRange ProtocolRange
}

// CollectorDescriptor describes static collector and metric capabilities.
type CollectorDescriptor struct {
	ID      string
	Metrics []MetricDescriptor
}

// MetricDescriptor gives one emitted metric a stable type and unit.
type MetricDescriptor struct {
	ID        string
	Type      MetricType
	Unit      string
	LabelKeys []string
}

// MetricType identifies the semantic shape of a plugin metric.
type MetricType string

const (
	MetricCounter MetricType = "counter"
	MetricGauge   MetricType = "gauge"
	MetricState   MetricType = "state"
	MetricEvent   MetricType = "event"
)

// DataQuality describes whether a reported observation is complete and current.
type DataQuality string

const (
	QualityValid     DataQuality = "valid"
	QualityPartial   DataQuality = "partial"
	QualityEstimated DataQuality = "estimated"
	QualityMissing   DataQuality = "missing"
	QualityReset     DataQuality = "reset"
	QualityStale     DataQuality = "stale"
)

// CollectRequest selects one described collector; its context carries the deadline.
type CollectRequest struct {
	CollectorID string
	Config      json.RawMessage
}

// CollectResult contains measured values and their actual observation time.
type CollectResult struct {
	ObservedAt time.Time
	Samples    []MetricSample
}

// MetricSample contains one metric value and its declared dimension labels.
type MetricSample struct {
	MetricID string
	Value    json.RawMessage
	Quality  DataQuality
	Labels   map[string]string
}

// HealthStatus describes plugin process health, not target-device availability.
type HealthStatus struct {
	Healthy bool
	Message string
}

// RPCCode is a stable error category suitable for mapping to gRPC status codes.
type RPCCode string

const (
	RPCInvalidArgument      RPCCode = "invalid_argument"
	RPCPermissionDenied     RPCCode = "permission_denied"
	RPCResourceExhausted    RPCCode = "resource_exhausted"
	RPCDeadlineExceeded     RPCCode = "deadline_exceeded"
	RPCUnavailable          RPCCode = "unavailable"
	RPCIncompatibleProtocol RPCCode = "incompatible_protocol"
	RPCInternal             RPCCode = "internal"
)

// RPCError preserves a machine-readable failure category across the host boundary.
type RPCError struct {
	Code    RPCCode
	Message string
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("plugin RPC %s: %s", e.Code, e.Message)
}

// Validate checks static descriptor invariants without invoking a collector.
func (d CollectorDescriptor) Validate() error {
	if !identifierPattern.MatchString(d.ID) {
		return errors.New("collector id must be a stable identifier")
	}
	seenMetrics := make(map[string]struct{}, len(d.Metrics))
	for _, metric := range d.Metrics {
		if !identifierPattern.MatchString(metric.ID) || strings.TrimSpace(metric.Unit) == "" {
			return fmt.Errorf("metric %q requires a stable id and unit", metric.ID)
		}
		if !validMetricType(metric.Type) {
			return fmt.Errorf("metric %q has unsupported type %q", metric.ID, metric.Type)
		}
		if _, ok := seenMetrics[metric.ID]; ok {
			return fmt.Errorf("duplicate metric id %q", metric.ID)
		}
		seenMetrics[metric.ID] = struct{}{}
		seenLabels := make(map[string]struct{}, len(metric.LabelKeys))
		for _, label := range metric.LabelKeys {
			if !identifierPattern.MatchString(label) {
				return fmt.Errorf("metric %q has invalid label key %q", metric.ID, label)
			}
			if _, ok := seenLabels[label]; ok {
				return fmt.Errorf("metric %q has duplicate label key %q", metric.ID, label)
			}
			seenLabels[label] = struct{}{}
		}
	}
	return nil
}

// Validate checks that a collection response matches the static metric description.
func (r CollectResult) Validate(descriptor CollectorDescriptor) error {
	if r.ObservedAt.IsZero() {
		return errors.New("collection observed_at is required")
	}
	if err := descriptor.Validate(); err != nil {
		return fmt.Errorf("invalid collector descriptor: %w", err)
	}
	known := make(map[string]MetricDescriptor, len(descriptor.Metrics))
	for _, metric := range descriptor.Metrics {
		known[metric.ID] = metric
	}
	seen := make(map[string]struct{}, len(r.Samples))
	for _, sample := range r.Samples {
		metric, ok := known[sample.MetricID]
		if !ok {
			return fmt.Errorf("sample references undescribed metric %q", sample.MetricID)
		}
		if len(sample.Labels) != len(metric.LabelKeys) {
			return fmt.Errorf("metric %q sample labels do not match the declared label keys", sample.MetricID)
		}
		for _, key := range metric.LabelKeys {
			value, exists := sample.Labels[key]
			if !exists {
				return fmt.Errorf("metric %q sample is missing label %q", sample.MetricID, key)
			}
			if !utf8.ValidString(value) {
				return fmt.Errorf("metric %q label %q is not valid UTF-8", sample.MetricID, key)
			}
		}
		for key := range sample.Labels {
			if !slices.Contains(metric.LabelKeys, key) {
				return fmt.Errorf("metric %q sample has undeclared label %q", sample.MetricID, key)
			}
		}
		identity := metricSampleIdentity(sample)
		if _, ok := seen[identity]; ok {
			return fmt.Errorf("duplicate sample for metric %q and labels", sample.MetricID)
		}
		if !json.Valid(sample.Value) {
			return fmt.Errorf("metric %q has invalid JSON value", sample.MetricID)
		}
		if !validQuality(sample.Quality) {
			return fmt.Errorf("metric %q has unsupported quality %q", sample.MetricID, sample.Quality)
		}
		seen[identity] = struct{}{}
	}
	return nil
}

func metricSampleIdentity(sample MetricSample) string {
	keys := make([]string, 0, len(sample.Labels))
	for key := range sample.Labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var identity strings.Builder
	identity.WriteString(sample.MetricID)
	identity.WriteByte(0)
	for _, key := range keys {
		fmt.Fprintf(&identity, "%d:%s%d:%s", len(key), key, len(sample.Labels[key]), sample.Labels[key])
	}
	return identity.String()
}

func validMetricType(value MetricType) bool {
	switch value {
	case MetricCounter, MetricGauge, MetricState, MetricEvent:
		return true
	default:
		return false
	}
}

func validQuality(value DataQuality) bool {
	switch value {
	case QualityValid, QualityPartial, QualityEstimated, QualityMissing, QualityReset, QualityStale:
		return true
	default:
		return false
	}
}

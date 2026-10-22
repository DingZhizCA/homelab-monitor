/*
Package dashboard provides a bounded latest-snapshot metric query boundary.

Descriptors map approved metric names to existing Agent fields. Query
authorization is explicit, and this path never schedules device collection.
*/
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DingZhizCA/homelab-monitor/internal/auth"
	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

type MetricDescriptor struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Unit        string   `json:"unit"`
	Dimensions  []string `json:"dimensions,omitempty"`
	Description string   `json:"description"`
}

type MetricPoint struct {
	Value      *float64          `json:"value,omitempty"`
	Dimensions map[string]string `json:"dimensions,omitempty"`
	Quality    string            `json:"quality"`
}

type MetricResult struct {
	Descriptor MetricDescriptor `json:"descriptor"`
	DeviceID   string           `json:"device_id"`
	SampledAt  time.Time        `json:"sampled_at"`
	ReceivedAt time.Time        `json:"received_at"`
	Status     string           `json:"status"`
	Points     []MetricPoint    `json:"points"`
}

type Snapshot struct {
	Envelope   metrics.MetricEnvelope
	ReceivedAt time.Time
	Status     string
}

type SnapshotSource interface {
	Current(context.Context, string) (Snapshot, bool, error)
}

type MetricAuthorizer interface {
	AuthorizeMetric(auth.Principal, string, string) error
}

type metricExtractor func(metrics.MetricEnvelope) []MetricPoint

type MetricRegistry struct {
	descriptors map[string]MetricDescriptor
	extractors  map[string]metricExtractor
}

func NewMetricRegistry() *MetricRegistry {
	entries := []struct {
		descriptor MetricDescriptor
		extract    metricExtractor
	}{
		{MetricDescriptor{ID: "system.load1", Name: "System load (1 minute)", Unit: "load", Description: "One-minute system load average."}, scalarMetric(func(m metrics.MetricEnvelope) float64 { return m.System.Load1 })},
		{MetricDescriptor{ID: "system.load5", Name: "System load (5 minutes)", Unit: "load", Description: "Five-minute system load average."}, scalarMetric(func(m metrics.MetricEnvelope) float64 { return m.System.Load5 })},
		{MetricDescriptor{ID: "system.load15", Name: "System load (15 minutes)", Unit: "load", Description: "Fifteen-minute system load average."}, scalarMetric(func(m metrics.MetricEnvelope) float64 { return m.System.Load15 })},
		{MetricDescriptor{ID: "cpu.usage_percent", Name: "CPU usage", Unit: "percent", Description: "CPU usage calculated by the Agent."}, scalarMetric(func(m metrics.MetricEnvelope) float64 { return m.CPU.UsagePercent })},
		{MetricDescriptor{ID: "cpu.temperature_celsius", Name: "CPU temperature", Unit: "celsius", Description: "Optional hardware temperature; absent when no safe sensor reading is available."}, optionalMetric(func(m metrics.MetricEnvelope) *float64 { return m.CPU.TemperatureCelsius })},
		{MetricDescriptor{ID: "memory.usage_percent", Name: "Memory usage", Unit: "percent", Description: "Memory usage reported by the Agent."}, scalarMetric(func(m metrics.MetricEnvelope) float64 { return m.Memory.UsagePercent })},
		{MetricDescriptor{ID: "disk.usage_percent", Name: "Filesystem usage", Unit: "percent", Dimensions: []string{"mount_point"}, Description: "Filesystem capacity usage; missing when a low-power refresh was deferred."}, diskMetric},
		{MetricDescriptor{ID: "network.rx_bytes_per_second", Name: "Network receive rate", Unit: "bytes_per_second", Dimensions: []string{"interface"}, Description: "Per-interface receive rate from the current Agent snapshot."}, networkMetric(func(m metrics.NetworkMetrics) float64 { return m.RXBytesPerSecond })},
		{MetricDescriptor{ID: "network.tx_bytes_per_second", Name: "Network transmit rate", Unit: "bytes_per_second", Dimensions: []string{"interface"}, Description: "Per-interface transmit rate from the current Agent snapshot."}, networkMetric(func(m metrics.NetworkMetrics) float64 { return m.TXBytesPerSecond })},
	}
	registry := &MetricRegistry{descriptors: make(map[string]MetricDescriptor, len(entries)), extractors: make(map[string]metricExtractor, len(entries))}
	for _, entry := range entries {
		registry.descriptors[entry.descriptor.ID] = entry.descriptor
		registry.extractors[entry.descriptor.ID] = entry.extract
	}
	return registry
}

func (r *MetricRegistry) List() []MetricDescriptor {
	items := make([]MetricDescriptor, 0, len(r.descriptors))
	for _, descriptor := range r.descriptors {
		descriptor.Dimensions = append([]string(nil), descriptor.Dimensions...)
		items = append(items, descriptor)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

func (r *MetricRegistry) Lookup(id string) (MetricDescriptor, bool) {
	descriptor, ok := r.descriptors[id]
	descriptor.Dimensions = append([]string(nil), descriptor.Dimensions...)
	return descriptor, ok
}

type QueryService struct {
	source     SnapshotSource
	authorizer MetricAuthorizer
	registry   *MetricRegistry
}

func NewQueryService(source SnapshotSource, authorizer MetricAuthorizer, registry *MetricRegistry) (*QueryService, error) {
	if source == nil || authorizer == nil || registry == nil {
		return nil, errors.New("snapshot source, metric authorizer, and registry are required")
	}
	return &QueryService{source: source, authorizer: authorizer, registry: registry}, nil
}

func (s *QueryService) QueryLatest(ctx context.Context, actor auth.Principal, descriptorID, deviceID string) (MetricResult, error) {
	if actor.UserID == "" || (actor.Role != auth.RoleOwner && actor.Role != auth.RoleViewer) {
		return MetricResult{}, ErrForbidden
	}
	descriptor, ok := s.registry.Lookup(descriptorID)
	if !ok {
		return MetricResult{}, ErrUnsupportedMetric
	}
	if strings.TrimSpace(deviceID) == "" || strings.TrimSpace(deviceID) != deviceID {
		return MetricResult{}, fmt.Errorf("%w: device ID is required and must be trimmed", ErrInvalidRequest)
	}
	if err := s.authorizer.AuthorizeMetric(actor, descriptorID, deviceID); err != nil {
		return MetricResult{}, fmt.Errorf("authorize metric query: %w", err)
	}
	snapshot, ok, err := s.source.Current(ctx, deviceID)
	if err != nil {
		return MetricResult{}, fmt.Errorf("read current metric snapshot: %w", err)
	}
	if !ok {
		return MetricResult{}, ErrSnapshotUnavailable
	}
	if snapshot.Envelope.Device.ID != deviceID {
		return MetricResult{}, ErrInvalidMetricSnapshot
	}
	points := s.registry.extractors[descriptorID](snapshot.Envelope)
	if err := validateMetricPoints(points, descriptor); err != nil {
		return MetricResult{}, fmt.Errorf("%w: %v", ErrInvalidMetricSnapshot, err)
	}
	for index := range points {
		if points[index].Value == nil {
			points[index].Quality = "missing"
		} else if snapshot.Status == "stale" || snapshot.Status == "offline" {
			points[index].Quality = "stale"
		} else {
			points[index].Quality = "fresh"
		}
	}
	if len(points) == 0 {
		points = []MetricPoint{{Quality: "missing"}}
	}
	return MetricResult{
		Descriptor: descriptor,
		DeviceID:   deviceID,
		SampledAt:  snapshot.Envelope.Timestamp.UTC(),
		ReceivedAt: snapshot.ReceivedAt.UTC(),
		Status:     snapshot.Status,
		Points:     points,
	}, nil
}

var (
	ErrUnsupportedMetric     = errors.New("metric descriptor is unsupported")
	ErrSnapshotUnavailable   = errors.New("metric snapshot is unavailable")
	ErrInvalidMetricSnapshot = errors.New("snapshot source returned data outside the metric query contract")
)

func validateMetricPoints(points []MetricPoint, descriptor MetricDescriptor) error {
	for _, point := range points {
		if len(point.Dimensions) != len(descriptor.Dimensions) {
			return errors.New("metric dimensions do not match the descriptor")
		}
		if point.Value != nil && (math.IsNaN(*point.Value) || math.IsInf(*point.Value, 0)) {
			return errors.New("metric value is not finite")
		}
		for _, dimension := range descriptor.Dimensions {
			if _, exists := point.Dimensions[dimension]; !exists {
				return errors.New("metric dimension is missing")
			}
		}
		for key, value := range point.Dimensions {
			if !utf8.ValidString(value) || !containsString(descriptor.Dimensions, key) {
				return errors.New("metric dimension is invalid or undeclared")
			}
		}
	}
	return nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func scalarMetric(read func(metrics.MetricEnvelope) float64) metricExtractor {
	return func(envelope metrics.MetricEnvelope) []MetricPoint {
		value := read(envelope)
		return []MetricPoint{{Value: &value}}
	}
}

func optionalMetric(read func(metrics.MetricEnvelope) *float64) metricExtractor {
	return func(envelope metrics.MetricEnvelope) []MetricPoint {
		value := read(envelope)
		if value == nil {
			return []MetricPoint{{}}
		}
		copyOfValue := *value
		return []MetricPoint{{Value: &copyOfValue}}
	}
}

func diskMetric(envelope metrics.MetricEnvelope) []MetricPoint {
	points := make([]MetricPoint, 0, len(envelope.Disks))
	for _, disk := range envelope.Disks {
		point := MetricPoint{Dimensions: map[string]string{"mount_point": disk.MountPoint}}
		if disk.CapacityAvailable {
			value := disk.UsagePercent
			point.Value = &value
		}
		points = append(points, point)
	}
	return points
}

func networkMetric(read func(metrics.NetworkMetrics) float64) metricExtractor {
	return func(envelope metrics.MetricEnvelope) []MetricPoint {
		points := make([]MetricPoint, 0, len(envelope.Networks))
		for _, network := range envelope.Networks {
			value := read(network)
			points = append(points, MetricPoint{Value: &value, Dimensions: map[string]string{"interface": network.Interface}})
		}
		return points
	}
}

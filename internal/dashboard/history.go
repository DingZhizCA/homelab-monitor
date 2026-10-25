/*
Package dashboard provides bounded historical metric queries.

The service enforces caller supplied range and point budgets before reading a
history source. Storage adapters own retention and aggregation semantics.
*/
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DingZhizCA/homelab-monitor/internal/auth"
)

// HistoricalPoint is one persisted observation returned by a history source.
type HistoricalPoint struct {
	DeviceID          string            `json:"device_id"`
	DescriptorID      string            `json:"descriptor_id"`
	SampledAt         time.Time         `json:"sampled_at"`
	Value             *float64          `json:"value,omitempty"`
	Dimensions        map[string]string `json:"dimensions,omitempty"`
	Quality           string            `json:"quality"`
	ResolutionSeconds int               `json:"resolution_seconds"`
	AggregationKind   string            `json:"aggregation_kind"`
}

// HistoryRequest selects observations in the half-open interval [From, To).
type HistoryRequest struct {
	DescriptorID string
	DeviceID     string
	From         time.Time
	To           time.Time
	Limit        int
}

// HistorySource must return at most Limit observations for the requested
// descriptor, device, and half-open interval. It must not trigger collection.
type HistorySource interface {
	QueryHistory(context.Context, HistoryRequest) ([]HistoricalPoint, error)
}

// HistoryBudget bounds one history query. Values must be selected explicitly
// by the caller so this package does not introduce storage or product defaults.
type HistoryBudget struct {
	MaxRange  time.Duration
	MaxPoints int
}

// HistoryResult contains a bounded, chronologically ordered history response.
type HistoryResult struct {
	Descriptor MetricDescriptor  `json:"descriptor"`
	DeviceID   string            `json:"device_id"`
	From       time.Time         `json:"from"`
	To         time.Time         `json:"to"`
	Points     []HistoricalPoint `json:"points"`
}

// HistoryService authorizes and bounds access to a historical metric source.
type HistoryService struct {
	source     HistorySource
	authorizer MetricAuthorizer
	registry   *MetricRegistry
	budget     HistoryBudget
}

// NewHistoryService requires explicit dependencies and query bounds.
func NewHistoryService(source HistorySource, authorizer MetricAuthorizer, registry *MetricRegistry, budget HistoryBudget) (*HistoryService, error) {
	if source == nil || authorizer == nil || registry == nil || budget.MaxRange <= 0 || budget.MaxPoints <= 0 {
		return nil, errors.New("history source, metric authorizer, registry, and positive query budgets are required")
	}
	return &HistoryService{source: source, authorizer: authorizer, registry: registry, budget: budget}, nil
}

// Query validates, authorizes, and returns one bounded historical metric query.
func (s *HistoryService) Query(ctx context.Context, actor auth.Principal, request HistoryRequest) (HistoryResult, error) {
	if actor.UserID == "" || (actor.Role != auth.RoleOwner && actor.Role != auth.RoleViewer) {
		return HistoryResult{}, ErrForbidden
	}
	descriptor, ok := s.registry.Lookup(request.DescriptorID)
	if !ok {
		return HistoryResult{}, ErrUnsupportedMetric
	}
	if strings.TrimSpace(request.DeviceID) == "" || strings.TrimSpace(request.DeviceID) != request.DeviceID || request.From.IsZero() || request.To.IsZero() || !request.From.Before(request.To) || request.To.Sub(request.From) > s.budget.MaxRange {
		return HistoryResult{}, fmt.Errorf("%w: device and a positive range within the configured maximum are required", ErrInvalidRequest)
	}
	if request.Limit < 1 || request.Limit > s.budget.MaxPoints {
		return HistoryResult{}, fmt.Errorf("%w: point limit must be between 1 and %d", ErrInvalidRequest, s.budget.MaxPoints)
	}
	if err := s.authorizer.AuthorizeMetric(actor, request.DescriptorID, request.DeviceID); err != nil {
		return HistoryResult{}, fmt.Errorf("authorize metric history query: %w", err)
	}
	request.From = request.From.UTC()
	request.To = request.To.UTC()
	points, err := s.source.QueryHistory(ctx, request)
	if err != nil {
		return HistoryResult{}, fmt.Errorf("read metric history: %w", err)
	}
	if len(points) > request.Limit {
		return HistoryResult{}, ErrHistorySourceLimit
	}
	points = append([]HistoricalPoint(nil), points...)
	for index := range points {
		point := &points[index]
		// Empty kind is accepted from older injected sources and normalized to raw.
		if point.AggregationKind == "" && point.ResolutionSeconds == 0 {
			point.AggregationKind = "raw"
		}
		if point.DeviceID != request.DeviceID || point.DescriptorID != request.DescriptorID || point.SampledAt.Before(request.From) || !point.SampledAt.Before(request.To) || point.SampledAt.IsZero() {
			return HistoryResult{}, ErrInvalidHistoryPoint
		}
		if !validHistoricalQuality(point.Quality) || !validHistoricalAggregation(point.ResolutionSeconds, point.AggregationKind) || len(point.Dimensions) != len(descriptor.Dimensions) {
			return HistoryResult{}, ErrInvalidHistoryPoint
		}
		if point.Value != nil && (math.IsNaN(*point.Value) || math.IsInf(*point.Value, 0)) {
			return HistoryResult{}, ErrInvalidHistoryPoint
		}
		for _, dimension := range descriptor.Dimensions {
			if _, exists := point.Dimensions[dimension]; !exists {
				return HistoryResult{}, ErrInvalidHistoryPoint
			}
		}
		for key, value := range point.Dimensions {
			if !slices.Contains(descriptor.Dimensions, key) || !utf8.ValidString(value) {
				return HistoryResult{}, ErrInvalidHistoryPoint
			}
		}
		point.SampledAt = point.SampledAt.UTC()
		if point.Value != nil {
			value := *point.Value
			point.Value = &value
		}
		point.Dimensions = cloneDimensions(point.Dimensions)
	}
	sort.SliceStable(points, func(i, j int) bool {
		if points[i].SampledAt.Equal(points[j].SampledAt) {
			return dimensionKey(points[i].Dimensions) < dimensionKey(points[j].Dimensions)
		}
		return points[i].SampledAt.Before(points[j].SampledAt)
	})
	return HistoryResult{Descriptor: descriptor, DeviceID: request.DeviceID, From: request.From, To: request.To, Points: points}, nil
}

func validHistoricalAggregation(resolution int, kind string) bool {
	return resolution == 0 && kind == "raw" || resolution > 0 && kind == "gauge_mean"
}

func validHistoricalQuality(quality string) bool {
	switch quality {
	case "valid", "partial", "estimated", "missing", "reset", "stale":
		return true
	default:
		return false
	}
}

func cloneDimensions(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func dimensionKey(dimensions map[string]string) string {
	keys := make([]string, 0, len(dimensions))
	for key := range dimensions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var result string
	for _, key := range keys {
		result += key + "\x00" + dimensions[key] + "\x00"
	}
	return result
}

var (
	// ErrHistorySourceLimit reports a source violating the requested point cap.
	ErrHistorySourceLimit = errors.New("history source exceeded the requested point limit")
	// ErrInvalidHistoryPoint reports data outside the descriptor or requested range.
	ErrInvalidHistoryPoint = errors.New("history source returned an observation outside the request")
)

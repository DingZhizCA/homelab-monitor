/*
Package pluginmetrics defines authenticated, bounded plugin history queries.

Plugin identity remains structured and includes package version so histories
with potentially different semantics are never merged implicitly.
*/
package pluginmetrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/auth"
)

var (
	ErrForbidden      = errors.New("plugin metric access is forbidden")
	ErrInvalidRequest = errors.New("invalid plugin history request")
	ErrInvalidPoint   = errors.New("invalid plugin history point")
	ErrSourceLimit    = errors.New("plugin history source exceeded the point limit")
	ErrScanLimit      = errors.New("plugin history scan limit was reached")
	historyIdentifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
	historyVersion    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.+_-]{0,127}$`)
)

// HistoryRequest selects one exact plugin metric series family.
type HistoryRequest struct {
	DeviceID      string
	PluginID      string
	PluginVersion string
	CollectorID   string
	MetricID      string
	From          time.Time
	To            time.Time
	Limit         int
}

// HistoricalPoint retains typed JSON values and label dimensions.
type HistoricalPoint struct {
	ObservedAt time.Time         `json:"observed_at"`
	Type       string            `json:"type"`
	Unit       string            `json:"unit"`
	Value      json.RawMessage   `json:"value"`
	Quality    string            `json:"quality"`
	Labels     map[string]string `json:"labels,omitempty"`
}

// HistoryResult echoes the exact structured identity selected by the caller.
type HistoryResult struct {
	DeviceID      string            `json:"device_id"`
	PluginID      string            `json:"plugin_id"`
	PluginVersion string            `json:"plugin_version"`
	CollectorID   string            `json:"collector_id"`
	MetricID      string            `json:"metric_id"`
	From          time.Time         `json:"from"`
	To            time.Time         `json:"to"`
	Points        []HistoricalPoint `json:"points"`
}

// HistorySource reads data without triggering collection.
type HistorySource interface {
	QueryPluginHistory(context.Context, HistoryRequest) ([]HistoricalPoint, error)
}

// HistoryBudget provides explicit request bounds.
type HistoryBudget struct {
	MaxRange  time.Duration
	MaxPoints int
}

// HistoryService validates access and verifies storage output.
type HistoryService struct {
	source HistorySource
	budget HistoryBudget
}

// NewHistoryService requires a source and explicit range and point budgets.
func NewHistoryService(source HistorySource, budget HistoryBudget) (*HistoryService, error) {
	if source == nil || budget.MaxRange <= 0 || budget.MaxPoints < 1 {
		return nil, errors.New("plugin history source and positive budgets are required")
	}
	return &HistoryService{source: source, budget: budget}, nil
}

// Query allows authenticated Owner and Viewer principals to read one series family.
func (s *HistoryService) Query(ctx context.Context, principal auth.Principal, request HistoryRequest) (HistoryResult, error) {
	if principal.UserID == "" || (principal.Role != auth.RoleOwner && principal.Role != auth.RoleViewer) {
		return HistoryResult{}, ErrForbidden
	}
	if err := validateRequest(request, s.budget); err != nil {
		return HistoryResult{}, err
	}
	request.From, request.To = request.From.UTC(), request.To.UTC()
	points, err := s.source.QueryPluginHistory(ctx, request)
	if err != nil {
		return HistoryResult{}, fmt.Errorf("read plugin metric history: %w", err)
	}
	if len(points) > request.Limit {
		return HistoryResult{}, ErrSourceLimit
	}
	points = append([]HistoricalPoint(nil), points...)
	for index := range points {
		point := &points[index]
		if point.ObservedAt.IsZero() || point.ObservedAt.Before(request.From) || !point.ObservedAt.Before(request.To) || !json.Valid(point.Value) || strings.TrimSpace(point.Unit) == "" || !validType(point.Type) || !validQuality(point.Quality) {
			return HistoryResult{}, ErrInvalidPoint
		}
		point.ObservedAt = point.ObservedAt.UTC()
		point.Value = append(json.RawMessage(nil), point.Value...)
		point.Labels = cloneLabels(point.Labels)
	}
	sort.SliceStable(points, func(i, j int) bool {
		if points[i].ObservedAt.Equal(points[j].ObservedAt) {
			return labelKey(points[i].Labels) < labelKey(points[j].Labels)
		}
		return points[i].ObservedAt.Before(points[j].ObservedAt)
	})
	return HistoryResult{DeviceID: request.DeviceID, PluginID: request.PluginID, PluginVersion: request.PluginVersion, CollectorID: request.CollectorID, MetricID: request.MetricID, From: request.From, To: request.To, Points: points}, nil
}

func validateRequest(request HistoryRequest, budget HistoryBudget) error {
	if strings.TrimSpace(request.DeviceID) == "" || strings.TrimSpace(request.DeviceID) != request.DeviceID || !historyIdentifier.MatchString(request.PluginID) || !historyVersion.MatchString(request.PluginVersion) || !historyIdentifier.MatchString(request.CollectorID) || !historyIdentifier.MatchString(request.MetricID) || request.From.IsZero() || request.To.IsZero() || !request.From.Before(request.To) || request.To.Sub(request.From) > budget.MaxRange || request.Limit < 1 || request.Limit > budget.MaxPoints {
		return ErrInvalidRequest
	}
	return nil
}

func validType(value string) bool {
	return value == "counter" || value == "gauge" || value == "state" || value == "event"
}
func validQuality(value string) bool {
	return value == "valid" || value == "partial" || value == "estimated" || value == "missing" || value == "reset" || value == "stale"
}

func cloneLabels(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func labelKey(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var result string
	for _, key := range keys {
		result += key + "\x00" + labels[key] + "\x00"
	}
	return result
}

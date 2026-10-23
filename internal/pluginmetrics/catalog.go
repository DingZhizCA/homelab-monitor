/*
Package pluginmetrics defines the authenticated catalog of metric schemas that
the Server has actually observed from one device and plugin version.
*/
package pluginmetrics

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/auth"
)

var ErrDescriptorLimit = errors.New("plugin metric descriptor limit was reached")

type Descriptor struct {
	DeviceID      string    `json:"device_id"`
	PluginID      string    `json:"plugin_id"`
	PluginVersion string    `json:"plugin_version"`
	CollectorID   string    `json:"collector_id"`
	MetricID      string    `json:"metric_id"`
	Type          string    `json:"type"`
	Unit          string    `json:"unit"`
	LabelKeys     []string  `json:"label_keys"`
	FirstObserved time.Time `json:"first_observed_at"`
	LastObserved  time.Time `json:"last_observed_at"`
}

type DescriptorSource interface {
	ListPluginMetricDescriptors(context.Context, string, int) ([]Descriptor, error)
}

type CatalogService struct {
	source         DescriptorSource
	maxDescriptors int
}

func NewCatalogService(source DescriptorSource, maxDescriptors int) (*CatalogService, error) {
	if source == nil || maxDescriptors < 1 {
		return nil, errors.New("plugin metric descriptor source and positive limit are required")
	}
	return &CatalogService{source: source, maxDescriptors: maxDescriptors}, nil
}

func (s *CatalogService) List(ctx context.Context, principal auth.Principal, deviceID string) ([]Descriptor, error) {
	if principal.UserID == "" || (principal.Role != auth.RoleOwner && principal.Role != auth.RoleViewer) {
		return nil, ErrForbidden
	}
	if strings.TrimSpace(deviceID) == "" || strings.TrimSpace(deviceID) != deviceID {
		return nil, ErrInvalidRequest
	}
	items, err := s.source.ListPluginMetricDescriptors(ctx, deviceID, s.maxDescriptors)
	if err != nil {
		return nil, err
	}
	if len(items) > s.maxDescriptors {
		return nil, ErrDescriptorLimit
	}
	for index := range items {
		item := &items[index]
		if item.DeviceID != deviceID || !historyIdentifier.MatchString(item.PluginID) || !historyVersion.MatchString(item.PluginVersion) || !historyIdentifier.MatchString(item.CollectorID) || !historyIdentifier.MatchString(item.MetricID) || !validType(item.Type) || strings.TrimSpace(item.Unit) == "" || item.FirstObserved.IsZero() || item.LastObserved.Before(item.FirstObserved) {
			return nil, ErrInvalidPoint
		}
		item.LabelKeys = append([]string(nil), item.LabelKeys...)
		seen := make(map[string]struct{}, len(item.LabelKeys))
		for _, key := range item.LabelKeys {
			if !historyIdentifier.MatchString(key) {
				return nil, ErrInvalidPoint
			}
			if _, exists := seen[key]; exists {
				return nil, ErrInvalidPoint
			}
			seen[key] = struct{}{}
		}
		sort.Strings(item.LabelKeys)
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.PluginID != b.PluginID {
			return a.PluginID < b.PluginID
		}
		if a.PluginVersion != b.PluginVersion {
			return a.PluginVersion < b.PluginVersion
		}
		if a.CollectorID != b.CollectorID {
			return a.CollectorID < b.CollectorID
		}
		return a.MetricID < b.MetricID
	})
	return items, nil
}

/*
Package dashboard validates that a shared Dashboard only publishes built-in or
versioned plugin metrics available through the authenticated Viewer query
surface. Device scope remains unrestricted while both human roles share the
same metric read set.
*/
package dashboard

import (
	"context"
	"errors"
	"fmt"
)

// ViewerPublicationPolicy checks shared documents against the active registry.
type ViewerPublicationPolicy struct {
	registry      *MetricRegistry
	pluginMetrics PluginMetricPublicationSource
}

// PluginMetricPublicationSource checks an exact observed plugin descriptor.
type PluginMetricPublicationSource interface {
	HasPluginMetricDescriptor(context.Context, string, PluginMetricReference) (bool, error)
}

// NewViewerPublicationPolicy binds publication validation to queryable metrics.
func NewViewerPublicationPolicy(registry *MetricRegistry, pluginMetrics PluginMetricPublicationSource) (*ViewerPublicationPolicy, error) {
	if registry == nil {
		return nil, errors.New("metric registry is required")
	}
	return &ViewerPublicationPolicy{registry: registry, pluginMetrics: pluginMetrics}, nil
}

// ValidateViewerAccess rejects descriptor references absent from Viewer APIs.
func (p *ViewerPublicationPolicy) ValidateViewerAccess(ctx context.Context, document Document) error {
	if ctx == nil {
		return errors.New("publication context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := document.Validate(); err != nil {
		return fmt.Errorf("validate Dashboard document: %w", err)
	}
	for _, widget := range document.Widgets {
		if widget.PluginMetric != nil {
			if p.pluginMetrics == nil {
				return fmt.Errorf("%w: widget %q references unavailable plugin metrics", ErrForbidden, widget.ID)
			}
			exists, err := p.pluginMetrics.HasPluginMetricDescriptor(ctx, widget.DeviceID, *widget.PluginMetric)
			if err != nil {
				return fmt.Errorf("check plugin metric for widget %q: %w", widget.ID, err)
			}
			if !exists {
				return fmt.Errorf("%w: widget %q references an unobserved plugin metric", ErrForbidden, widget.ID)
			}
			continue
		}
		if widget.DescriptorID == "" {
			continue
		}
		if _, ok := p.registry.Lookup(widget.DescriptorID); !ok {
			return fmt.Errorf("%w: widget %q references descriptor %q", ErrForbidden, widget.ID, widget.DescriptorID)
		}
	}
	return nil
}

var _ PublicationPolicy = (*ViewerPublicationPolicy)(nil)

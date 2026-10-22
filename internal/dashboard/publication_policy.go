/*
Package dashboard validates that a shared Dashboard only publishes metric
descriptors available through the authenticated Viewer query surface. Device
scope remains unrestricted because ADR-020 grants both human roles the same
built-in metric read set.
*/
package dashboard

import (
	"context"
	"errors"
	"fmt"
)

// ViewerPublicationPolicy checks shared documents against the active registry.
type ViewerPublicationPolicy struct {
	registry *MetricRegistry
}

// NewViewerPublicationPolicy binds publication validation to one registry.
func NewViewerPublicationPolicy(registry *MetricRegistry) (*ViewerPublicationPolicy, error) {
	if registry == nil {
		return nil, errors.New("metric registry is required")
	}
	return &ViewerPublicationPolicy{registry: registry}, nil
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

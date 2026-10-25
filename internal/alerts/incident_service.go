/*
Package alerts exposes authenticated read access to current Incident states.

The query service preserves repository ordering and device isolation. It does
not mutate transitions, select Incident policy, or broaden metric visibility.
*/
package alerts

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/DingZhizCA/homelab-monitor/internal/auth"
)

// IncidentStateSource is the read-only repository surface needed by queries.
type IncidentStateSource interface {
	ListCurrent(context.Context, string) ([]IncidentState, error)
}

// IncidentService authorizes device-scoped current-state queries.
type IncidentService struct {
	source IncidentStateSource
}

// NewIncidentService requires a current-state source.
func NewIncidentService(source IncidentStateSource) (*IncidentService, error) {
	if source == nil {
		return nil, errors.New("Incident state source is required")
	}
	return &IncidentService{source: source}, nil
}

// ListCurrent returns current states for one device to an authenticated user.
func (s *IncidentService) ListCurrent(ctx context.Context, actor auth.Principal, deviceID string) ([]IncidentState, error) {
	if strings.TrimSpace(actor.UserID) == "" || (actor.Role != auth.RoleOwner && actor.Role != auth.RoleViewer) {
		return nil, auth.ErrForbidden
	}
	if ctx == nil {
		return nil, errors.New("Incident query context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateIncidentDeviceID(deviceID); err != nil {
		return nil, fmt.Errorf("%w: device ID must be non-empty and trimmed", ErrInvalidIncidentRequest)
	}
	return s.source.ListCurrent(ctx, deviceID)
}

// ErrInvalidIncidentRequest reports a malformed current-state query.
var ErrInvalidIncidentRequest = errors.New("invalid Incident request")

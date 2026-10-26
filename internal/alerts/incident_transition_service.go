/*
Package alerts authorizes bounded reads of durable Incident status changes.
It does not evaluate rules, mutate current state, or select retention policy.
*/
package alerts

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/DingZhizCA/homelab-monitor/internal/auth"
)

// IncidentTransitionSource reads newest status changes for one rule.
type IncidentTransitionSource interface {
	ListTransitions(context.Context, string, string, int) ([]IncidentTransition, error)
}

// IncidentTransitionService authorizes bounded transition-history reads.
type IncidentTransitionService struct {
	source   IncidentTransitionSource
	maxLimit int
}

// NewIncidentTransitionService requires a source and explicit query ceiling.
func NewIncidentTransitionService(source IncidentTransitionSource, maxLimit int) (*IncidentTransitionService, error) {
	if source == nil || maxLimit < 1 {
		return nil, errors.New("Incident transition source and positive query limit are required")
	}
	return &IncidentTransitionService{source: source, maxLimit: maxLimit}, nil
}

// List returns newest transitions for one device-scoped rule.
func (s *IncidentTransitionService) List(ctx context.Context, actor auth.Principal, deviceID, ruleID string, limit int) ([]IncidentTransition, error) {
	if strings.TrimSpace(actor.UserID) == "" || (actor.Role != auth.RoleOwner && actor.Role != auth.RoleViewer) {
		return nil, auth.ErrForbidden
	}
	if ctx == nil {
		return nil, errors.New("Incident transition query context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateIncidentIdentity(deviceID, ruleID); err != nil || limit < 1 || limit > s.maxLimit {
		return nil, fmt.Errorf("%w: valid device, rule, and bounded limit are required", ErrInvalidIncidentRequest)
	}
	transitions, err := s.source.ListTransitions(ctx, deviceID, ruleID, limit)
	if err != nil {
		return nil, err
	}
	if transitions == nil {
		transitions = make([]IncidentTransition, 0)
	}
	return transitions, nil
}

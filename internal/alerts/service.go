/*
Package alerts exposes the Owner-authorized rule-management boundary.

The service validates rule structure and revision preconditions before calling
an injected repository. It does not define default rules, evaluate incidents,
or select persistence and notification policies.
*/
package alerts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/audit"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
)

// RuleService applies Owner authorization to alert-rule repository operations.
type RuleService struct {
	repository RuleRepository
	registry   Registry
	limits     Limits
}

// NewRuleService requires an explicit repository, metric registry, and expression limits.
func NewRuleService(repository RuleRepository, registry Registry, limits Limits) (*RuleService, error) {
	if repository == nil || registry == nil || limits.MaxDepth < 1 || limits.MaxConditions < 1 {
		return nil, errors.New("rule repository, metric registry, and positive expression limits are required")
	}
	return &RuleService{repository: repository, registry: registry, limits: limits}, nil
}

// ListCurrent returns rules for one device to an authenticated Owner.
func (s *RuleService) ListCurrent(ctx context.Context, actor auth.Principal, deviceID string) ([]Rule, error) {
	if err := authorizeRuleOwner(actor); err != nil {
		return nil, err
	}
	if err := checkRuleRepositoryContext(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(deviceID) == "" || strings.TrimSpace(deviceID) != deviceID {
		return nil, fmt.Errorf("%w: device ID must be non-empty and trimmed", ErrInvalidRuleRequest)
	}
	return s.repository.ListCurrent(ctx, deviceID)
}

// Current returns one rule revision to an authenticated Owner.
func (s *RuleService) Current(ctx context.Context, actor auth.Principal, deviceID, ruleID string) (Rule, bool, error) {
	if err := authorizeRuleOwner(actor); err != nil {
		return Rule{}, false, err
	}
	if err := checkRuleRepositoryContext(ctx); err != nil {
		return Rule{}, false, err
	}
	if strings.TrimSpace(deviceID) == "" || strings.TrimSpace(deviceID) != deviceID || !ruleIDPattern.MatchString(ruleID) {
		return Rule{}, false, fmt.Errorf("%w: valid device and rule identities are required", ErrInvalidRuleRequest)
	}
	return s.repository.Current(ctx, deviceID, ruleID)
}

// SaveRevision records an Owner-approved rule change using an expected-current CAS value.
func (s *RuleService) SaveRevision(ctx context.Context, actor auth.Principal, rule Rule, expectedCurrent uint64) error {
	if err := authorizeRuleOwner(actor); err != nil {
		return err
	}
	if err := checkRuleRepositoryContext(ctx); err != nil {
		return err
	}
	if err := rule.Validate(s.registry, s.limits); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRuleRequest, err)
	}
	event, err := audit.NewEvent(time.Now(), actor.UserID, RuleAuditAction, RuleAuditResourceType, RuleAuditResourceID(rule))
	if err != nil {
		return fmt.Errorf("create alert-rule audit event: %w", err)
	}
	return s.repository.SaveRevisionWithAudit(ctx, actor.UserID, rule, expectedCurrent, event)
}

func authorizeRuleOwner(actor auth.Principal) error {
	if strings.TrimSpace(actor.UserID) == "" || actor.Role != auth.RoleOwner {
		return auth.ErrForbidden
	}
	return nil
}

var ErrInvalidRuleRequest = errors.New("invalid alert rule request")

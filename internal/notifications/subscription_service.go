/*
Package notifications exposes authenticated self-service subscription changes.
The service binds every mutation to the Session user and verifies the exact
alert rule exists before committing a revision and audit event.
*/
package notifications

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/alerts"
	"github.com/DingZhizCA/homelab-monitor/internal/audit"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
)

var ErrSubscriptionRuleNotFound = errors.New("notification subscription alert rule not found")

type SubscriptionRuleSource interface {
	Current(ctx context.Context, deviceID, ruleID string) (alerts.Rule, bool, error)
}

type SubscriptionService struct {
	repository SubscriptionRepository
	rules      SubscriptionRuleSource
	now        func() time.Time
}

func NewSubscriptionService(repository SubscriptionRepository, rules SubscriptionRuleSource, now func() time.Time) (*SubscriptionService, error) {
	if repository == nil || rules == nil || now == nil {
		return nil, errors.New("notification subscription repository, rule source, and clock are required")
	}
	return &SubscriptionService{repository: repository, rules: rules, now: now}, nil
}

func (s *SubscriptionService) List(ctx context.Context, actor auth.Principal, limit int) ([]Subscription, error) {
	if err := authorizeSubscriptionUser(ctx, actor); err != nil {
		return nil, err
	}
	return s.repository.ListForUser(ctx, actor.UserID, limit)
}

func (s *SubscriptionService) Save(ctx context.Context, actor auth.Principal, subscription Subscription, expectedRevision uint64) error {
	if err := authorizeSubscriptionUser(ctx, actor); err != nil {
		return err
	}
	subscription.UserID = actor.UserID
	if err := ValidateSubscription(subscription); err != nil {
		return err
	}
	if _, found, err := s.rules.Current(ctx, subscription.DeviceID, subscription.RuleID); err != nil {
		return err
	} else if !found {
		return ErrSubscriptionRuleNotFound
	}
	event, err := audit.NewEvent(s.now().UTC(), actor.UserID, SubscriptionAuditAction, SubscriptionResourceType, SubscriptionResourceID(subscription))
	if err != nil {
		return fmt.Errorf("create notification subscription audit event: %w", err)
	}
	return s.repository.SaveWithAudit(ctx, actor.UserID, subscription, expectedRevision, event)
}

func authorizeSubscriptionUser(ctx context.Context, actor auth.Principal) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(actor.UserID) == "" || (actor.Role != auth.RoleOwner && actor.Role != auth.RoleViewer) {
		return auth.ErrForbidden
	}
	return nil
}
